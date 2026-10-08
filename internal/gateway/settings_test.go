package gateway

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
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
	if len(c.API.Keys) != 1 || c.API.Keys[0].Hash != hex.EncodeToString(sum[:]) || !c.API.RequireKey {
		t.Fatalf("%+v", c.API)
	}
	saved, _ := os.ReadFile(store.Path())
	if strings.Contains(string(saved), key) {
		t.Fatal("the key itself was saved")
	}
	if v := SettingsView(store.Get(), nil); !v.HasKey || strings.Contains(mustJSON(v), c.API.Keys[0].Hash) {
		t.Fatalf("%+v", v)
	}
	second, _ := UpdateSettings(store, map[string]any{"action": "generate"})
	if keys := store.Get().API.Keys; second == key || len(keys) != 1 || keys[0].Hash == hex.EncodeToString(sum[:]) {
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
	v := SettingsView(store.Get(), nil)
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
	// The panel decodes its JSON with UseNumber: numbers arrive as json.Number.
	if _, err := UpdateSettings(store, map[string]any{"action": "save", "queue_depth": json.Number("3")}); err != nil || store.Get().API.QueueDepth != 3 {
		t.Fatalf("json.Number: %v", err)
	}
	if _, err := UpdateSettings(store, map[string]any{"action": "save", "queue_depth": json.Number("2.5")}); err == nil {
		t.Fatal("a fraction was accepted")
	}
}

func TestSaveChangesTheNASProxy(t *testing.T) {
	store := settingsStore(t)
	if _, err := UpdateSettings(store, map[string]any{"action": "save", "public_origin": "LLM.fnx.red", "trusted_proxies": []any{" 192.168.1.10 "}}); err != nil {
		t.Fatal(err)
	}
	v := SettingsView(store.Get(), nil)
	if v.PublicOrigin != "https://llm.fnx.red" || len(v.TrustedProxies) != 1 || v.TrustedProxies[0] != "192.168.1.10" {
		t.Fatalf("%+v", v)
	}
	for _, bad := range []map[string]any{
		{"action": "save", "public_origin": "", "trusted_proxies": []any{"192.168.1.10"}},
		{"action": "save", "trusted_proxies": []any{"192.168.1.0/24"}},
		{"action": "save", "public_origin": "http://llm.fnx.red"},
		{"action": "save", "trusted_proxies": "192.168.1.10"},
	} {
		if _, err := UpdateSettings(store, bad); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if _, err := UpdateSettings(store, map[string]any{"action": "save", "public_origin": "", "trusted_proxies": []any{}}); err != nil {
		t.Fatal(err)
	}
	if c := store.Get(); c.Panel.PublicOrigin != "" || len(c.Panel.TrustedProxies) != 0 {
		t.Fatalf("NAS not removed: %+v", c.Panel)
	}
}

func TestNamedKeysAreAddedRevokedAndCounted(t *testing.T) {
	store := settingsStore(t)
	jan, err := UpdateSettings(store, map[string]any{"action": "add", "name": "Jan"})
	if err != nil {
		t.Fatal(err)
	}
	pi, _ := UpdateSettings(store, map[string]any{"action": "add", "name": "Pi"})
	if _, err := UpdateSettings(store, map[string]any{"action": "add", "name": "Jan"}); err == nil {
		t.Fatal("a duplicate name accepted")
	}
	c := store.Get()
	if len(c.API.Keys) != 2 || !c.API.RequireKey {
		t.Fatalf("%+v", c.API.Keys)
	}
	for _, key := range []string{jan, pi} {
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		if id, ok := MatchKey(r, c.API); !ok || id == "" {
			t.Fatalf("%s not accepted", key[:10])
		}
	}
	janID := c.API.Keys[0].ID
	v := SettingsView(c, map[string]KeyUsage{janID: {Requests: 3, Tokens: 120}})
	if v.Keys[0].Name != "Jan" || v.Keys[0].Requests != 3 || v.Keys[0].LastUsed == nil || v.Keys[1].LastUsed != nil {
		t.Fatalf("%+v", v.Keys)
	}
	if _, err := UpdateSettings(store, map[string]any{"action": "revoke", "id": janID}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("Authorization", "Bearer "+jan)
	if _, ok := MatchKey(r, store.Get().API); ok {
		t.Fatal("a revoked key still works")
	}
	if _, err := UpdateSettings(store, map[string]any{"action": "revoke", "id": store.Get().API.Keys[0].ID}); err == nil || !strings.Contains(err.Error(), "последний ключ") {
		t.Fatalf("the last required key revoked: %v", err)
	}
}

func TestRequestsAreCountedPerKey(t *testing.T) {
	f := newFixture(t, nil)
	f.request("/v1/chat/completions", chat("ornith-27b"), nil)
	sum := sha256.Sum256([]byte(apiKey))
	id := config.KeyID(hex.EncodeToString(sum[:]))
	if r := f.g.Snapshot().Requests[0]; r.KeyID != id {
		t.Fatalf("%+v", r)
	}
	if u := f.g.KeyUsage()[id]; u.Requests != 1 || u.Tokens != 14 || u.LastUsed.IsZero() {
		t.Fatalf("%+v", u)
	}
	// The count comes back from the journal after a restart.
	again := New(&Gateway{Config: f.cfg.Load, Services: f.services, Dir: f.g.Dir})
	if u := again.KeyUsage()[id]; u.Requests != 1 {
		t.Fatalf("after restart: %+v", u)
	}
}
