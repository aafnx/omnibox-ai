package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Error is a refusal before the answer starts, with the HTTP status for /v1 clients.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string   { return e.Message }
func (e *Error) HTTPStatus() int { return e.Status }

// Prefix names the subscription's models for /v1 clients: chatgpt/gpt-5.5.
const Prefix = "chatgpt/"

// defaultInstructions replace Codex's own prompt (it is a coding agent's) when the request has
// no system message.
const defaultInstructions = "You are ChatGPT, a helpful assistant. Answer in the language of the user."

// allowedItems are what an answer may consist of; anything else (a command, a file change, a
// tool call) interrupts the turn.
var allowedItems = map[string]bool{"userMessage": true, "agentMessage": true, "reasoning": true, "contextCompaction": true, "hookPrompt": true}

type part struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// parts reads a message's content: a string or a list of text and image parts.
func parts(raw json.RawMessage) ([]part, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []part{{Type: "text", Text: text}}, nil
	}
	var list []part
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("message content must be a string or a list of parts")
	}
	for _, p := range list {
		switch p.Type {
		case "text", "input_text":
		case "image_url":
			if p.ImageURL == nil || !(strings.HasPrefix(p.ImageURL.URL, "data:image/") || strings.HasPrefix(p.ImageURL.URL, "https://")) {
				return nil, errors.New("an image must be a data: or https: URL")
			}
		default:
			return nil, fmt.Errorf("content of type %q is not supported by ChatGPT here", p.Type)
		}
	}
	return list, nil
}

// item is a message of the history as the Responses API holds it.
func item(role string, ps []part) map[string]any {
	var content []map[string]any
	for _, p := range ps {
		switch {
		case p.ImageURL != nil && role == "user":
			content = append(content, map[string]any{"type": "input_image", "image_url": p.ImageURL.URL})
		case p.Text != "" && role == "assistant":
			content = append(content, map[string]any{"type": "output_text", "text": p.Text})
		case p.Text != "":
			content = append(content, map[string]any{"type": "input_text", "text": p.Text})
		}
	}
	return map[string]any{"type": "message", "role": role, "content": content}
}

// request is a chat.completions request turned into a Codex thread.
type request struct {
	model        string
	stream       bool
	effort       string
	instructions string
	history      []map[string]any
	input        []map[string]any
}

func (s *Service) parse(ctx context.Context, body []byte) (*request, error) {
	var in struct {
		Model     string        `json:"model"`
		Messages  []chatMessage `json:"messages"`
		Stream    bool          `json:"stream"`
		Effort    string        `json:"reasoning_effort"`
		Reasoning *struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, &Error{http.StatusBadRequest, "invalid JSON body"}
	}
	r := &request{model: strings.TrimPrefix(in.Model, Prefix), stream: in.Stream, effort: in.Effort}
	if in.Reasoning != nil && in.Reasoning.Effort != "" {
		r.effort = in.Reasoning.Effort
	}
	models, err := s.Models(ctx)
	if err != nil {
		return nil, &Error{http.StatusServiceUnavailable, err.Error()}
	}
	i := slices.IndexFunc(models, func(m Model) bool { return m.ID == r.model })
	if i < 0 {
		return nil, &Error{http.StatusNotFound, "model " + in.Model + " is not available in the ChatGPT subscription"}
	}
	m := models[i]
	if !slices.Contains(m.Efforts, r.effort) {
		r.effort = ""
	}
	var system []string
	var convo []chatMessage
	for _, msg := range in.Messages {
		switch msg.Role {
		case "system", "developer":
			ps, err := parts(msg.Content)
			if err != nil {
				return nil, &Error{http.StatusBadRequest, err.Error()}
			}
			for _, p := range ps {
				if strings.TrimSpace(p.Text) != "" {
					system = append(system, p.Text)
				}
			}
		case "user", "assistant":
			convo = append(convo, msg)
		}
		// Tool results have nowhere to go: ChatGPT is given no tools here.
	}
	if len(convo) == 0 || convo[len(convo)-1].Role != "user" {
		return nil, &Error{http.StatusBadRequest, "the last message must be from the user"}
	}
	r.instructions = strings.Join(system, "\n\n")
	if r.instructions == "" {
		r.instructions = defaultInstructions
	}
	for k, msg := range convo {
		ps, err := parts(msg.Content)
		if err != nil {
			return nil, &Error{http.StatusBadRequest, err.Error()}
		}
		if k < len(convo)-1 {
			if it := item(msg.Role, ps); len(it["content"].([]map[string]any)) > 0 {
				r.history = append(r.history, it)
			}
			continue
		}
		for _, p := range ps {
			switch {
			case p.ImageURL != nil:
				if !m.Images {
					return nil, &Error{http.StatusBadRequest, m.Name + " does not accept images"}
				}
				r.input = append(r.input, map[string]any{"type": "image", "url": p.ImageURL.URL})
			case p.Text != "":
				r.input = append(r.input, map[string]any{"type": "text", "text": p.Text})
			}
		}
	}
	if len(r.input) == 0 {
		return nil, &Error{http.StatusBadRequest, "the last message is empty"}
	}
	return r, nil
}

