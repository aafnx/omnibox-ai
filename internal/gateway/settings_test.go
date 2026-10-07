package gateway

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
)

func settingsStore(t *testing.T) *config.Store {
	t.Helper()
	c := config.Default()
	c.Panel.Hosts = []string{"localhost"}
	b64 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	c.Panel.Password = config.Password{Salt: b64, Hash: b64, Iterations: 210000}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	return config.NewStore(path, c)
}

func TestKeyIsShownOnceAndOnlyItsHashIsStored(t *testing.T) {
	store := settingsStore(t)
	key, err := UpdateSettings(store, map[string]any{"action": "generate"})
	if err != nil || !strings.HasPrefix(key, "obai-") || len(key) != 69 {
		t.Fatalf("%q %v", key, err)
	}
	sum := sha256.Sum256([]byte(key))
	c := store.Get()
	if c.API.KeyHash != hex.EncodeToString(sum[:]) || !c.API.RequireKey {
		t.Fatalf("%+v", c.API)
	}
	saved, _ := os.ReadFile(store.Path())
	if strings.Contains(string(saved), key) {
		t.Fatal("the key itself was saved")
	}
	if v := SettingsView(store.Get()); !v.HasKey || strings.Contains(strings.ToLower(v.PublicURL), "obai") {
		t.Fatalf("%+v", v)
	}
	second, _ := UpdateSettings(store, map[string]any{"action": "generate"})
	if second == key || store.Get().API.KeyHash == hex.EncodeToString(sum[:]) {
		t.Fatal("a new key did not revoke the old one")
	}
}

func TestSaveValidatesSettings(t *testing.T) {
	store := settingsStore(t)
	if _, err := UpdateSettings(store, map[string]any{"action": "save", "require_key": true}); err == nil || !strings.Contains(err.Error(), "создайте API key") {
		t.Fatalf("key required without a key: %v", err)
	}
	if _, err := UpdateSettings(store, map[string]any{"action": "save", "enabled": true, "base_url": "https://llm.fnx.red/v1/", "queue_depth": float64(2), "queue_timeout_seconds": float64(30), "require_key": false}); err != nil {
		t.Fatal(err)
	}
	v := SettingsView(store.Get())
	if !v.Enabled || v.PublicURL != "https://llm.fnx.red/v1" || v.QueueDepth != 2 || v.QueueTimeoutSeconds != 30 {
		t.Fatalf("%+v", v)
	}
	for _, bad := range []map[string]any{
		{"action": "save", "base_url": "https://user:pw@x/v1"}, {"action": "save", "queue_depth": float64(99)},
		{"action": "save", "key_hash": strings.Repeat("a", 64)}, {"action": "drop"}, {"action": "generate", "require_key": false},
	} {
		if _, err := UpdateSettings(store, bad); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if store.Get().API.QueueDepth != 2 {
		t.Fatal("a refused change was applied")
	}
}
