package gateway

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Journal limits: metadata and content files rotate with two older copies each.
const (
	journalLimit  = 4 << 20
	contentLimit  = 16 << 20
	recentLimit   = 500
	sessionLimit  = 100
	textLimit     = 1 << 20
	observedLimit = 4 << 20
)

// Record is one /v1 request. It is the only payload of the metadata journal: request text goes
// to content.jsonl, and only when the owner enables it.
type Record struct {
	ID               string     `json:"id"`
	SessionID        string     `json:"session_id"`
	Engine           string     `json:"backend"`
	Model            string     `json:"model"`
	Route            string     `json:"route"`
	Started          time.Time  `json:"started"`
	Ended            *time.Time `json:"ended"`
	State            string     `json:"state"` // queued, preparing, generating, completed, failed
	Status           int        `json:"status"`
	Duration         float64    `json:"duration_seconds"`
	QueuedSeconds    float64    `json:"queued_seconds,omitempty"`
	InputTokens      *int64     `json:"input_tokens"`
	OutputTokens     *int64     `json:"output_tokens"`
	TotalTokens      *int64     `json:"total_tokens"`
	ContextLimit     int        `json:"context_limit"`
	ContextUsed      *int64     `json:"context_used"`
	ContextRemaining *int64     `json:"context_remaining"`
	PromptProcessed  *int64     `json:"prompt_processed"`
	PromptTotal      *int64     `json:"prompt_total"`
	UsageSource      string     `json:"usage_source"`
	// KeyID is the client's API key (its short id), empty without a key.
	KeyID string `json:"key_id,omitempty"`
	// Client names the panel's own requests: "chat" (its chat) or "test" (the model test).
	Client string `json:"client,omitempty"`
}

// KeyUsage is what one API key used: shown next to the key so an unused one can be revoked.
type KeyUsage struct {
	Requests int       `json:"requests"`
	Tokens   int64     `json:"tokens"`
	LastUsed time.Time `json:"last_used"`
}

