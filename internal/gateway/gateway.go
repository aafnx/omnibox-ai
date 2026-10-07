// Package gateway is the OpenAI-compatible /v1 API. It forwards requests to the engine that runs
// on loopback, one generation at a time with a short FIFO queue (F2), counts tokens and writes
// the request journal. It never starts an engine or loads a model.
package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

const (
	bodyLimit       = 8 << 20
	maxInFlight     = 32
	requestTimeout  = 30 * time.Minute
	resolveCacheTTL = time.Second
)

// Target is the engine that serves /v1 right now: the one whose service runs.
type Target struct {
	Engine    string
	BaseURL   string
	Model     string // the real model name clients send, e.g. "qwen3:8b" or a GGUF alias
	Context   int
	Placement string
}

// Gateway serves /v1 and implements control.Gate.
type Gateway struct {
	Config   func() *config.Config
	Services platform.Services
	Dir      string // logs/requests
	Now      func() time.Time
	HTTP     *http.Client

	mu            sync.Mutex
	gen           uint64
	control       bool
	current       *Record
	cancel        context.CancelFunc
	cancels       map[string]context.CancelFunc
	queue         []*waiter
	recent        []*Record
	sessions      map[string]*Session
	modelSessions map[string]string
	journalErr    string
	cached        *resolved

	journal  sync.Mutex
	inFlight atomic.Int32
}

type resolved struct {
	gen    uint64
	at     time.Time
	target Target
	err    error
}

// New prepares a gateway and loads the recent request journal.
func New(g *Gateway) *Gateway {
	g.cancels, g.sessions, g.modelSessions = map[string]context.CancelFunc{}, map[string]*Session{}, map[string]string{}
	if g.HTTP == nil {
		g.HTTP = &http.Client{
			Transport:     &http.Transport{Proxy: nil, MaxConnsPerHost: 8, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, ResponseHeaderTimeout: requestTimeout},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	g.mu.Lock()
	g.load()
	g.mu.Unlock()
	return g
}

func (g *Gateway) path(name string) string { return filepath.Join(g.Dir, name) }

func (g *Gateway) contentEnabled() bool { return g.Config().Storage.RequestLog == "content" }

func jsonLine(v any) ([]byte, error) { return json.Marshal(v) }

func fail(w http.ResponseWriter, status int, message string) {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": "omnibox_error", "code": status}})
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if status == http.StatusTooManyRequests {
		h.Set("Retry-After", "5")
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// Authorized checks the client's API key against the stored SHA256 hash in constant time.
func Authorized(r *http.Request, api config.API) bool {
	if !api.RequireKey {
		return true
	}
	auth := r.Header.Get("Authorization")
	if len(auth) < 8 || len(auth) > 256 || !strings.EqualFold(auth[:7], "Bearer ") {
		return false
	}
	want, err := hex.DecodeString(api.KeyHash)
	if err != nil || len(want) != sha256.Size {
		return false
	}
	got := sha256.Sum256([]byte(auth[7:]))
	return subtle.ConstantTimeCompare(got[:], want) == 1
}

var routes = map[string]string{
	"/v1/models": http.MethodGet, "/v1/chat/completions": http.MethodPost, "/v1/completions": http.MethodPost,
	"/v1/embeddings": http.MethodPost, "/v1/responses": http.MethodPost,
}

// resolve finds the running engine; the answer is reused for a second within one generation,
// so bursts of requests do not query the service manager each time.
func (g *Gateway) resolve() (Target, uint64, error) {
	g.mu.Lock()
	gen, c := g.gen, g.cached
	g.mu.Unlock()
	if c != nil && c.gen == gen && g.now().Sub(c.at) < resolveCacheTTL {
		return c.target, gen, c.err
	}
	t, err := g.find()
	g.mu.Lock()
	if g.gen == gen {
		g.cached = &resolved{gen: gen, at: g.now(), target: t, err: err}
	}
	g.mu.Unlock()
	return t, gen, err
}

var errNoEngine = &admissionError{503, "No model is running. Start one in OmniBox AI."}

func (g *Gateway) find() (Target, error) {
	cfg := g.Config()
	for _, name := range config.EngineNames {
		e, ok := cfg.Engines[name]
		if !ok {
			continue
		}
		info, err := g.Services.Query(config.ServiceName(name))
		if err != nil || info.State != platform.Running {
			continue
		}
		launch := e.RunningLaunch()
		view := config.View(name, launch.Settings)
		t := Target{Engine: name, BaseURL: e.BaseURL(), Placement: launch.Settings["placement"]}
		t.Context, _ = strconv.Atoi(launch.Settings["context"])
		if name == config.PrismML {
			t.Model, _ = view["alias"].(string)
		} else {
			t.Model = e.Model
		}
		return t, nil
	}
	return Target{}, errNoEngine
}

// ServeHTTP handles /v1. The panel has already checked the Host header.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := g.Config()
	method, ok := routes[r.URL.Path]
	if !ok || method != r.Method || r.URL.RawQuery != "" {
		fail(w, http.StatusNotFound, "Unsupported OpenAI route.")
		return
	}
	// The key is checked before anything touches services or engines.
	if !Authorized(r, cfg.API) {
		fail(w, http.StatusUnauthorized, "Invalid or missing API key.")
		return
	}
	if g.inFlight.Add(1) > maxInFlight {
		g.inFlight.Add(-1)
		fail(w, http.StatusTooManyRequests, "Too many simultaneous requests.")
		return
	}
	defer g.inFlight.Add(-1)
	t, gen, err := g.resolve()
	if err != nil {
		var a *admissionError
		errors.As(err, &a)
		fail(w, a.status, a.message)
		return
	}
	target, err := url.Parse(strings.TrimRight(t.BaseURL, "/") + r.URL.Path)
	if err != nil || target.Scheme != "http" || target.User != nil || !loopback(target.Hostname()) {
		fail(w, http.StatusServiceUnavailable, "Invalid engine address.")
		return
	}
	if r.Method == http.MethodGet {
		g.models(w, r, target, t)
		return
	}
	g.generate(w, r, cfg, target, t, gen)
}

func loopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// models lists only the model loaded in the panel, under its real name.
func (g *Gateway) models(w http.ResponseWriter, r *http.Request, target *url.URL, t Target) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	resp, err := g.HTTP.Do(req)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "Engine unavailable. Start the model in OmniBox AI.")
		return
	}
	defer resp.Body.Close()
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, bodyLimit)).Decode(&list) != nil {
		fail(w, http.StatusBadGateway, "The engine returned an invalid model list.")
		return
	}
	data := []map[string]any{}
	for _, m := range list.Data {
		if id, _ := m["id"].(string); id != "" && sameModel(id, t.Model) {
			data = append(data, m)
		}
	}
	body, _ := json.Marshal(map[string]any{"object": "list", "data": data})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// sameModel accepts the selected name and, for Ollama, the name without ":latest".
