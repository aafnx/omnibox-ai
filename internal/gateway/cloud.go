package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/aafnx/omnibox-ai/internal/today"
)

// CloudRoute serves the OpenRouter and ChatGPT models the owner chose to show to /v1 clients.
// Their requests do not wait in the local queue: they use no memory of this PC.
type CloudRoute interface {
	// Models are the exposed model ids; empty while the option is off or no key is saved.
	Models() []string
	// Forward sends a chat request for one of them, with unsupported parameters removed.
	Forward(ctx context.Context, model string, body map[string]any) (*http.Response, error)
	// Spent records what OpenRouter reported a request cost.
	Spent(model string, usd float64)
}

// cloudModel tells whether a /v1 request names an exposed cloud model, and returns its body.
func (g *Gateway) cloudModel(r *http.Request) (string, []byte, bool) {
	if g.Cloud == nil || r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		return "", nil, false
	}
	models := g.Cloud.Models()
	if len(models) == 0 || r.ContentLength > bodyLimit {
		return "", nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
	if err != nil {
		body = nil
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var head struct {
		Model string `json:"model"`
	}
	if len(body) > bodyLimit || json.Unmarshal(body, &head) != nil || !slices.Contains(models, head.Model) {
		return "", nil, false
	}
	return head.Model, body, true
}

func (g *Gateway) cloudGenerate(w http.ResponseWriter, r *http.Request, model string, body []byte) {
	req := decodeObject(body)
	if req == nil {
		fail(w, http.StatusBadRequest, "A JSON object is required.")
		return
	}
	// OpenRouter reports each request's cost when asked for usage.
	usage, _ := req["usage"].(map[string]any)
	if usage == nil {
		usage = map[string]any{}
	}
	usage["include"] = true
	req["usage"] = usage
	if now := g.dateFor(g.Config()); now != nil {
		if messages, ok := req["messages"].([]any); ok {
			req["messages"] = today.Inject(messages, *now)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	resp, err := g.Cloud.Forward(ctx, model, req)
	if err != nil {
		status := http.StatusBadGateway
		var se interface{ HTTPStatus() int }
		if errors.As(err, &se) {
			status = se.HTTPStatus()
		}
		if strings.HasPrefix(model, ChatGPTPrefix) {
			fail(w, status, "ChatGPT request failed: "+englishChatGPTError(status, err))
			return
		}
		fail(w, status, "OpenRouter request failed: "+englishCloudError(status))
		return
	}
	defer resp.Body.Close()
	h := w.Header()
	h.Set("Content-Type", resp.Header.Get("Content-Type"))
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)
	flusher := http.NewResponseController(w)
	var seen bytes.Buffer
	reader := bufio.NewReaderSize(resp.Body, 32<<10)
	cost := 0.0
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			if c, ok := costOf(line); ok {
				cost = c
			}
			if seen.Len() < 1<<20 {
				seen.Write(line)
			}
			if _, werr := w.Write(line); werr != nil {
				break
			}
			_ = flusher.Flush()
		}
		if err != nil {
			break
		}
	}
	if cost == 0 {
		cost, _ = costOf(seen.Bytes())
	}
	if cost > 0 {
		g.Cloud.Spent(model, cost)
	}
}

// costOf reads usage.cost from a JSON body or one SSE data line.
func costOf(line []byte) (float64, bool) {
	s := strings.TrimSpace(string(line))
	s = strings.TrimSpace(strings.TrimPrefix(s, "data:"))
	if !strings.Contains(s, `"cost"`) {
		return 0, false
	}
	var v struct {
		Usage *struct {
			Cost float64 `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(s), &v) != nil || v.Usage == nil {
		return 0, false
	}
	return v.Usage.Cost, true
}

func englishCloudError(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "the OpenRouter key was not accepted."
	case http.StatusPaymentRequired:
		return "the OpenRouter balance is empty."
	case http.StatusTooManyRequests:
		return "rate limited by OpenRouter; retry later."
	}
	return "upstream error."
}

// ChatGPTPrefix names the ChatGPT subscription's models among the cloud ones.
const ChatGPTPrefix = "chatgpt/"

func englishChatGPTError(status int, err error) string {
	switch status {
	case http.StatusBadRequest:
		return err.Error()
	case http.StatusUnauthorized:
		return "the ChatGPT account is not signed in on the OmniBox panel."
	case http.StatusNotFound:
		return "the model is not available in the ChatGPT subscription."
	case http.StatusServiceUnavailable:
		return "ChatGPT is not available on this server right now."
	}
	return "upstream error."
}

// cloudList adds the exposed cloud models to a /v1/models answer.
func (g *Gateway) cloudList() []map[string]any {
	if g.Cloud == nil {
		return nil
	}
	var out []map[string]any
	for _, id := range g.Cloud.Models() {
		owner := "openrouter"
		if strings.HasPrefix(id, ChatGPTPrefix) {
			owner = "openai"
		}
		out = append(out, map[string]any{"id": id, "object": "model", "owned_by": owner})
	}
	return out
}
