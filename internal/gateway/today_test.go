package gateway

import (
	"strings"
	"testing"
)

// The current date reaches a /v1 chat request only when the owner turned it on; the content log
// keeps what the client itself sent.
func TestDateIsAddedOnlyWhenChosen(t *testing.T) {
	f := newFixture(t, nil)
	body := map[string]any{"model": "ornith-27b", "messages": []any{map[string]any{"role": "user", "content": "Какое сегодня число?"}}}
	if resp, _ := f.request("/v1/chat/completions", body, nil); resp.StatusCode != 200 {
		t.Fatalf("%d", resp.StatusCode)
	}
	f.mu.Lock()
	first := f.received[len(f.received)-1]["messages"].([]any)[0].(map[string]any)
	f.mu.Unlock()
	if first["role"] != "user" {
		t.Fatalf("a date was added while off: %v", first)
	}
	c := f.cfg.Load().Clone()
	c.API.InjectDate = true
	f.cfg.Store(c)
	if resp, _ := f.request("/v1/chat/completions", body, nil); resp.StatusCode != 200 {
		t.Fatalf("%d", resp.StatusCode)
	}
	f.mu.Lock()
	msgs := f.received[len(f.received)-1]["messages"].([]any)
	f.mu.Unlock()
	if sys := msgs[0].(map[string]any); len(msgs) != 2 || sys["role"] != "system" || !strings.HasPrefix(sys["content"].(string), "Сейчас ") {
		t.Fatalf("no date: %v", msgs)
	}
	// Embeddings and plain completions are left alone.
	if resp, _ := f.request("/v1/completions", map[string]any{"model": "ornith-27b", "prompt": "x"}, nil); resp.StatusCode != 200 {
		t.Fatalf("%d", resp.StatusCode)
	}
	f.mu.Lock()
	_, touched := f.received[len(f.received)-1]["messages"]
	f.mu.Unlock()
	if touched {
		t.Fatal("a completion got messages")
	}
}

func TestInjectDateSetting(t *testing.T) {
	store := settingsStore(t)
	if _, err := UpdateSettings(store, map[string]any{"action": "save", "inject_date": true}); err != nil || !store.Get().API.InjectDate || !SettingsView(store.Get(), nil).InjectDate {
		t.Fatalf("%v %+v", err, store.Get().API)
	}
	if _, err := UpdateSettings(store, map[string]any{"action": "save", "inject_date": "yes"}); err == nil {
		t.Fatal("a string accepted")
	}
}
