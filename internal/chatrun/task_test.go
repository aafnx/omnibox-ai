package chatrun

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
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

// A task with its own sites searches only there, reads the pages it finds and may follow the
// links between the site's pages — and nowhere else.
func TestTaskWalksItsSites(t *testing.T) {
	g := newRig(t)
	g.r.Config = config.Default
	var queries []string
	g.r.Search = func(_ context.Context, q websearch.Query) (websearch.Found, error) {
		queries = append(queries, q.Text)
		return websearch.Found{Results: []websearch.Result{{Title: "Поиск законопроектов", URL: "https://sozd.duma.gov.ru/oz?q=1", Snippet: "Результаты"}}}, nil
	}
	var read []string
	var mu sync.Mutex
	g.r.Read = func(_ context.Context, url string) (websearch.Page, error) {
		mu.Lock()
		read = append(read, url)
		mu.Unlock()
		return websearch.Page{URL: url, Title: "СОЗД", Text: "Законопроект о связи. " + strings.Repeat("Текст карточки. ", 20), Links: []websearch.Link{
			{Text: "№ 2-8 О связи", URL: "https://sozd.duma.gov.ru/bill/2-8"}, {Text: "Чужой сайт", URL: "https://evil.example/x"}}}, nil
	}
	g.r.Automation = g.r.Local
	call := func(url string) string {
		args, _ := json.Marshal(map[string]string{"url": url})
		return `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read_page","arguments":` + strconvQuote(string(args)) + `}}]}}]}`
	}
	g.replies = []string{
		sse(`{"choices":[{"delta":{"content":"{\"question\":\"законопроекты о связи\",\"queries\":[\"законопроекты о связи\"]}"}}]}`),
		sse(call("https://sozd.duma.gov.ru/bill/2-8")),
		sse(call("https://evil.example/x")),
		sse(`{"choices":[{"delta":{"content":"Законопроект № 2-8 [2]."}}]}`),
	}
	var trace []string
	var tmu sync.Mutex
	msg, err := g.r.Task(context.Background(), Task{Model: local.Key, Session: "panel-auto-x", Prompt: "Найди законопроекты о связи.", Web: true, Read: true, Sites: []string{"sozd.duma.gov.ru"},
		Trace: func(s string) { tmu.Lock(); trace = append(trace, s); tmu.Unlock() }})
	if err != nil || msg.Text != "Законопроект № 2-8 [2]." {
		t.Fatalf("%v %+v", err, msg)
	}
	if len(queries) != 1 || !strings.HasSuffix(queries[0], " site:sozd.duma.gov.ru") {
		t.Fatalf("queries %v", queries)
	}
	mu.Lock()
	got := append([]string{}, read...)
	mu.Unlock()
	if !slices.Contains(got, "https://sozd.duma.gov.ru/bill/2-8") || slices.Contains(got, "https://evil.example/x") {
		t.Fatalf("read %v", got)
	}
	sys := g.bodies[1]["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "- № 2-8 О связи — https://sozd.duma.gov.ru/bill/2-8") || strings.Contains(sys, "evil.example") || !strings.Contains(sys, "только по сайтам sozd.duma.gov.ru") {
		t.Fatalf("system: %s", sys)
	}
	last := g.bodies[3]["messages"].([]any)
	if c := last[len(last)-1].(map[string]any)["content"].(string); !strings.Contains(c, "прочитать нельзя") {
		t.Fatalf("a page off the sites was read: %s", c)
	}
	// The trace tells where the time went: the search, the pages, the model's turns.
	tmu.Lock()
	tr := strings.Join(trace, "\n")
	tmu.Unlock()
	for _, want := range []string{"0:00 Модель составляет поисковые запросы", "site:sozd.duma.gov.ru»: найдено 1", "Прочитана страница https://sozd.duma.gov.ru/oz?q=1",
		"Модель открыла https://sozd.duma.gov.ru/bill/2-8", "Модель хотела открыть https://evil.example/x — нельзя", "Модель a отвечает: запрос", "Модель ответила за"} {
		if !strings.Contains(tr, want) {
			t.Fatalf("trace has no %q:\n%s", want, tr)
		}
	}
}
