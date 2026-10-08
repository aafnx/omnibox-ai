package openrouter

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aafnx/omnibox-ai/internal/platform"
)

// KeyFile is the key's place under the data directory.
const KeyFile = "run/openrouter-key.bin"

// KeyStore keeps the OpenRouter key encrypted in one file (see the package comment).
type KeyStore struct {
	// Path is <data>/run/openrouter-key.bin.
	Path string
}

// Saved is the stored key. Only Hint and Saved may be shown in the panel.
type Saved struct {
	Key   string
	Saved time.Time
	// Hint is "…" and the last four characters, for the panel.
	Hint string
}

type savedKey struct {
	Key   string    `json:"key"`
	Saved time.Time `json:"saved"`
}

// Load returns the stored key; false when there is none or it cannot be decrypted (a file
// copied from another PC).
func (s KeyStore) Load() (Saved, bool) {
	sealed, err := platform.ReadShared(s.Path)
	if err != nil || len(sealed) == 0 {
		return Saved{}, false
	}
	plain, err := platform.Unprotect(sealed)
	if err != nil {
		return Saved{}, false
	}
	var v savedKey
	if json.Unmarshal(plain, &v) != nil || checkKey(v.Key) != nil {
		return Saved{}, false
	}
	return Saved{Key: v.Key, Saved: v.Saved, Hint: hint(v.Key)}, true
}

// Save checks and stores key, replacing the previous one. Surrounding spaces from a paste are
// dropped.
func (s KeyStore) Save(key string, now time.Time) error {
	key = strings.TrimSpace(key)
	if err := checkKey(key); err != nil {
		return err
	}
	plain, err := json.Marshal(savedKey{Key: key, Saved: now.UTC()})
	if err != nil {
		return err
	}
	sealed, err := platform.Protect(plain)
	if err != nil {
		return errors.New("Ключ OpenRouter не зашифрован: системное шифрование недоступно.")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("Каталог для ключа OpenRouter не создан: %v", err)
	}
	if err := platform.WriteAtomic(s.Path, sealed, 0o600); err != nil {
		return fmt.Errorf("Ключ OpenRouter не сохранён: %v", err)
	}
	return nil
}

// Forget removes the stored key; no key is not an error.
func (s KeyStore) Forget() error {
	if err := os.Remove(s.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Ключ OpenRouter не удалён: %v", err)
	}
	return nil
}

// checkKey validates the shape of a key. Its messages never quote the key.
func checkKey(key string) error {
	if !strings.HasPrefix(key, "sk-or-") {
		return errors.New("Ключ OpenRouter должен начинаться с sk-or-.")
	}
	if len(key) < 20 || len(key) > 200 {
		return errors.New("Ключ OpenRouter должен быть длиной от 20 до 200 символов.")
	}
	if strings.IndexFunc(key, func(r rune) bool { return r <= ' ' || r == 0x7f || r > 0x7e }) >= 0 {
		return errors.New("В ключе OpenRouter не должно быть пробелов и других лишних символов.")
	}
	return nil
}

func hint(key string) string {
	if len(key) < 4 {
		return ""
	}
	return "…" + key[len(key)-4:]
}