// Session sums the requests of one client conversation with one model.
type Session struct {
	ID           string    `json:"id"`
	Engine       string    `json:"backend"`
	Model        string    `json:"model"`
	Started      time.Time `json:"started"`
	LastRequest  time.Time `json:"last_request"`
	Requests     int       `json:"requests"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
	TotalTokens  int64     `json:"total_tokens"`
	MissingUsage int       `json:"missing_usage"`
}

func ptr(n int64) *int64 { return &n }

// usageNumber reads a non-negative integer field.
func usageNumber(obj map[string]any, key string) *int64 {
	switch v := obj[key].(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil && n >= 0 {
			return &n
		}
	case float64:
		if v >= 0 && v == float64(int64(v)) {
			return ptr(int64(v))
		}
	}
	return nil
}

func decodeObject(data []byte) map[string]any {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v map[string]any
	if dec.Decode(&v) != nil {
		return nil
	}
	return v
}

// observe takes token counts from a response body or one SSE event. The caller holds the lock.
func (r *Record) observe(data []byte) {
	if len(data) > observedLimit {
		return
	}
	root := decodeObject(data)
	if root == nil {
		return
	}
	if response, ok := root["response"].(map[string]any); ok {
		root = response
	}
	if usage, ok := root["usage"].(map[string]any); ok {
		input := usageNumber(usage, "prompt_tokens")
		if input == nil {
			input = usageNumber(usage, "input_tokens")
		}
		output := usageNumber(usage, "completion_tokens")
		if output == nil {
			output = usageNumber(usage, "output_tokens")
		}
		if input != nil {
			r.InputTokens = input
		}
		if output != nil {
			r.OutputTokens = output
		}
		// Embedding APIs report input and total but have no output tokens.
		if r.Route == "/v1/embeddings" && input != nil {
			r.OutputTokens = ptr(0)
		}
		r.TotalTokens = usageNumber(usage, "total_tokens")
		if r.TotalTokens == nil && r.InputTokens != nil && r.OutputTokens != nil {
			r.TotalTokens = ptr(*r.InputTokens + *r.OutputTokens)
		}
		r.UsageSource = "usage"
	}
	if progress, ok := root["prompt_progress"].(map[string]any); ok {
		r.PromptTotal, r.PromptProcessed = usageNumber(progress, "total"), usageNumber(progress, "processed")
		if r.PromptTotal != nil {
			r.InputTokens = r.PromptTotal
		}
		r.UsageSource = "engine_progress"
	}
	if timings, ok := root["timings"].(map[string]any); ok {
		if decoded := usageNumber(timings, "predicted_n"); decoded != nil {
			r.OutputTokens = decoded
		}
	}
	r.updateContext()
}

func (r *Record) updateContext() {
	if r.InputTokens == nil || r.OutputTokens == nil {
		return
	}
	used := *r.InputTokens + *r.OutputTokens
	r.ContextUsed, r.ContextRemaining = ptr(used), ptr(max(0, int64(r.ContextLimit)-used))
	if r.UsageSource != "usage" {
		r.TotalTokens = ptr(used)
	}
}

// capture keeps request and response text for the optional content journal.
type capture struct {
	request   string
	content   strings.Builder
	reasoning strings.Builder
	body      *string
	truncated bool
}

func newCapture(request []byte) *capture {
	c := &capture{}
	if len(request) <= textLimit {
		c.request = string(request)
	} else {
		c.truncated = true
	}
	return c
}

func (c *capture) add(target *strings.Builder, text string) {
	if text == "" {
		return
	}
	if target.Len()+len(text) > textLimit {
		c.truncated = true
		return
	}
	target.WriteString(text)
}

func text(obj map[string]any, key string) string {
	s, _ := obj[key].(string)
	return s
}

// event collects chat and completion deltas and Responses API text deltas from one SSE event.
func (c *capture) event(data []byte) {
	root := decodeObject(data)
	if root == nil {
		return
	}
	if kind, ok := root["type"].(string); ok {
		switch {
		case kind == "response.output_text.delta":
			c.add(&c.content, text(root, "delta"))
		case strings.HasPrefix(kind, "response.reasoning") && strings.HasSuffix(kind, ".delta"):
			c.add(&c.reasoning, text(root, "delta"))
		}
		return
	}
	choices, _ := root["choices"].([]any)
	for _, item := range choices {
		choice, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if delta, ok := choice["delta"].(map[string]any); ok {
			c.add(&c.content, text(delta, "content"))
			reasoning := text(delta, "reasoning_content")
			if reasoning == "" {
				reasoning = text(delta, "reasoning")
			}
			c.add(&c.reasoning, reasoning)
		}
		c.add(&c.content, text(choice, "text"))
	}
}

func (c *capture) setBody(data []byte) {
	if len(data) <= textLimit {
		s := string(data)
		c.body = &s
	} else {
		c.truncated = true
	}
}

func rawOrText(s string) json.RawMessage {
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	b, _ := json.Marshal(s)
	return b
}

func (c *capture) line(r *Record) ([]byte, error) {
	type entry struct {
		ID        string          `json:"id"`
		SessionID string          `json:"session_id"`
		Engine    string          `json:"backend"`
		Model     string          `json:"model"`
		Route     string          `json:"route"`
		Started   time.Time       `json:"started"`
		Status    int             `json:"status"`
		Truncated bool            `json:"truncated"`
		Request   json.RawMessage `json:"request"`
		Response  json.RawMessage `json:"response,omitempty"`
		Text      *string         `json:"response_text,omitempty"`
		Reasoning string          `json:"reasoning_text,omitempty"`
	}
	e := entry{ID: r.ID, SessionID: r.SessionID, Engine: r.Engine, Model: r.Model, Route: r.Route, Started: r.Started, Status: r.Status, Truncated: c.truncated, Request: json.RawMessage("null")}
	if c.request != "" {
		e.Request = rawOrText(dropImages(c.request))
	}
	if c.body != nil {
		e.Response = rawOrText(*c.body)
	} else {
		t := c.content.String()
		e.Text, e.Reasoning = &t, c.reasoning.String()
	}
	return json.Marshal(e)
}

var inlineImage = regexp.MustCompile(`data:(image/[a-zA-Z0-9.+-]+);base64,[A-Za-z0-9+/=]+`)

// dropImages replaces images sent inline (base64) with their size: the journal keeps the text of
// a conversation, and a few photos would fill its file.
func dropImages(s string) string {
	return inlineImage.ReplaceAllStringFunc(s, func(m string) string {
		kind := inlineImage.FindStringSubmatch(m)[1]
		return fmt.Sprintf("data:%s;base64,[изображение %d КБ не сохранено]", kind, len(m)*3/4/1024)
	})
}

// rotate keeps the file and two older copies once it reaches limit.
func rotate(path string, limit int64) {
	if info, err := os.Stat(path); err == nil && info.Size() >= limit {
		_ = os.Remove(path + ".2")
		_ = os.Rename(path+".1", path+".2")
		_ = os.Rename(path, path+".1")
	}
}

func appendLine(path string, line []byte, limit int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	rotate(path, limit)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// load reads the recent journal so the panel keeps its history across restarts.
func (g *Gateway) load() {
	for _, suffix := range []string{".2", ".1", ""} {
		path := filepath.Join(g.Dir, "requests.jsonl"+suffix)
		info, err := os.Stat(path)
		if err != nil || info.Size() > journalLimit+16384 {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			g.journalErr = "Журнал запросов недоступен."
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 8192)
		for scanner.Scan() {
			var r Record
			if len(scanner.Bytes()) <= 4096 && json.Unmarshal(scanner.Bytes(), &r) == nil && r.ID != "" && r.Ended != nil {
				g.add(&r)
			}
		}
		f.Close()
	}
}

// add counts a finished request. The caller holds the lock.
func (g *Gateway) add(r *Record) {
	g.recent = append(g.recent, r)
	if len(g.recent) > recentLimit {
		g.recent = g.recent[len(g.recent)-recentLimit:]
	}
	s, ok := g.sessions[r.SessionID]
	if !ok {
		if len(g.sessions) >= sessionLimit {
			var oldest *Session
			for _, x := range g.sessions {
				if oldest == nil || x.LastRequest.Before(oldest.LastRequest) {
					oldest = x
				}
			}
			delete(g.sessions, oldest.ID)
		}
		s = &Session{ID: r.SessionID, Engine: r.Engine, Model: r.Model, Started: r.Started}
		g.sessions[r.SessionID] = s
	}
	if r.KeyID != "" {
		u := g.keyUse[r.KeyID]
		if u == nil {
			u = &KeyUsage{}
			g.keyUse[r.KeyID] = u
		}
		u.Requests++
		if r.TotalTokens != nil {
			u.Tokens += *r.TotalTokens
		}
		if r.Ended.After(u.LastUsed) {
			u.LastUsed = *r.Ended
		}
	}
	s.Requests++
	s.LastRequest = *r.Ended
	if r.InputTokens != nil {
		s.InputTokens += *r.InputTokens
	}
	if r.OutputTokens != nil {
		s.OutputTokens += *r.OutputTokens
	}
	if r.TotalTokens != nil {
		s.TotalTokens += *r.TotalTokens
	}
	if r.InputTokens == nil || r.OutputTokens == nil {
		s.MissingUsage++
	}
}

// sessionID groups requests: a client session header gives a stable id, otherwise all requests
// to one model share an id until a control operation ends it.
func (g *Gateway) sessionID(engine, model, header string) string {
	key := engine + "\n" + model
	if header != "" && len(header) <= 128 {
		sum := sha256.Sum256([]byte(key + "\n" + header))
		return hex.EncodeToString(sum[:])[:24]
	}
	if id, ok := g.modelSessions[key]; ok {
		return id
	}
	id := newID()
	g.modelSessions[key] = id
	return id
}

// Activity is the /api/activity document.
type Activity struct {
	Active      *Record    `json:"active"`
	Queued      int        `json:"queued"`
	ControlBusy bool       `json:"control_busy"`
	Requests    []Record   `json:"requests"`
	Sessions    []*Session `json:"sessions"`
	JournalErr  string     `json:"journal_error"`
}

// Snapshot returns recent requests and sessions for the panel.
func (g *Gateway) Snapshot() Activity {
	g.mu.Lock()
	defer g.mu.Unlock()
	a := Activity{Queued: len(g.queue), ControlBusy: g.control, Requests: []Record{}, Sessions: []*Session{}, JournalErr: g.journalErr}
	if g.current != nil {
		c := *g.current
		a.Active = &c
	}
	for i := len(g.recent) - 1; i >= 0 && len(a.Requests) < 200; i-- {
		a.Requests = append(a.Requests, *g.recent[i])
	}
	for _, s := range g.sessions {
		c := *s
		a.Sessions = append(a.Sessions, &c)
	}
	sort.Slice(a.Sessions, func(i, j int) bool { return a.Sessions[i].LastRequest.After(a.Sessions[j].LastRequest) })
	return a
}

// KeyUsage returns the use of each API key by its id, from the request journal.
func (g *Gateway) KeyUsage() map[string]KeyUsage {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]KeyUsage, len(g.keyUse))
	for id, u := range g.keyUse {
		out[id] = *u
	}
	return out
}

// IsActive tells whether a generation runs on the engine.
func (g *Gateway) IsActive(engine string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.current != nil && g.current.Engine == engine && g.current.State != "queued"
}

// LastActivity is when the engine last finished a request.
func (g *Gateway) LastActivity(engine string) time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	var last time.Time
	for _, r := range g.recent {
		if r.Engine == engine && r.Ended != nil && r.Ended.After(last) {
			last = *r.Ended
		}
	}
	return last
}

// ObserveSlot records decoding progress that llama-server reports in /slots during a generation.
func (g *Gateway) ObserveSlot(engine, id string, decoded int64, context int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.current
	if r == nil || r.Engine != engine || (id != "" && r.ID != id) {
		return
	}
	if r.OutputTokens == nil || decoded > *r.OutputTokens {
		r.OutputTokens = ptr(decoded)
	}
	if context > 0 {
		r.ContextLimit = context
	}
	if r.InputTokens != nil {
		r.updateContext()
		r.TotalTokens = r.ContextUsed
	}
}

// ActiveID is the id of the generation running on the engine, or "".
func (g *Gateway) ActiveID(engine string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.current != nil && g.current.Engine == engine {
		return g.current.ID
	}
	return ""
}

var hexID = func(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// FindContent returns one stored content record by the id from the metadata journal.
func (g *Gateway) FindContent(id string) (json.RawMessage, bool) {
	if !hexID(id) || strings.ToLower(id) != id {
		return nil, false
	}
	marker := []byte(fmt.Sprintf(`{"id":%q`, id))
	g.journal.Lock()
	defer g.journal.Unlock()
	for _, suffix := range []string{"", ".1", ".2"} {
		f, err := os.Open(filepath.Join(g.Dir, "content.jsonl"+suffix))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64<<10), 8*textLimit)
		for scanner.Scan() {
			if bytes.HasPrefix(scanner.Bytes(), marker) {
				line := append([]byte{}, scanner.Bytes()...)
				f.Close()
				return line, true
			}
		}
		f.Close()
	}
	return nil, false
}
