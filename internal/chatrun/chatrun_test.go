package chatrun

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/chats"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/library"
	"github.com/aafnx/omnibox-ai/internal/openrouter"
	"github.com/aafnx/omnibox-ai/internal/reasoning"
	"github.com/aafnx/omnibox-ai/internal/websearch"
)

type rig struct {
	t      *testing.T
	r      *Runner
	mu     sync.Mutex
	bodies []map[string]any
	// replies are the SSE streams of the local model, one per request.
	replies []string
	chat    *chats.Chat
}

func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString("data: " + e + "\n\n")
	}
	return b.String() + "data: [DONE]\n\n"
}

var local = library.Model{Key: "prismml:a.gguf", Source: "local", Engine: "prismml", Name: "a", Alias: "a", Running: true,
	Caps: library.Caps{Tools: true}, Reasoning: reasoning.Profile{Mode: reasoning.ModeToggle, Default: reasoning.StepOn, Show: reasoning.ShowFull}}

func newRig(t *testing.T) *rig {
	dir := t.TempDir()
	g := &rig{t: t}
	g.r = &Runner{
		Chats:      &chats.Store{Dir: filepath.Join(dir, "chats")},
		Assistants: &chats.Assistants{Path: filepath.Join(dir, "assistants.json")},
		Config:     config.Default,
		Model: func(ctx context.Context, key string) (library.Model, int, error) {
			if key == local.Key {
				return local, 32768, nil
			}
			if key == "cloud:openai/gpt-5" {
				return library.Model{Key: key, Source: "cloud", Engine: "openrouter", ID: "openai/gpt-5", Name: "GPT-5",
					Reasoning: reasoning.ForCloud("openai/gpt-5", nil)}, 400000, nil
			}
			return library.Model{}, 0, errors.New("Нет такой модели.")
		},
		Local: func(ctx context.Context, session string, body []byte) *http.Response {
			var m map[string]any
			json.Unmarshal(body, &m)
			g.mu.Lock()
			g.bodies = append(g.bodies, m)
			reply := g.replies[0]
			g.replies = g.replies[1:]
			g.mu.Unlock()
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply))}
		},
		Search: func(ctx context.Context, q string) ([]websearch.Result, error) {
			return []websearch.Result{{Title: "Wake on LAN", URL: "https://www.home-assistant.io/wol", Snippet: "magic packet"}}, nil
		},
		Spend: &openrouter.Spend{Path: filepath.Join(dir, "spend.json")},
	}
	c, err := g.r.Chats.Create("", 720, "", "")
	if err != nil {
		t.Fatal(err)
	}
	g.chat = c
	return g
}

// send posts a message and returns the status and the events by name.
func (g *rig) send(req Request) (int, map[string][]string) {
	data, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/api/chat/send", bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.r.ServeHTTP(w, r)
	events := map[string][]string{}
	if w.Code != 200 {
		events["error"] = []string{w.Body.String()}
		return w.Code, events
	}
	for _, block := range strings.Split(w.Body.String(), "\n\n") {
		name, data := "", ""
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				name = v
			}
			if v, ok := strings.CutPrefix(line, "data: "); ok {
				data = v
			}
		}
		if name != "" {
			events[name] = append(events[name], data)
		}
	}
	return w.Code, events
}

func TestLocalAnswerIsStreamedAndStored(t *testing.T) {
	g := newRig(t)
	g.replies = []string{sse(`{"choices":[{"delta":{"reasoning_content":"думаю"}}]}`, `{"choices":[{"delta":{"content":"При"}}]}`,
		`{"choices":[{"delta":{"content":"вет"}}]}`, `{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3},"timings":{"predicted_per_second":61.2}}`)}
	code, ev := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Как дела?\nвторая строка", Effort: "off"})
	if code != 200 || len(ev["delta"]) != 2 || len(ev["reasoning"]) != 1 || len(ev["done"]) != 1 {
		t.Fatalf("%d %v", code, ev)
	}
	body := g.bodies[0]
	if body["model"] != "a" || body["stream"] != true || body["tools"] != nil {
		t.Fatalf("body %v", body)
	}
	if k, _ := body["chat_template_kwargs"].(map[string]any); k["enable_thinking"] != false {
		t.Fatalf("thinking not switched off: %v", body)
	}
	c, _ := g.r.Chats.Get(g.chat.ID)
	if c.Title != "Как дела?" || len(c.Messages) != 2 || c.Model != local.Key {
		t.Fatalf("%+v", c)
	}
	a := c.Messages[1]
	if a.Text != "Привет" || a.Reasoning != "думаю" || a.Meta.InputTokens != 12 || a.Meta.OutputTokens != 3 || a.Meta.TokensPerSecond != 61.2 {
		t.Fatalf("%+v %+v", a, a.Meta)
	}
}