// note is a notification of the thread.
type note struct {
	method string
	params json.RawMessage
}

// queue holds the thread's notifications: the reader never waits for the answer's writer.
type queue struct {
	mu    sync.Mutex
	items []note
	ready chan struct{}
}

func (q *queue) push(n note) {
	q.mu.Lock()
	q.items = append(q.items, n)
	q.mu.Unlock()
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

func (q *queue) take() []note {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.items
	q.items = nil
	return out
}

// Chat answers an OpenAI chat.completions request with a model of the subscription: a stream of
// chat.completion.chunk events, or one chat.completion without "stream". The request becomes an
// ephemeral thread: the system messages are its instructions, the earlier messages its history.
func (s *Service) Chat(ctx context.Context, body []byte) (*http.Response, error) {
	r, err := s.parse(ctx, body)
	if err != nil {
		return nil, err
	}
	c, release, err := s.use(ctx)
	if err != nil {
		return nil, &Error{http.StatusServiceUnavailable, err.Error()}
	}
	q := &queue{ready: make(chan struct{}, 1)}
	var threadID string
	var idMu sync.Mutex
	unsubscribe := c.subscribe(func(method string, params json.RawMessage) {
		var head struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(params, &head)
		idMu.Lock()
		mine := head.ThreadID != "" && head.ThreadID == threadID
		idMu.Unlock()
		if mine {
			q.push(note{method, params})
		}
	})
	fail := func(err error) (*http.Response, error) {
		unsubscribe()
		release()
		var e *Error
		if errors.As(err, &e) {
			return nil, e
		}
		if errors.Is(explain(err), ErrSignedOut) {
			return nil, &Error{http.StatusUnauthorized, ErrSignedOut.Error()}
		}
		return nil, &Error{http.StatusBadGateway, explain(err).Error()}
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	err = c.call(ctx, "thread/start", map[string]any{
		"model": r.model, "ephemeral": true, "sandbox": "read-only", "approvalPolicy": "on-request",
		"baseInstructions": r.instructions, "cwd": s.work(),
		// No environment: Codex offers the model no shell, no file edits and no images to view.
		"environments": []any{},
	}, &started)
	if err != nil {
		return fail(err)
	}
	idMu.Lock()
	threadID = started.Thread.ID
	idMu.Unlock()
	closeThread := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.call(ctx, "thread/unsubscribe", map[string]any{"threadId": threadID}, nil)
	}
	if len(r.history) > 0 {
		if err := c.call(ctx, "thread/inject_items", map[string]any{"threadId": threadID, "items": r.history}, nil); err != nil {
			closeThread()
			return fail(err)
		}
	}
	turnParams := map[string]any{"threadId": threadID, "input": r.input, "model": r.model, "summary": "auto"}
	if r.effort != "" {
		turnParams["effort"] = r.effort
	}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := c.call(ctx, "turn/start", turnParams, &turn); err != nil {
		closeThread()
		return fail(err)
	}
	w := &writer{model: Prefix + r.model, id: "chatcmpl-" + turn.Turn.ID, created: s.now().Unix(), stream: r.stream}
	done := func() {
		closeThread()
		unsubscribe()
		release()
	}
	if !r.stream {
		var buf strings.Builder
		w.w = &buf
		err := s.follow(ctx, c, q, threadID, turn.Turn.ID, w)
		done()
		if err != nil {
			return nil, &Error{http.StatusBadGateway, err.Error()}
		}
		w.whole()
		h := http.Header{}
		h.Set("Content-Type", "application/json")
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader(buf.String()))}, nil
	}
	pr, pw := io.Pipe()
	w.w = pw
	go func() {
		defer done()
		if err := s.follow(ctx, c, q, threadID, turn.Turn.ID, w); err != nil {
			w.chunk(map[string]any{"error": map[string]any{"message": err.Error()}})
		} else {
			w.finish()
		}
		_ = pw.Close()
	}()
	h := http.Header{}
	h.Set("Content-Type", "text/event-stream")
	return &http.Response{StatusCode: http.StatusOK, Header: h, Body: pr}, nil
}

