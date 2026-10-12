package chatrun

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/chats"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/library"
	"github.com/aafnx/omnibox-ai/internal/websearch"
)

func planningRig(t *testing.T) (*rig, *[]string) {
	g := newRig(t)
	g.r.Config = config.Default
	var mu sync.Mutex
	queries := &[]string{}
	g.r.Search = func(_ context.Context, sq websearch.Query) (websearch.Found, error) {
		q := sq.Text
		mu.Lock()
		*queries = append(*queries, q)
		mu.Unlock()
		return websearch.Found{Results: []websearch.Result{{Title: "Афиша кино Москва", URL: "https://afisha.example/" + q, Snippet: "Москва кино"}}}, nil
	}
	return g, queries
}

// A follow-up is searched as the question it stands for: the model restates it from the
// conversation and writes short queries with the date.
func TestFollowUpIsSearchedWithItsContext(t *testing.T) {
	g, queries := planningRig(t)
	if _, err := g.r.Chats.Update(g.chat.ID, func(c *chats.Chat) error {
		c.Messages = append(c.Messages,
			chats.Message{ID: chats.NewMessageID(), Role: "user", Text: "Что идёт в кино в Казани на выходных?"},
			chats.Message{ID: chats.NewMessageID(), Role: "assistant", Text: "В Казани идут «Дюна 3» и «Майор Гром»."})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	g.replies = []string{
		sse(`{"choices":[{"delta":{"content":"{\"question\": \"Что идёт в кино в Москве на выходных 17–18 октября 2026?\", \"queries\": [\"афиша кино Москва 17 октября 2026\", \"кинотеатры Москва расписание выходные\"]}"}}]}`),
		sse(`{"choices":[{"delta":{"content":"В Москве идёт… [1]"}}]}`),
	}
	code, ev := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "а в Москве?", Web: true})
	if code != 200 || len(g.bodies) != 2 {
		t.Fatalf("%d %v", code, ev)
	}
	if strings.Join(*queries, "|") != "афиша кино Москва 17 октября 2026|кинотеатры Москва расписание выходные" {
		t.Fatalf("queries %v", *queries)
	}
	plan := mustJSON(g.bodies[0]["messages"])
	if !strings.Contains(plan, "Майор Гром") || !strings.Contains(plan, "а в Москве?") || !strings.Contains(plan, "2026") {
		t.Fatalf("the planner did not get the conversation and the date: %s", plan)
	}
	system := g.bodies[1]["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "«Что идёт в кино в Москве на выходных 17–18 октября 2026?»") || !strings.Contains(system, "[2] Афиша кино Москва") {
		t.Fatalf("system prompt: %s", system)
	}
}

// A planner that does not answer in JSON leaves the question's own words as the query.
func TestBadPlanFallsBackToKeywords(t *testing.T) {
	g, queries := planningRig(t)
	g.replies = []string{
		sse(`{"choices":[{"delta":{"content":"Конечно! Вот что можно поискать."}}]}`),
		sse(`{"choices":[{"delta":{"content":"Ответ"}}]}`),
	}
	if code, _ := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Слушай, а какая погода будет завтра в Казани?", Web: true}); code != 200 {
		t.Fatal(code)
	}
	if len(*queries) != 1 || (*queries)[0] != "погода будет завтра Казани" {
		t.Fatalf("%v", *queries)
	}
}

func TestParsePlanKeywordsAndAnchors(t *testing.T) {
	p, ok := parsePlan("Вот: {\"question\": \" Курс  евро \", \"queries\": [\"курс евро ЦБ\", \"курс евро ЦБ\", \"\", \"euro rate\", \"евро прогноз\", \"лишний\"]} ")
	if !ok || p.Question != "Курс евро" || strings.Join(p.Queries, "|") != "курс евро ЦБ|euro rate|евро прогноз" {
		t.Fatalf("%+v %v", p, ok)
	}
	if p, ok := parsePlan(`{"question":"x","queries":[]}`); !ok || len(p.Queries) != 0 {
		t.Fatal("a plan without queries means no search is needed")
	}
	if _, ok := parsePlan(`не JSON`); ok {
		t.Fatal("an answer without JSON is not a plan")
	}
	if got := keywords("Подскажи, пожалуйста, сколько стоит RTX 5080 в DNS?"); got != "стоит RTX 5080 DNS" {
		t.Fatalf("%q", got)
	}
	if got := anchored("Какая сегодня погода в Казани", "погода Кохем"); got != "погода Кохем Казани" {
		t.Fatalf("%q", got)
	}
	if got := anchored("Какая сегодня погода в Казани", "погода в Казани завтра"); got != "погода в Казани завтра" {
		t.Fatalf("%q", got)
	}
}

// An answering engine is asked the restated question once; its summary and cost reach the answer.
func TestAnsweringEngineGetsTheQuestion(t *testing.T) {
	g, queries := planningRig(t)
	g.r.SearchByQuestion = func() bool { return true }
	g.r.Search = func(_ context.Context, sq websearch.Query) (websearch.Found, error) {
		q := sq.Text
		*queries = append(*queries, q)
		return websearch.Found{Summary: "Курс евро 95,1 ₽.", CostUSD: 0.007, Results: []websearch.Result{{Title: "ЦБ", URL: "https://cbr.example/"}}}, nil
	}
	g.replies = []string{
		sse(`{"choices":[{"delta":{"content":"{\"question\": \"Какой курс евро ЦБ на 13 октября 2026?\", \"queries\": [\"курс евро ЦБ\"]}"}}]}`),
		sse(`{"choices":[{"delta":{"content":"95,1 ₽ [1]"}}]}`),
	}
	if code, _ := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "почём евро завтра?", Web: true}); code != 200 {
		t.Fatal(code)
	}
	if len(*queries) != 1 || (*queries)[0] != "Какой курс евро ЦБ на 13 октября 2026?" {
		t.Fatalf("%v", *queries)
	}
	system := g.bodies[1]["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "Курс евро 95,1 ₽.") || !strings.Contains(system, "[1] ЦБ") {
		t.Fatalf("%s", system)
	}
	c, _ := g.r.Chats.Get(g.chat.ID)
	if c.Messages[len(c.Messages)-1].Meta.CostUSD != 0.007 {
		t.Fatalf("cost %+v", c.Messages[len(c.Messages)-1].Meta)
	}
}

