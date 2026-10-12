package websearch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aafnx/omnibox-ai/internal/platform"
)

// KeyFile keeps the search engines' keys under the data directory, encrypted like the
// OpenRouter key (DPAPI on Windows): only the service reads them, the panel sees a hint.
const KeyFile = "run/search-keys.bin"

// Keys stores the keys of the engines in KeyEngines.
type Keys struct {
	Path string
}

// KeyInfo is what the panel may see of a key.
type KeyInfo struct {
	Hint  string    `json:"hint"`
	Saved time.Time `json:"saved"`
}

type storedKey struct {
	Key   string    `json:"key"`
	Saved time.Time `json:"saved"`
}

func (k Keys) load() map[string]storedKey {
	out := map[string]storedKey{}
	sealed, err := platform.ReadShared(k.Path)
	if err != nil || len(sealed) == 0 {
		return out
	}
	plain, err := platform.Unprotect(sealed)
	if err != nil || json.Unmarshal(plain, &out) != nil {
		return map[string]storedKey{}
	}
	return out
}

// Get returns the key of an engine, "" without one.
func (k Keys) Get(engine string) string { return k.load()[engine].Key }

// Info lists the saved keys for the panel, without the keys themselves.
func (k Keys) Info() map[string]KeyInfo {
	out := map[string]KeyInfo{}
	for name, v := range k.load() {
		if slices.Contains(KeyEngines, name) && len(v.Key) >= 4 {
			out[name] = KeyInfo{Hint: "…" + v.Key[len(v.Key)-4:], Saved: v.Saved}
		}
	}
	return out
}

// Save stores an engine's key; an empty key forgets it.
func (k Keys) Save(engine, key string, now time.Time) error {
	if !slices.Contains(KeyEngines, engine) {
		return errors.New("Ключ нужен только для Perplexity, Tavily и Brave Search.")
	}
	key = strings.TrimSpace(key)
	all := k.load()
	if key == "" {
		delete(all, engine)
	} else {
		if err := CheckKey(engine, key); err != nil {
			return err
		}
		all[engine] = storedKey{Key: key, Saved: now.UTC()}
	}
	if len(all) == 0 {
		if err := os.Remove(k.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("Ключ не удалён: %v", err)
		}
		return nil
	}
	plain, _ := json.Marshal(all)
	sealed, err := platform.Protect(plain)
	if err != nil {
		return errors.New("Ключ не зашифрован: системное шифрование недоступно.")
	}
	if err := os.MkdirAll(filepath.Dir(k.Path), 0o700); err != nil {
		return err
	}
	if err := platform.WriteAtomic(k.Path, sealed, 0o600); err != nil {
		return fmt.Errorf("Ключ не сохранён: %v", err)
	}
	return nil
}

// CheckKey validates a key's shape; its messages never quote the key.
func CheckKey(engine, key string) error {
	name := EngineName(engine)
	if len(key) < 16 || len(key) > 300 || strings.IndexFunc(key, func(r rune) bool { return r <= ' ' || r > 0x7e }) >= 0 {
		return fmt.Errorf("Ключ %s: от 16 до 300 символов, без пробелов.", name)
	}
	switch {
	case engine == EnginePerplexity && !strings.HasPrefix(key, "pplx-"):
		return errors.New("Ключ Perplexity начинается с pplx-.")
	case engine == EngineTavily && !strings.HasPrefix(key, "tvly-"):
		return errors.New("Ключ Tavily начинается с tvly-.")
	}
	return nil
}
