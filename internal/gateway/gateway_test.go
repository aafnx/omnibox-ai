package gateway

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

const apiKey = "obai-test-key"

// countingServices counts service queries, to prove what does not reach the service manager.
type countingServices struct {
	*platform.FakeServices
	queries atomic.Int32
}

func (c *countingServices) Query(name string) (platform.ServiceInfo, error) {
	c.queries.Add(1)
	return c.FakeServices.Query(name)
}

type fixture struct {
	t        *testing.T
	cfg      atomic.Pointer[config.Config]
	services *countingServices
	g        *Gateway
	srv      *httptest.Server
	engine   *httptest.Server
	mu       sync.Mutex
	received []map[string]any
	headers  []http.Header
	hold     chan struct{} // while set, the engine answers only after it is closed
	release  sync.Once
}

// block makes the engine hold every generation until unblock.
func (f *fixture) block() {
	f.mu.Lock()
	f.hold = make(chan struct{})
	f.mu.Unlock()
	f.t.Cleanup(f.unblock)
}

func (f *fixture) unblock() {
	f.mu.Lock()
	hold := f.hold
	f.mu.Unlock()
	if hold != nil {
		f.release.Do(func() { close(hold) })
	}
}

func newFixture(t *testing.T, change func(c *config.Config)) *fixture {
	t.Helper()
	f := &fixture{t: t, services: &countingServices{FakeServices: platform.NewFakeServices()}}
	f.engine = httptest.NewServer(http.HandlerFunc(f.serveEngine))
	t.Cleanup(f.engine.Close)
	port, _ := strconv.Atoi(f.engine.URL[strings.LastIndex(f.engine.URL, ":")+1:])
	cfg := config.Default()
	cfg.Panel.Hosts = []string{"localhost"}
	b64 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg.Panel.Password = config.Password{Salt: b64, Hash: b64, Iterations: 210000}
	sum := sha256.Sum256([]byte(apiKey))
	cfg.API = config.API{Enabled: true, RequireKey: true, KeyHash: hex.EncodeToString(sum[:]), Engine: config.PrismML, QueueDepth: 2}
	cfg.Engines[config.PrismML] = &config.Engine{Port: port, Launch: config.Launch{Settings: map[string]string{"model": "/models/ornith-27b-Q4_K_M.gguf", "alias": "ornith-27b", "context": "32768"}}}
	cfg.Engines[config.Ollama] = &config.Engine{Port: port + 1, Model: "qwen3:latest", Launch: config.Launch{Settings: map[string]string{"context": "8192", "placement": "cpu"}}}
	if change != nil {
		change(cfg)
	}
	f.cfg.Store(cfg)
	f.services.Set(config.ServiceName(config.PrismML), platform.Running)
	f.services.Set(config.ServiceName(config.Ollama), platform.Stopped)
	f.g = New(&Gateway{Config: f.cfg.Load, Services: f.services, Dir: filepath.Join(t.TempDir(), "requests")})
	f.srv = httptest.NewServer(f.g)
	t.Cleanup(f.srv.Close)
	return f
}

// useOllama points Ollama at the fake engine and makes it the running one.
func (f *fixture) useOllama() {
	c := f.cfg.Load().Clone()
	c.Engines[config.Ollama].Port, c.Engines[config.PrismML].Port = c.Engines[config.PrismML].Port, c.Engines[config.PrismML].Port+1
	f.cfg.Store(c)
	f.services.Set(config.ServiceName(config.PrismML), platform.Stopped)
	f.services.Set(config.ServiceName(config.Ollama), platform.Running)
	f.g.Invalidate()
}

