package chatrun

import (
	"strings"
	"testing"
)

// "Answer again" and an edited question start a new version; the earlier answer stays.
func TestRetryAndEditKeepEarlierVersions(t *testing.T) {
	g := newRig(t)
	g.replies = []string{sse(`{"choices":[{"delta":{"content":"Первый"}}]}`), sse(`{"choices":[{"delta":{"content":"Второй"}}]}`), sse(`{"choices":[{"delta":{"content":"По правке"}}]}`)}
	g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Вопрос", Effort: "off"})
	c, _ := g.r.Chats.Get(g.chat.ID)
	q := c.Messages[0].ID
	if code, _ := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Retry: q, Effort: "off"}); code != 200 {
		t.Fatal(code)
	}
	c, _ = g.r.Chats.Get(g.chat.ID)
	if len(c.Messages) != 2 || c.Messages[1].Text != "Второй" || c.Messages[0].Branch != 1 || c.Messages[0].Branches[0][1].Text != "Первый" {
		t.Fatalf("retry: %+v", c.Messages)
	}
	if code, _ := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Edit: q, Text: "Вопрос точнее", Effort: "off"}); code != 200 {
		t.Fatal(code)
	}
	c, _ = g.r.Chats.Get(g.chat.ID)
	if c.Messages[0].Text != "Вопрос точнее" || c.Messages[1].Text != "По правке" || len(c.Messages[0].Branches) != 2 || c.Messages[0].Branch != 2 {
		t.Fatalf("edit: %+v", c.Messages[0])
	}
	// The model got the edited question, not the old one.
	last := g.bodies[len(g.bodies)-1]["messages"].([]any)
	if msg := last[len(last)-1].(map[string]any)["content"].(string); msg != "Вопрос точнее" || len(last) != 2 {
		t.Fatalf("sent: %v", last)
	}
	if code, ev := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Edit: q, Text: "", Effort: "off"}); code != 400 || !strings.Contains(ev["error"][0], "Пустое") {
		t.Fatalf("an empty edit: %d %v", code, ev)
	}
}