// follow turns the thread's notifications into the answer until the turn ends. A cancelled
// request interrupts the turn, so the subscription stops spending on it.
func (s *Service) follow(ctx context.Context, c *conn, q *queue, threadID, turnID string, w *writer) error {
	interrupt := func() {
		ictx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.call(ictx, "turn/interrupt", map[string]any{"threadId": threadID, "turnId": turnID}, nil)
	}
	for {
		for _, n := range q.take() {
			var p struct {
				TurnID string `json:"turnId"`
				Delta  string `json:"delta"`
				Item   struct {
					Type string `json:"type"`
				} `json:"item"`
				Turn struct {
					ID     string `json:"id"`
					Status string `json:"status"`
					Error  *struct {
						Message string `json:"message"`
					} `json:"error"`
				} `json:"turn"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
				WillRetry  bool `json:"willRetry"`
				TokenUsage struct {
					Last struct {
						Input     int `json:"inputTokens"`
						Cached    int `json:"cachedInputTokens"`
						Output    int `json:"outputTokens"`
						Reasoning int `json:"reasoningOutputTokens"`
					} `json:"last"`
				} `json:"tokenUsage"`
			}
			_ = json.Unmarshal(n.params, &p)
			if p.TurnID != "" && p.TurnID != turnID {
				continue
			}
			switch n.method {
			case "item/agentMessage/delta":
				w.content(p.Delta)
			case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
				w.reasoning(p.Delta)
			case "item/reasoning/summaryPartAdded":
				w.reasoning("\n\n")
			case "item/started":
				if !allowedItems[p.Item.Type] {
					interrupt()
					s.logf("chatgpt: interrupted a turn that started %q", p.Item.Type)
					return fmt.Errorf("ChatGPT попытался выполнить действие (%s), а не ответить: ответ остановлен.", p.Item.Type)
				}
			case "thread/tokenUsage/updated":
				u := p.TokenUsage.Last
				w.usage = &usage{Prompt: u.Input, Completion: u.Output, Total: u.Input + u.Output, Details: map[string]int{"reasoning_tokens": u.Reasoning}}
			case "error":
				if p.Error != nil && !p.WillRetry {
					return errors.New(limitText(p.Error.Message))
				}
			case "turn/completed":
				switch p.Turn.Status {
				case "failed":
					msg := "ChatGPT не ответил."
					if p.Turn.Error != nil && p.Turn.Error.Message != "" {
						msg = limitText(p.Turn.Error.Message)
					}
					return errors.New(msg)
				case "interrupted":
					return errors.New("Ответ остановлен.")
				}
				return nil
			}
		}
		select {
		case <-q.ready:
		case <-c.done:
			return errors.New("Codex завершился во время ответа.")
		case <-ctx.Done():
			interrupt()
			return ctx.Err()
		}
	}
}

// limitText explains a refusal for a reached limit in the owner's words.
func limitText(msg string) string {
	low := strings.ToLower(msg)
	if strings.Contains(low, "usage limit") || strings.Contains(low, "rate limit") {
		return "Достигнут лимит подписки ChatGPT: " + clip(msg, 300)
	}
	return "ChatGPT: " + clip(msg, 300)
}

type usage struct {
	Prompt     int            `json:"prompt_tokens"`
	Completion int            `json:"completion_tokens"`
	Total      int            `json:"total_tokens"`
	Details    map[string]int `json:"completion_tokens_details,omitempty"`
}

// writer writes the answer as OpenAI's chat.completion chunks, or keeps it for one reply.
type writer struct {
	w        io.Writer
	model    string
	id       string
	created  int64
	stream   bool
	text     strings.Builder
	thinking strings.Builder
	usage    *usage
}

func (w *writer) chunk(v map[string]any) {
	data, _ := json.Marshal(v)
	_, _ = fmt.Fprintf(w.w, "data: %s\n\n", data)
}

func (w *writer) delta(d map[string]any, finish any) {
	w.chunk(map[string]any{"id": w.id, "object": "chat.completion.chunk", "created": w.created, "model": w.model,
		"choices": []any{map[string]any{"index": 0, "delta": d, "finish_reason": finish}}})
}

func (w *writer) content(s string) {
	if s == "" {
		return
	}
	w.text.WriteString(s)
	if w.stream {
		w.delta(map[string]any{"content": s}, nil)
	}
}

func (w *writer) reasoning(s string) {
	if s == "" {
		return
	}
	w.thinking.WriteString(s)
	if w.stream {
		w.delta(map[string]any{"reasoning_content": s}, nil)
	}
}

func (w *writer) finish() {
	w.delta(map[string]any{}, "stop")
	if w.usage != nil {
		w.chunk(map[string]any{"id": w.id, "object": "chat.completion.chunk", "created": w.created, "model": w.model, "choices": []any{}, "usage": w.usage})
	}
	_, _ = io.WriteString(w.w, "data: [DONE]\n\n")
}

func (w *writer) whole() {
	msg := map[string]any{"role": "assistant", "content": w.text.String()}
	if w.thinking.Len() > 0 {
		msg["reasoning_content"] = w.thinking.String()
	}
	out := map[string]any{"id": w.id, "object": "chat.completion", "created": w.created, "model": w.model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": "stop"}}}
	if w.usage != nil {
		out["usage"] = w.usage
	}
	data, _ := json.Marshal(out)
	_, _ = w.w.Write(data)
}
