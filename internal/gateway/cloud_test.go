package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

type fakeCloud struct {
	mu     sync.Mutex
	models []string
	got    []map[string]any
	spent  float64
}

func (c *fakeCloud) Models() []string { return c.models }

func (c *fakeCloud) Forward(ctx context.Context, model string, body map[string]any) (*http.Response, error) {
	c.mu.Lock()
	c.got = append(c.got, body)
	c.mu.Unlock()
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"cost\":0.0123}}\n\ndata: [DONE]\n\n"))}, nil
}

func (c *fakeCloud) Spent(model string, usd float64) {
	c.mu.Lock()
	c.spent += usd
	c.mu.Unlock()
}

func TestExposedCloudModelsBypassTheLocalEngine(t *testing.T) {
	f := newFixture(t, nil)
	cloud := &fakeCloud{models: []string{"openai/gpt-5"}}
	f.g.Cloud = cloud
	f.services.Set(config.ServiceName(config.PrismML), platform.Stopped)
	f.g.Invalidate()
	resp, list := f.request("/v1/models", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(mustJSON(list), "openai/gpt-5") {
		t.Fatalf("%d %v", resp.StatusCode, list)
	}
	body := chat("openai/gpt-5")
	body["stream"] = true
	resp, _ = f.request("/v1/chat/completions", body, nil)
	if resp.StatusCode != 200 || len(cloud.got) != 1 || f.engineCalls() != 0 {
		t.Fatalf("status %d, cloud %d, engine %d", resp.StatusCode, len(cloud.got), f.engineCalls())
	}
	if u, _ := cloud.got[0]["usage"].(map[string]any); u["include"] != true {
		t.Fatalf("usage not asked: %v", cloud.got[0])
	}
	if cloud.spent != 0.0123 {
		t.Fatalf("spent %v", cloud.spent)
	}
	// The key is still required, and another name is a local request.
	resp, _ = f.request("/v1/chat/completions", body, func(r *http.Request) { r.Header.Del("Authorization") })
	if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	resp, _ = f.request("/v1/chat/completions", chat("ornith-27b"), nil)
	if resp.StatusCode != 503 {
		t.Fatal(resp.StatusCode)
	}
}

type gptCloud struct{ fakeCloud }

func (c *gptCloud) Forward(ctx context.Context, model string, body map[string]any) (*http.Response, error) {
	return nil, statusErr{http.StatusUnauthorized}
}

type statusErr struct{ status int }

func (e statusErr) Error() string   { return "Войдите в ChatGPT" }
func (e statusErr) HTTPStatus() int { return e.status }

// ChatGPT models are listed as OpenAI's, and their refusals are explained in English.
func TestChatGPTModelsInTheAPI(t *testing.T) {
	f := newFixture(t, nil)
	f.g.Cloud = &gptCloud{fakeCloud{models: []string{"chatgpt/gpt-5.5"}}}
	f.g.Invalidate()
	resp, list := f.request("/v1/models", nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(mustJSON(list), `"id":"chatgpt/gpt-5.5","object":"model","owned_by":"openai"`) {
		t.Fatalf("%d %s", resp.StatusCode, mustJSON(list))
	}
	resp, out := f.request("/v1/chat/completions", chat("chatgpt/gpt-5.5"), nil)
	if resp.StatusCode != 401 || !strings.Contains(mustJSON(out), "ChatGPT request failed: the ChatGPT account is not signed in") || strings.Contains(mustJSON(out), "Войдите") {
		t.Fatalf("%d %s", resp.StatusCode, mustJSON(out))
	}
}

func TestPanelChatGoesThroughTheQueueWithoutKey(t *testing.T) {
	f := newFixture(t, nil)
	body, _ := json.Marshal(map[string]any{"model": "ornith-27b", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "привет"}}})
	resp := f.g.Chat(context.Background(), "panel-chat", body)
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(data), "Hel") || f.engineCalls() != 1 {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	resp = f.g.Chat(context.Background(), "panel-chat", []byte(`{"model":"other"}`))
	data, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 409 {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
}