func (f *fixture) serveEngine(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.received = append(f.received, body)
	f.headers = append(f.headers, r.Header.Clone())
	hold := f.hold
	f.mu.Unlock()
	if r.URL.Path == "/v1/models" {
		io.WriteString(w, `{"object":"list","data":[{"id":"ornith-27b","object":"model"},{"id":"other","object":"model"},{"id":"qwen3:latest","object":"model"}]}`)
		return
	}
	if hold != nil {
		select {
		case <-hold:
		case <-r.Context().Done():
			return
		}
	}
	if stream, _ := body["stream"].(bool); stream {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []string{`{"choices":[{"delta":{"content":"Hel"}}]}`, `{"choices":[{"delta":{"content":"lo"}}]}`, `{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`, `[DONE]`} {
			io.WriteString(w, "data: "+e+"\n\n")
			w.(http.Flusher).Flush()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`)
}

func (f *fixture) request(path string, body any, mutate func(r *http.Request)) (*http.Response, map[string]any) {
	f.t.Helper()
	method := http.MethodGet
	var reader io.Reader
	if body != nil {
		method = http.MethodPost
		data, _ := json.Marshal(body)
		reader = strings.NewReader(string(data))
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, reader)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if mutate != nil {
		mutate(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return resp, out
}

func (f *fixture) engineCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

func chat(model string) map[string]any {
	return map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": "привет"}}}
}

func TestKeyIsCheckedBeforeServicesAndEngine(t *testing.T) {
	f := newFixture(t, nil)
	for _, auth := range []string{"", "Bearer wrong", "Basic " + apiKey, "Bearer " + strings.Repeat("a", 300)} {
		resp, _ := f.request("/v1/chat/completions", chat("ornith-27b"), func(r *http.Request) { r.Header.Set("Authorization", auth) })
		if resp.StatusCode != 401 {
			t.Fatalf("%q: %d", auth, resp.StatusCode)
		}
	}
	if f.services.queries.Load() != 0 || f.engineCalls() != 0 {
		t.Fatalf("an unauthorized request reached services (%d) or the engine (%d)", f.services.queries.Load(), f.engineCalls())
	}
	if resp, _ := f.request("/v1/models?url=http://other", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("query string: %d", resp.StatusCode)
	}
	if resp, _ := f.request("/v1/files", nil, nil); resp.StatusCode != 404 {
		t.Fatalf("unknown route: %d", resp.StatusCode)
	}
}

func TestRequestsGoToTheRunningEngineWithoutClientKey(t *testing.T) {
	f := newFixture(t, nil)
	resp, out := f.request("/v1/models", nil, nil)
	data, _ := out["data"].([]any)
	if resp.StatusCode != 200 || len(data) != 1 || data[0].(map[string]any)["id"] != "ornith-27b" {
		t.Fatalf("models: %d %v", resp.StatusCode, out)
	}
	body := chat("ornith-27b")
	body["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}}
	if resp, out := f.request("/v1/chat/completions", body, nil); resp.StatusCode != 200 || out["usage"] == nil {
		t.Fatalf("chat: %d %v", resp.StatusCode, out)
	}
	f.mu.Lock()
	forwarded, header := f.received[len(f.received)-1], f.headers[len(f.headers)-1]
	f.mu.Unlock()
	if forwarded["tools"] == nil || forwarded["stream_options"] != nil || header.Get("Authorization") != "" {
		t.Fatalf("forwarded %v, authorization %q", forwarded, header.Get("Authorization"))
	}
	a := f.g.Snapshot()
	r := a.Requests[0]
	if r.State != "completed" || *r.InputTokens != 11 || *r.OutputTokens != 3 || *r.TotalTokens != 14 || *r.ContextRemaining != 32768-14 || r.Model != "ornith-27b" {
		t.Fatalf("%+v", r)
	}
	f.services.Set(config.ServiceName(config.PrismML), platform.Stopped)
	f.g.Invalidate()
	if resp, out := f.request("/v1/chat/completions", chat("ornith-27b"), nil); resp.StatusCode != 503 || !strings.Contains(out["error"].(map[string]any)["message"].(string), "No model is running") {
		t.Fatalf("stopped: %d %v", resp.StatusCode, out)
	}
}

func TestRequestsAreCheckedBeforeTheEngine(t *testing.T) {
	f := newFixture(t, nil)
	cases := []struct {
		body   map[string]any
		status int
	}{
		{map[string]any{"model": "another-model"}, 409},
		{map[string]any{"model": "ornith-27b", "n": 2}, 400},
		{map[string]any{"model": "ornith-27b", "n": []any{}}, 400},
		{map[string]any{"model": "ornith-27b", "options": map[string]any{"num_ctx": 999999}}, 400},
		{map[string]any{"messages": []any{}}, 400},
	}
	for _, c := range cases {
		if resp, _ := f.request("/v1/chat/completions", c.body, nil); resp.StatusCode != c.status {
			t.Fatalf("%v: %d", c.body, resp.StatusCode)
		}
	}
	if resp, _ := f.request("/v1/chat/completions", chat("ornith-27b"), func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }); resp.StatusCode != 415 {
		t.Fatalf("content type: %d", resp.StatusCode)
	}
	big := chat("ornith-27b")
	big["messages"] = strings.Repeat("x", bodyLimit)
	if resp, _ := f.request("/v1/chat/completions", big, nil); resp.StatusCode != 413 {
		t.Fatalf("large body: %d", resp.StatusCode)
	}
	if f.engineCalls() != 0 {
		t.Fatalf("refused requests reached the engine: %d", f.engineCalls())
	}
}

func TestOllamaGetsContextAndPlacementFromThePanel(t *testing.T) {
	f := newFixture(t, nil)
	f.useOllama()
	if resp, _ := f.request("/v1/chat/completions", chat("qwen3"), nil); resp.StatusCode != 200 {
		t.Fatalf("name without :latest: %d", resp.StatusCode)
	}
	f.mu.Lock()
	options := f.received[len(f.received)-1]["options"].(map[string]any)
	f.mu.Unlock()
	if options["num_ctx"] != float64(8192) || options["num_gpu"] != float64(0) {
		t.Fatalf("options %v", options)
	}
	if resp, _ := f.request("/v1/chat/completions", map[string]any{"model": "qwen3:latest", "options": map[string]any{"num_gpu": 99}}, nil); resp.StatusCode != 400 {
		t.Fatalf("client placement: %d", resp.StatusCode)
	}
	if resp, _ := f.request("/v1/chat/completions", map[string]any{"model": "qwen3:latest", "options": "fast"}, nil); resp.StatusCode != 400 {
		t.Fatalf("options string: %d", resp.StatusCode)
	}
}

func TestStreamingAddsUsageAndIsNotBuffered(t *testing.T) {
	f := newFixture(t, func(c *config.Config) { c.Storage.RequestLog = "content" })
	body := chat("ornith-27b")
	body["stream"] = true
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+"/v1/chat/completions", strings.NewReader(mustJSON(body)))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("X-Accel-Buffering") != "no" || !strings.Contains(string(data), "[DONE]") {
		t.Fatalf("headers %v body %s", resp.Header, data)
	}
	f.mu.Lock()
	forwarded := f.received[len(f.received)-1]
	f.mu.Unlock()
	if forwarded["stream_options"].(map[string]any)["include_usage"] != true || forwarded["timings_per_token"] != true {
		t.Fatalf("forwarded %v", forwarded)
	}
	r := f.g.Snapshot().Requests[0]
	if r.State != "completed" || *r.TotalTokens != 9 {
		t.Fatalf("%+v", r)
	}
	content, ok := f.g.FindContent(r.ID)
	if !ok || !strings.Contains(string(content), `"response_text":"Hello"`) || !strings.Contains(string(content), "привет") {
		t.Fatalf("content %s", content)
	}
	journal, _ := os.ReadFile(f.g.path("requests.jsonl"))
	if strings.Contains(string(journal), "привет") || strings.Contains(string(journal), "Hello") {
		t.Fatal("the metadata journal holds request text")
	}
}

func TestContentIsNotStoredByDefault(t *testing.T) {
	f := newFixture(t, nil)
	f.request("/v1/chat/completions", chat("ornith-27b"), nil)
	id := f.g.Snapshot().Requests[0].ID
	if _, ok := f.g.FindContent(id); ok {
		t.Fatal("content stored without the owner's choice")
	}
	if _, err := os.Stat(f.g.path("content.jsonl")); err == nil {
		t.Fatal("content.jsonl created")
	}
	if _, ok := f.g.FindContent("../../config.json"); ok {
		t.Fatal("arbitrary id accepted")
	}
	g2 := New(&Gateway{Config: f.cfg.Load, Services: f.services, Dir: f.g.Dir})
	if a := g2.Snapshot(); len(a.Requests) != 1 || a.Requests[0].ID != id || len(a.Sessions) != 1 {
		t.Fatalf("history after restart: %+v", a)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// send starts a request in the background and returns its status when it completes.
func (f *fixture) send(ctx context.Context, body map[string]any) chan int {
	done := make(chan int, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, f.srv.URL+"/v1/chat/completions", strings.NewReader(mustJSON(body)))
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- 0
			return
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	return done
}

func (f *fixture) waitQueued(n int) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.g.Snapshot().Queued != n {
		if time.Now().After(deadline) {
			f.t.Fatalf("queue length %d, want %d", f.g.Snapshot().Queued, n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *fixture) waitActive() {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f.g.IsActive(config.PrismML) || f.engineCalls() == 0 {
		if time.Now().After(deadline) {
			f.t.Fatal("no active generation")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestQueueRunsRequestsInOrderAndRejectsOverflow(t *testing.T) {
	f := newFixture(t, nil)
	f.block()
	first := f.send(context.Background(), chat("ornith-27b"))
	f.waitActive()
	second := f.send(context.Background(), map[string]any{"model": "ornith-27b", "messages": []any{}, "seq": 2})
	f.waitQueued(1)
	third := f.send(context.Background(), map[string]any{"model": "ornith-27b", "messages": []any{}, "seq": 3})
	f.waitQueued(2)
	if resp, _ := f.request("/v1/chat/completions", chat("ornith-27b"), nil); resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("overflow: %d", resp.StatusCode)
	}
	if f.g.BeginControl(false) {
		t.Fatal("a control operation started during a generation")
	}
	f.unblock()
	for i, ch := range []chan int{first, second, third} {
		if s := <-ch; s != 200 {
			t.Fatalf("request %d: %d", i+1, s)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.received) != 3 || f.received[1]["seq"] != float64(2) || f.received[2]["seq"] != float64(3) {
		t.Fatalf("order %v", f.received)
	}
	if r := f.g.Snapshot().Requests[0]; r.QueuedSeconds <= 0 {
		t.Fatalf("queued time not recorded: %+v", r)
	}
}

func TestStopFailsQueuedRequestsAndCancelsTheRunningOne(t *testing.T) {
	f := newFixture(t, nil)
	f.block()
	first := f.send(context.Background(), chat("ornith-27b"))
	f.waitActive()
	second := f.send(context.Background(), map[string]any{"model": "ornith-27b", "seq": 2})
	f.waitQueued(1)
	if !f.g.BeginControl(true) {
		t.Fatal("stop refused")
	}
	if s := <-second; s != 503 {
		t.Fatalf("queued request after stop: %d", s)
	}
	if s := <-first; s == 200 {
		t.Fatalf("running request after stop: %d", s)
	}
	if resp, _ := f.request("/v1/chat/completions", chat("ornith-27b"), nil); resp.StatusCode != 503 {
		t.Fatalf("request during control: %d", resp.StatusCode)
	}
	f.g.EndControl()
	if f.engineCalls() != 1 {
		t.Fatalf("a queued request reached the engine after stop: %d", f.engineCalls())
	}
	if a := f.g.Snapshot(); a.Active != nil || a.Queued != 0 || a.Requests[0].State != "failed" {
		t.Fatalf("%+v", a)
	}
}

func TestQueueTimeoutDisconnectAndZeroDepth(t *testing.T) {
	f := newFixture(t, func(c *config.Config) { c.API.QueueTimeoutSeconds = 1 })
	f.block()
	first := f.send(context.Background(), chat("ornith-27b"))
	f.waitActive()
	started := time.Now()
	if s := <-f.send(context.Background(), chat("ornith-27b")); s != 503 || time.Since(started) < 900*time.Millisecond {
		t.Fatalf("queue timeout: %d after %v", s, time.Since(started))
	}
	ctx, cancel := context.WithCancel(context.Background())
	left := f.send(ctx, chat("ornith-27b"))
	f.waitQueued(1)
	cancel()
	<-left
	f.waitQueued(0)
	c := f.cfg.Load().Clone()
	c.API.QueueDepth = 0
	f.cfg.Store(c)
	if resp, _ := f.request("/v1/chat/completions", chat("ornith-27b"), nil); resp.StatusCode != 409 {
		t.Fatalf("depth 0: %d", resp.StatusCode)
	}
	_ = first
}

func TestSessionsGroupRequests(t *testing.T) {
	f := newFixture(t, nil)
	header := func(r *http.Request) { r.Header.Set("X-Omnibox-Session", "chat-1") }
	for range 2 {
		f.request("/v1/chat/completions", chat("ornith-27b"), header)
	}
	f.request("/v1/chat/completions", chat("ornith-27b"), nil)
	a := f.g.Snapshot()
	if len(a.Sessions) != 2 || a.Requests[1].SessionID != a.Requests[2].SessionID || a.Requests[0].SessionID == a.Requests[1].SessionID {
		t.Fatalf("%+v", a.Sessions)
	}
	var total int
	for _, s := range a.Sessions {
		total += s.Requests
	}
	if total != 3 {
		t.Fatalf("session counts %d", total)
	}
}

func TestSSEUsageReaderHandlesSplitEvents(t *testing.T) {
	g := New(&Gateway{Config: func() *config.Config { return config.Default() }, Dir: t.TempDir()})
	r := &Record{Route: "/v1/responses", ContextLimit: 100}
	u := &usageReader{g: g, record: r, sse: true}
	stream := "event: response.completed\r\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":4,\"total_tokens\":9}}}\r\n\r\n"
	scanner := bufio.NewScanner(strings.NewReader(stream))
	scanner.Split(bufio.ScanBytes)
	for scanner.Scan() {
		u.feed(scanner.Bytes())
	}
	if !u.done || r.TotalTokens == nil || *r.TotalTokens != 9 || *r.ContextRemaining != 91 {
		t.Fatalf("%+v done %v", r, u.done)
	}
}