func TestToolCallsSearchTheWeb(t *testing.T) {
	g := newRig(t)
	g.replies = []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"web_search","arguments":"{\"query\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"wol synology\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"Через Home Assistant [1]."}}]}`),
	}
	code, ev := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Как разбудить ПК?", Web: true})
	if code != 200 || len(ev["search"]) != 1 || len(g.bodies) != 2 {
		t.Fatalf("%d %v", code, ev)
	}
	if g.bodies[0]["tool_choice"] != "auto" {
		t.Fatalf("tools not offered: %v", g.bodies[0])
	}
	msgs := g.bodies[1]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "tool" || !strings.Contains(last["content"].(string), "[1] Wake on LAN — home-assistant.io") {
		t.Fatalf("tool result: %v", last)
	}
	c, _ := g.r.Chats.Get(g.chat.ID)
	m := c.Messages[1].Meta
	if len(m.Sources) != 1 || m.Searches[0] != "wol synology" || c.Messages[1].Text != "Через Home Assistant [1]." {
		t.Fatalf("%+v", m)
	}
}

func TestModelsWithoutToolsSearchFirst(t *testing.T) {
	g := newRig(t)
	local.Caps.Tools = false
	defer func() { local.Caps.Tools = true }()
	g.replies = []string{sse(`{"choices":[{"delta":{"content":"<think>хм</think>\nОтвет"}}]}`)}
	g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "погода в Москве", Web: true})
	sys := g.bodies[0]["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "Результаты веб-поиска") || g.bodies[0]["tools"] != nil {
		t.Fatalf("%v", sys)
	}
	c, _ := g.r.Chats.Get(g.chat.ID)
	if c.Messages[1].Text != "Ответ" || c.Messages[1].Reasoning != "хм" {
		t.Fatalf("%+v", c.Messages[1])
	}
}

func TestFilesTheModelCannotReadAreRefusedBeforeSending(t *testing.T) {
	g := newRig(t)
	img := base64.StdEncoding.EncodeToString([]byte("png"))
	code, ev := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "что на фото?", Files: []File{{Name: "a.png", Data: img}}})
	if code != 400 || !strings.Contains(ev["error"][0], "не понимает изображения") || len(g.bodies) != 0 {
		t.Fatalf("%d %v", code, ev)
	}
	if c, _ := g.r.Chats.Get(g.chat.ID); len(c.Messages) != 0 {
		t.Fatal("a refused message was stored")
	}
	// A text file is read for any model and kept for later questions.
	g.replies = []string{sse(`{"choices":[{"delta":{"content":"ok"}}]}`)}
	code, _ = g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "сводка", Files: []File{{Name: "n.md", Data: base64.StdEncoding.EncodeToString([]byte("# заметка"))}}})
	user := g.bodies[0]["messages"].([]any)[1].(map[string]any)["content"].(string)
	if code != 200 || !strings.Contains(user, "--- Файл «n.md» ---\n# заметка") {
		t.Fatalf("%d %q", code, user)
	}
	if c, _ := g.r.Chats.Get(g.chat.ID); c.Messages[0].Attachments[0].Text != "# заметка" {
		t.Fatalf("%+v", c.Messages[0])
	}
}

func TestRetryReplacesTheAnswer(t *testing.T) {
	g := newRig(t)
	g.replies = []string{sse(`{"choices":[{"delta":{"content":"первый"}}]}`), sse(`{"choices":[{"delta":{"content":"второй"}}]}`)}
	g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "вопрос"})
	c, _ := g.r.Chats.Get(g.chat.ID)
	code, _ := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Retry: c.Messages[0].ID})
	c, _ = g.r.Chats.Get(g.chat.ID)
	if code != 200 || len(c.Messages) != 2 || c.Messages[1].Text != "второй" {
		t.Fatalf("%d %+v", code, c.Messages)
	}
}

func TestStoppedLocalModelIsRefused(t *testing.T) {
	g := newRig(t)
	local.Running = false
	defer func() { local.Running = true }()
	if code, ev := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "x"}); code != 409 || !strings.Contains(ev["error"][0], "не запущена") {
		t.Fatalf("%d %v", code, ev)
	}
}

func TestCloudAnswerCostsAndLearnsRefusedParameters(t *testing.T) {
	g := newRig(t)
	var got []map[string]any
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		got = append(got, m)
		if _, ok := m["temperature"]; ok {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Unsupported parameter: temperature"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`{"choices":[{"delta":{"reasoning":"мысль"}}]}`, `{"choices":[{"delta":{"content":"Да"}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"cost":0.0042}}`))
	}))
	defer or.Close()
	g.r.Cloud = func() (*openrouter.Client, error) { return &openrouter.Client{Key: "sk-or-v1-test", BaseURL: or.URL}, nil }
	a, _ := g.r.Assistants.Save(chats.Assistant{Name: "Точный", Style: "precise", Instructions: "Отвечай кратко."})
	id := a.ID
	code, ev := g.send(Request{ChatID: g.chat.ID, Model: "cloud:openai/gpt-5", Text: "привет", Assistant: &id, Effort: "high"})
	if code != 200 || len(ev["done"]) != 1 {
		t.Fatalf("%d %v", code, ev)
	}
	// GPT-5 takes no sampling: the profile already removed the temperature.
	if len(got) != 1 || got[0]["temperature"] != nil || got[0]["model"] != "openai/gpt-5" {
		t.Fatalf("%v", got)
	}
	if r, _ := got[0]["reasoning"].(map[string]any); r["effort"] != "high" {
		t.Fatalf("effort: %v", got[0])
	}
	sys := got[0]["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "Отвечай кратко.") {
		t.Fatal(sys)
	}
	c, _ := g.r.Chats.Get(g.chat.ID)
	if c.CostUSD != 0.0042 || c.AssistantID != id || c.Messages[1].Reasoning != "мысль" {
		t.Fatalf("%+v", c)
	}
	if days := g.r.Spend.Days(1); days[0].USD != 0.0042 {
		t.Fatalf("%v", days)
	}
	// A model that refuses a parameter the profile allowed gets it dropped and remembered.
	g.r.Model = func(ctx context.Context, key string) (library.Model, int, error) {
		return library.Model{Key: key, Source: "cloud", Engine: "openrouter", ID: "x/y", Name: "Y"}, 8000, nil
	}
	got = nil
	g.send(Request{ChatID: g.chat.ID, Model: "cloud:x/y", Text: "ещё"})
	if len(got) != 2 || got[1]["temperature"] != nil {
		t.Fatalf("%v", got)
	}
	got = nil
	g.send(Request{ChatID: g.chat.ID, Model: "cloud:x/y", Text: "и ещё"})
	if len(got) != 1 {
		t.Fatalf("the refused parameter was sent again: %v", got)
	}
}

func TestThinkSplitter(t *testing.T) {
	s := &thinkSplitter{}
	var text, think string
	for _, c := range []string{"<thi", "nk>раз", "мышление</th", "ink>\nОтв", "ет"} {
		a, b := s.split(c)
		text, think = text+a, think+b
	}
	if text != "Ответ" || think != "размышление" {
		t.Fatalf("%q %q", text, think)
	}
	s = &thinkSplitter{}
	if a, _ := s.split("Просто текст <think> внутри"); a != "Просто текст <think> внутри" {
		t.Fatal(a)
	}
}
