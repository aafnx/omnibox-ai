package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/attach"
	"github.com/aafnx/omnibox-ai/internal/config"
)

func TestPanelChatCanForwardMaxSizeAttachment(t *testing.T) {
	f := newFixture(t, nil)
	b := chat("ornith-27b")
	b["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(make([]byte, attach.MaxFile))}}}}}
	body := []byte(mustJSON(b))
	if len(body) <= bodyLimit {
		t.Fatal("fixture must exceed the external API limit")
	}
	resp := f.g.Chat(context.Background(), "attachment-test", body)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("25 MiB attachment refused: %d %s", resp.StatusCode, data)
	}
	// The larger limit belongs only to authenticated panel traffic, never external clients.
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	w := httptest.NewRecorder()
	f.g.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "32 MiB") {
		t.Fatalf("external limit changed: %d %s", w.Code, w.Body.String())
	}
}

// An automation's request is never written to the content log, even when the owner keeps the
// text of /v1 requests: a contract's personal data stays out of the journals. The panel chat's
// request in the same setting is, as before.
func TestAutomationRequestTextIsNotLogged(t *testing.T) {
	f := newFixture(t, func(c *config.Config) { c.Storage.RequestLog = "content" })
	b := chat("ornith-27b")
	b["messages"] = []any{map[string]any{"role": "user", "content": "Паспорт 4512 345678, Иванов Иван"}}
	resp := f.g.Automation(context.Background(), "panel-auto-a1", []byte(mustJSON(b)))
	io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d", resp.StatusCode)
	}
	r := f.g.Snapshot().Requests[0]
	if _, ok := f.g.FindContent(r.ID); ok || r.Client != "automation" {
		t.Fatalf("automation content stored, client %q", r.Client)
	}
	if data, err := os.ReadFile(f.g.path("content.jsonl")); err == nil && strings.Contains(string(data), "345678") {
		t.Fatal("passport number in the content log")
	}
	resp = f.g.Chat(context.Background(), "panel-chat-1", []byte(mustJSON(b)))
	io.ReadAll(resp.Body)
	resp.Body.Close()
	if _, ok := f.g.FindContent(f.g.Snapshot().Requests[0].ID); !ok {
		t.Fatal("the chat's content is kept as the owner chose")
	}
}
