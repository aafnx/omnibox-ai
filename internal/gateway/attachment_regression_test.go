package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/attach"
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