func sameModel(name, selected string) bool {
	return selected != "" && (name == selected || (strings.HasSuffix(selected, ":latest") && name == strings.TrimSuffix(selected, ":latest")))
}

func intValue(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return i, err == nil
}

// prepare checks a generation request and adds the options OmniBox enforces.
func prepare(body []byte, route string, t Target) ([]byte, bool, *admissionError) {
	json := decodeObject(body)
	if json == nil {
		return nil, false, &admissionError{400, "A JSON object is required."}
	}
	model, ok := json["model"].(string)
	if !ok {
		return nil, false, &admissionError{400, "A model name is required."}
	}
	if !sameModel(model, t.Model) {
		return nil, false, &admissionError{409, "Load/select the model in OmniBox AI first. Only that model is available."}
	}
	for _, field := range []string{"n", "best_of"} {
		if v, ok := json[field]; ok {
			if n, ok := intValue(v); !ok || n != 1 {
				return nil, false, &admissionError{400, "Only one output sequence is allowed."}
			}
		}
	}
	changed := false
	if v, ok := json["options"]; ok {
		options, isObject := v.(map[string]any)
		if !isObject && t.Engine == config.Ollama {
			return nil, false, &admissionError{400, "options must be an object."}
		}
		if isObject {
			if v, ok := options["num_ctx"]; ok {
				if n, ok := intValue(v); !ok || int(n) != t.Context {
					return nil, false, &admissionError{400, "Change model context in OmniBox AI before sending requests."}
				}
			}
		}
	}
	if t.Engine == config.Ollama {
		options, _ := json["options"].(map[string]any)
		if options == nil {
			options = map[string]any{}
		}
		if _, ok := options["num_gpu"]; ok {
			return nil, false, &admissionError{400, "Change model placement in OmniBox AI."}
		}
		if t.Context > 0 {
			options["num_ctx"] = t.Context
		}
		if t.Placement == "cpu" {
			options["num_gpu"] = 0
		}
		json["options"] = options
		changed = true
	}
	streaming, _ := json["stream"].(bool)
	if streaming && (route == "/v1/chat/completions" || route == "/v1/completions") {
		options, _ := json["stream_options"].(map[string]any)
		if options == nil {
			options = map[string]any{}
		}
		options["include_usage"] = true
		json["stream_options"] = options
		if t.Engine == config.PrismML {
			json["timings_per_token"], json["return_progress"] = true, true
		}
		changed = true
	}
	if changed {
		var buf bytes.Buffer
		enc := jsonEncoder(&buf)
		if err := enc.Encode(json); err != nil {
			return nil, false, &admissionError{400, "A JSON object is required."}
		}
		body = bytes.TrimRight(buf.Bytes(), "\n")
	}
	return body, streaming, nil
}

func jsonEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc
}

func (g *Gateway) generate(w http.ResponseWriter, r *http.Request, cfg *config.Config, target *url.URL, t Target, gen uint64) {
	if r.ContentLength > bodyLimit {
		fail(w, http.StatusRequestEntityTooLarge, "Request exceeds 8 MiB.")
		return
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		fail(w, http.StatusUnsupportedMediaType, "JSON content required.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
	if err != nil {
		fail(w, http.StatusBadRequest, "The request body could not be read.")
		return
	}
	if len(body) > bodyLimit {
		fail(w, http.StatusRequestEntityTooLarge, "Request exceeds 8 MiB.")
		return
	}
	var c *capture
	// The client's own body, before OmniBox adds engine options; embeddings carry no readable text.
	if cfg.Storage.RequestLog == "content" && r.URL.Path != "/v1/embeddings" {
		c = newCapture(body)
	}
	forward, _, perr := prepare(body, r.URL.Path, t)
	if perr != nil {
		fail(w, perr.status, perr.message)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	timeout := time.Duration(cfg.API.QueueTimeout()) * time.Second
	record, err := g.admit(ctx, gen, t, r.URL.Path, r.Header.Get("X-Omnibox-Session"), cancel, cfg.API.QueueDepth, timeout)
	if err != nil {
		var a *admissionError
		if errors.As(err, &a) {
			fail(w, a.status, a.message)
		}
		// A client that left gets no answer.
		return
	}
	status, completed := http.StatusBadGateway, false
	defer func() { g.finish(record, status, completed, c) }()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(forward))
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.HTTP.Do(req)
	if err != nil {
		status = http.StatusServiceUnavailable
		message := "Engine unavailable. Start the model in OmniBox AI."
		if ctx.Err() != nil {
			status, message = http.StatusGatewayTimeout, "Engine request timed out or was stopped."
		}
		fail(w, status, message)
		return
	}
	defer resp.Body.Close()
	status = resp.StatusCode
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	sse := strings.HasPrefix(strings.ToLower(contentType), "text/event-stream")
	reader := &usageReader{g: g, record: record, sse: sse, capture: c}
	flusher := http.NewResponseController(w)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			g.generating(record)
			reader.feed(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = flusher.Flush()
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				status = http.StatusGatewayTimeout
			}
			return
		}
	}
	reader.finish()
	completed = !sse || reader.done
}

func (g *Gateway) generating(r *Record) {
	g.mu.Lock()
	r.State = "generating"
	g.mu.Unlock()
}

// usageReader watches the response for token counts and, when enabled, its text.
type usageReader struct {
	g       *Gateway
	record  *Record
	sse     bool
	capture *capture
	text    []byte
	event   []byte
	over    bool
	done    bool
}

func (u *usageReader) feed(chunk []byte) {
	if !u.sse {
		if !u.over && len(u.text)+len(chunk) <= observedLimit {
			u.text = append(u.text, chunk...)
		} else {
			u.over, u.text = true, nil
		}
		return
	}
	for _, b := range chunk {
		if b != '\n' {
			if len(u.text) < 1<<20 {
				u.text = append(u.text, b)
			} else {
				u.over = true
			}
			continue
		}
		line := bytes.TrimRight(u.text, "\r")
		u.text = u.text[:0]
		switch {
		case len(line) == 0:
			if !u.over && len(u.event) > 0 {
				u.observeEvent(u.event)
			}
			u.event, u.over = u.event[:0], false
		case bytes.HasPrefix(line, []byte("data:")) && !u.over:
			if len(u.event) > 0 {
				u.event = append(u.event, '\n')
			}
			u.event = append(u.event, bytes.TrimLeft(line[5:], " \t")...)
			if len(u.event) > observedLimit {
				u.event, u.over = u.event[:0], true
			}
		}
	}
}

func (u *usageReader) observeEvent(data []byte) {
	if string(data) == "[DONE]" {
		u.done = true
		return
	}
	u.g.mu.Lock()
	u.record.observe(data)
	u.g.mu.Unlock()
	if u.capture != nil {
		u.capture.event(data)
	}
	if bytes.Contains(data, []byte(`"response.completed"`)) {
		u.done = true
	}
}

func (u *usageReader) finish() {
	if u.sse || u.over {
		return
	}
	u.g.mu.Lock()
	u.record.observe(u.text)
	u.g.mu.Unlock()
	if u.capture != nil {
		u.capture.setBody(u.text)
	}
}
