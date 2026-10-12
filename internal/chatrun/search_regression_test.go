package chatrun

import (
	"context"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/websearch"
)

func TestTextualBrowserCallIsRecoveredWithoutLeakingMarkup(t *testing.T) {
	g := newRig(t)
	g.replies = []string{
		sse(`{"choices":[{"delta":{"content":"<tool_"}}]}`, `{"choices":[{"delta":{"content":"call><function=browser_navigate><parameter=url>https://gismeteo.ru</parameter></function></tool_call>"}}]}`),
		sse(`{"choices":[{"delta":{"content":"В Казани +8 °C [1]."}}]}`),
	}
	queries := []string{}
	g.r.Search = func(_ context.Context, q string) (websearch.Found, error) {
		queries = append(queries, q)
		return websearch.Found{Results: []websearch.Result{{Title: "Погода в Казани", URL: "https://weather.example/kazan", Snippet: "Сегодня +8 °C"}}}, nil
	}
	code, events := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Какая сегодня погода в Казани", Web: true})
	if code != 200 || len(g.bodies) != 2 || len(queries) != 1 || queries[0] != "сегодня погода Казани" {
		t.Fatalf("status %d, requests %d, queries %v", code, len(g.bodies), queries)
	}
	for _, event := range events["delta"] {
		if strings.Contains(event, "tool_") || strings.Contains(event, "browser_navigate") {
			t.Fatalf("tool syntax leaked to the stream: %s", event)
		}
	}
	c, _ := g.r.Chats.Get(g.chat.ID)
	if c.Messages[1].Text != "В Казани +8 °C [1]." || c.Messages[1].Error != "" || g.bodies[1]["tools"] != nil {
		t.Fatalf("answer %+v, retry %v", c.Messages[1], g.bodies[1])
	}
}

func TestUnsupportedStructuredToolGetsFinalAnswerInsteadOfBeingIgnored(t *testing.T) {
	g := newRig(t)
	g.replies = []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"browser_navigate","arguments":"{\"url\":\"https://example.com\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"Нет точных данных в найденных источниках."}}]}`),
	}
	code, _ := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Погода в Казани", Web: true})
	if code != 200 || len(g.bodies) != 2 || g.bodies[1]["tools"] != nil {
		t.Fatalf("status %d, requests %v", code, g.bodies)
	}
	c, _ := g.r.Chats.Get(g.chat.ID)
	if !strings.Contains(c.Messages[1].Text, "Нет точных") {
		t.Fatalf("unsupported tool ended the answer: %+v", c.Messages[1])
	}
}

func TestRepeatedMalformedToolProducesReadableError(t *testing.T) {
	g := newRig(t)
	bad := sse(`{"choices":[{"delta":{"content":"<tool_call><function=browser_navigate>"}}]}`)
	g.replies = []string{bad, bad}
	g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Погода в Казани", Web: true})
	c, _ := g.r.Chats.Get(g.chat.ID)
	if c.Messages[1].Text != "" || !strings.Contains(c.Messages[1].Error, "вместо ответа") || len(g.bodies) != 2 {
		t.Fatalf("%+v", c.Messages[1])
	}
}

func TestToolTextFilterKeepsNormalTextAcrossChunks(t *testing.T) {
	f := &toolTextFilter{}
	got := f.split("Ответ: 2 < 3.\n") + f.split("Продолжение <to") + f.split("day>.") + f.pending
	if got != "Ответ: 2 < 3.\nПродолжение <today>." || f.blocked {
		t.Fatalf("ordinary text changed: %q", got)
	}
}

func TestToolQueryCannotReplaceTheRequestedCity(t *testing.T) {
	g := newRig(t)
	g.replies = []string{
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"web_search","arguments":"{\"query\":\"погода Кохем\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{"content":"Погода в Казани [1]."}}]}`),
	}
	g.r.Search = func(_ context.Context, q string) (websearch.Found, error) {
		if !strings.Contains(q, "Казани") {
			t.Errorf("query lost the city: %q", q)
		}
		return websearch.Found{Results: []websearch.Result{
			{Title: "Погода в Кохеме", URL: "https://wrong.example", Snippet: "Температура"},
			{Title: "Погода в Казани", URL: "https://right.example", Snippet: "Казань"},
		}}, nil
	}
	g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Какая сегодня погода в Казани", Web: true})
	c, _ := g.r.Chats.Get(g.chat.ID)
	if sources := c.Messages[1].Meta.Sources; len(sources) != 1 || sources[0].URL != "https://right.example" {
		t.Fatalf("model's wrong city entered sources: %+v", sources)
	}
}

// The owner's case: a local model wrote its tool call as text after a search had already found
// sources. The retry tells it plainly there are no tools, and the model gets no tools again.
func TestTextToolCallAfterSearchIsRetriedWithoutToolsAndRemembered(t *testing.T) {
	g := newRig(t)
	g.replies = []string{
		sse(`{"choices":[{"delta":{"content":"<tool_call>{\"name\": \"web_search\", \"arguments\": {\"query\": \"погода Казань\"}}</tool_call>"}}]}`),
		sse(`{"choices":[{"delta":{"content":"В Казани сегодня +8 °C [1]."}}]}`),
		sse(`{"choices":[{"delta":{"content":"Завтра +6 °C [1]."}}]}`),
	}
	code, _ := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Какая сегодня погода в Казани?", Web: true})
	c, _ := g.r.Chats.Get(g.chat.ID)
	if code != 200 || c.Messages[1].Error != "" || c.Messages[1].Text != "В Казани сегодня +8 °C [1]." {
		t.Fatalf("%d %+v", code, c.Messages[1])
	}
	retry := g.bodies[1]["messages"].([]any)[0].(map[string]any)["content"].(string)
	if strings.Contains(retry, toolsNote) || !strings.Contains(retry, plainNote) || g.bodies[1]["tools"] != nil {
		t.Fatalf("the retry still speaks of tools: %s", retry)
	}
	g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "А завтра?", Web: true})
	next := g.bodies[2]
	if next["tools"] != nil || !strings.Contains(next["messages"].([]any)[0].(map[string]any)["content"].(string), plainNote) {
		t.Fatalf("a model that wrote calls as text is offered tools again: %v", next["tools"])
	}
}

// Text the model wrote before a stray command is kept when it already answers the question.
func TestAnswerBeforeStrayCommandIsKept(t *testing.T) {
	g := newRig(t)
	long := strings.Repeat("Погода в Казани сегодня ясная, днём до +8 °C, ночью около 0 °C [1]. ", 4)
	g.replies = []string{sse(`{"choices":[{"delta":{"content":"` + long + `<tool_call>{\"name\":\"web_search\"}</tool_call>"}}]}`)}
	if code, _ := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Погода в Казани", Web: true}); code != 200 {
		t.Fatal(code)
	}
	c, _ := g.r.Chats.Get(g.chat.ID)
	if len(g.bodies) != 1 || c.Messages[1].Error != "" || !strings.HasPrefix(c.Messages[1].Text, "Погода в Казани") || strings.Contains(c.Messages[1].Text, "tool_call") {
		t.Fatalf("requests %d, %+v", len(g.bodies), c.Messages[1])
	}
}