// A greeting needs no search: the planner says so and nothing is searched.
func TestNoSearchWhenThePlannerSaysSo(t *testing.T) {
	g, queries := planningRig(t)
	g.replies = []string{
		sse(`{"choices":[{"delta":{"content":"{\"question\": \"Привет, что ты умеешь?\", \"queries\": []}"}}]}`),
		sse(`{"choices":[{"delta":{"content":"Привет!"}}]}`),
	}
	if code, ev := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "привет, что умеешь?", Web: true}); code != 200 || len(ev["search"]) != 0 {
		t.Fatalf("%d %v", code, ev["search"])
	}
	if len(*queries) != 0 {
		t.Fatalf("%v", *queries)
	}
}

// ChatGPT with OpenAI's search searches itself: OmniBox does not plan or search, the request
// asks for web_search_options and the searches it reports are kept.
func TestChatGPTSearchesItself(t *testing.T) {
	g, queries := planningRig(t)
	gpt := &fakeGPT{reply: sse(`{"choices":[],"omnibox_search":{"query":"погода Казань"}}`, `{"choices":[{"delta":{"content":"+8 [Gismeteo](https://gismeteo.example)"}}]}`)}
	g.r.ChatGPT = gpt
	g.r.ChatGPTSearch = func() bool { return true }
	model := library.Model{Key: "chatgpt:gpt-5.5", Source: "chatgpt", Engine: "chatgpt", ID: "gpt-5.5", Name: "GPT-5.5"}
	prev := g.r.Model
	g.r.Model = func(ctx context.Context, key string) (library.Model, int, error) {
		if key == model.Key {
			return model, 272000, nil
		}
		return prev(ctx, key)
	}
	code, ev := g.send(Request{ChatID: g.chat.ID, Model: model.Key, Text: "погода в Казани", Web: true})
	if code != 200 || len(*queries) != 0 || len(gpt.bodies) != 1 || len(ev["search"]) != 1 {
		t.Fatalf("%d queries %v bodies %d search %v", code, *queries, len(gpt.bodies), ev["search"])
	}
	if _, ok := gpt.bodies[0]["web_search_options"]; !ok || strings.Contains(mustJSON(gpt.bodies[0]["messages"]), "Веб-поиск уже выполняется") {
		t.Fatalf("%v", gpt.bodies[0])
	}
	c, _ := g.r.Chats.Get(g.chat.ID)
	if m := c.Messages[len(c.Messages)-1].Meta; len(m.Searches) != 1 || m.Searches[0] != "погода Казань" {
		t.Fatalf("%+v", m)
	}
}

// The owner's case: "последние новости по ИИ" found an encyclopedia article about a film. News
// are searched as news, among fresh pages; the planner says so, or the word itself does.
func TestNewsAreSearchedFresh(t *testing.T) {
	g := newRig(t)
	g.r.Config = config.Default
	var got []websearch.Query
	g.r.Search = func(_ context.Context, q websearch.Query) (websearch.Found, error) {
		got = append(got, q)
		return websearch.Found{Results: []websearch.Result{{Title: "Новости ИИ", URL: "https://news.example/ai", Snippet: "OpenAI выпустила"}}}, nil
	}
	g.replies = []string{
		sse(`{"choices":[{"delta":{"content":"{\"question\": \"Последние новости об ИИ\", \"queries\": [\"новости искусственный интеллект октябрь 2026\"], \"recency\": \"week\", \"news\": true}"}}]}`),
		sse(`{"choices":[{"delta":{"content":"Главное за неделю [1]."}}]}`),
	}
	if code, ev := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Найди последние новости по ИИ", Web: true}); code != 200 {
		t.Fatalf("%d %v", code, ev)
	}
	if len(got) != 1 || got[0].Recency != "week" || !got[0].News {
		t.Fatalf("planned %+v", got)
	}
	// A planner that leaves the fields out, or no planner at all: the word "новости" is enough.
	if p, ok := parsePlan(`{"question":"q","queries":["новости ИИ"],"recency":"forever"}`); !ok || p.Recency != "" {
		t.Fatalf("an unknown recency is dropped: %+v", p)
	}
	if r, news := freshness("Что нового? Дай новости ИИ"); r != "week" || !news {
		t.Fatalf("%q %v", r, news)
	}
	if r, news := freshness("Как включить WoL"); r != "" || news {
		t.Fatalf("%q %v", r, news)
	}
}

// "Из других источников" needs the sites of the last answer: the planner sees them.
func TestPlannerSeesTheLastAnswersSites(t *testing.T) {
	prompt := mustJSON(planPrompt([]chats.Message{
		{Role: "user", Text: "Новости ИИ"},
		{Role: "assistant", Text: "Вот новости [1].", Meta: &chats.Meta{Sources: []chats.Source{{URL: "https://www.habr.com/a"}, {URL: "https://habr.com/b"}, {URL: "https://3dnews.ru/c"}}}},
	}, chats.Message{Role: "user", Text: "Найди из других источников"}, time.Now()))
	if !strings.Contains(prompt, "(источники ответа: habr.com, 3dnews.ru)") {
		t.Fatal(prompt)
	}
}
