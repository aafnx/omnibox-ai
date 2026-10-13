package chatrun

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/websearch"
)

// An automation's task searches like the chat but stores nothing: the answer comes back to the
// caller, and every request of it — the search planner's too — goes the automation's way.
func TestTaskSearchesAndStoresNothing(t *testing.T) {
	g := newRig(t)
	g.r.Config = config.Default
	searched := 0
	g.r.Search = func(_ context.Context, q websearch.Query) (websearch.Found, error) {
		searched++
		return websearch.Found{Results: []websearch.Result{{Title: "ИИ за неделю", URL: "https://news.example/ai", Snippet: "OpenAI выпустила модель"}}}, nil
	}
	g.r.Automation = g.r.Local
	g.r.Local = func(context.Context, string, []byte) *http.Response {
		t.Fatal("a task went the chat's way")
		return nil
	}
	g.replies = []string{
		sse(`{"choices":[{"delta":{"content":"{\"question\":\"новости ИИ за неделю\",\"queries\":[\"новости ИИ\"],\"recency\":\"week\",\"news\":true}"}}]}`),
		sse(`{"choices":[{"delta":{"content":"Главное за неделю [1]."}}]}`),
	}
	before, _ := g.r.Chats.List()
	msg, err := g.r.Task(context.Background(), Task{Model: local.Key, Session: "panel-auto-x", Prompt: "Сделай сводку новостей ИИ за неделю.", Web: true, Read: false})
	if err != nil || msg.Text != "Главное за неделю [1]." || msg.Meta == nil || len(msg.Meta.Sources) != 1 || searched != 1 {
		t.Fatalf("%v %+v searched %d", err, msg, searched)
	}
	if msgs := g.bodies[1]["messages"].([]any); !strings.Contains(msgs[len(msgs)-1].(map[string]any)["content"].(string), "Сделай сводку новостей ИИ") ||
		!strings.Contains(msgs[0].(map[string]any)["content"].(string), "задача автоматизации") || !strings.Contains(mustJSON(g.bodies[0]["messages"]), "Сделай сводку") {
		t.Fatal("the task is not the user's message or the planner did not see it")
	}
	after, _ := g.r.Chats.List()
	if len(after) != len(before) {
		t.Fatal("a task was stored as a chat")
	}
	// A private task never searches, whatever it asks for.
	g.replies = []string{sse(`{"choices":[{"delta":{"content":"Без поиска."}}]}`)}
	msg, err = g.r.Task(context.Background(), Task{Model: local.Key, Private: true, Session: "panel-auto-x", Prompt: "Найди в интернете", Web: true, Read: true})
	if err != nil || msg.Text != "Без поиска." || searched != 1 || g.bodies[2]["tools"] != nil {
		t.Fatalf("private task searched: %v %+v %d", err, msg, searched)
	}
}
