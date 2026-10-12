package chatrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/library"
	"github.com/aafnx/omnibox-ai/internal/openrouter"
	"github.com/aafnx/omnibox-ai/internal/reasoning"
)

func (g *rig) followUps(chatID, messageID string) (int, map[string]any) {
	data, _ := json.Marshal(FollowUpRequest{ChatID: chatID, MessageID: messageID})
	r := httptest.NewRequest(http.MethodPost, "/api/chat/followups", bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.r.FollowUps(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// answered sends one question to the local model and returns the stored answer's id.
func (g *rig) answered() string {
	g.replies = append(g.replies, sse(`{"choices":[{"delta":{"content":"В Казани +15°, ясно."}}]}`))
	if code, ev := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Какая погода в Казани?", Effort: "on"}); code != 200 || len(ev["done"]) != 1 {
		g.t.Fatalf("%d %v", code, ev)
	}
	c, _ := g.r.Chats.Get(g.chat.ID)
	return c.Messages[len(c.Messages)-1].ID
}

func withFollowUps(on bool, model string) func() *config.Config {
	return func() *config.Config {
		c := config.Default()
		s := config.DefaultChat()
		s.FollowUps, s.FollowUpModel = on, model
		c.Chat = &s
		return c
	}
}

func TestFollowUpsFromTheChatModel(t *testing.T) {
	g := newRig(t)
	g.r.Config = withFollowUps(true, "")
	id := g.answered()
	g.replies = append(g.replies, sse(`{"choices":[{"delta":{"content":"<think>коротко</think>{\"follow_ups\": [\"Что будет завтра?\", \"  Нужен ли зонт? \", \"что будет завтра?\", \"\"]}"}}]}`))
	code, out := g.followUps(g.chat.ID, id)
	if code != 200 || !reflect.DeepEqual(out["follow_ups"], []any{"Что будет завтра?", "Нужен ли зонт?"}) {
		t.Fatalf("%d %v", code, out)
	}
	// The same local model through the same queue, with its thinking off and only the end of the chat.
	body := g.bodies[len(g.bodies)-1]
	if body["model"] != "a" {
		t.Fatalf("model: %v", body)
	}
	if kw, _ := body["chat_template_kwargs"].(map[string]any); kw["enable_thinking"] != false {
		t.Fatalf("thinking not off: %v", body)
	}
	msgs := body["messages"].([]any)
	if len(msgs) != 2 || !strings.Contains(msgs[1].(map[string]any)["content"].(string), "Ассистент: В Казани +15°, ясно.") {
		t.Fatalf("prompt: %v", msgs)
	}
	c, _ := g.r.Chats.Get(g.chat.ID)
	if got := c.Messages[1].Meta.FollowUps; !reflect.DeepEqual(got, []string{"Что будет завтра?", "Нужен ли зонт?"}) {
		t.Fatalf("stored: %v", got)
	}
	// Only the last answer gets suggestions.
	if code, _ := g.followUps(g.chat.ID, c.Messages[0].ID); code != http.StatusConflict {
		t.Fatalf("not the last answer: %d", code)
	}
}

func TestFollowUpsAreOffUnlessChosen(t *testing.T) {
	g := newRig(t)
	id := g.answered()
	n := len(g.bodies)
	if code, out := g.followUps(g.chat.ID, id); code != http.StatusConflict || !strings.Contains(out["error"].(string), "выключены") {
		t.Fatalf("off by default: %d %v", code, out)
	}
	// The chat's own choice wins over the setting, both ways.
	on, off := true, false
	g.r.Chats.SetFollowUps(g.chat.ID, &on)
	g.replies = append(g.replies, sse(`{"choices":[{"delta":{"content":"- Что завтра?\n- Нужен ли зонт?"}}]}`))
	if code, out := g.followUps(g.chat.ID, id); code != 200 || len(out["follow_ups"].([]any)) != 2 {
		t.Fatalf("on in the chat: %d %v", code, out)
	}
	g.r.Config = withFollowUps(true, "")
	g.r.Chats.SetFollowUps(g.chat.ID, &off)
	if code, _ := g.followUps(g.chat.ID, id); code != http.StatusConflict {
		t.Fatalf("off in the chat: %d", code)
	}
	if len(g.bodies) != n+1 {
		t.Fatalf("a refused request reached the model: %d", len(g.bodies)-n)
	}
}

func TestFollowUpsNeverStartALocalModel(t *testing.T) {
	g := newRig(t)
	g.r.Config = withFollowUps(true, "")
	id := g.answered()
	stopped := local
	stopped.Running = false
	g.r.Model = func(ctx context.Context, key string) (library.Model, int, error) { return stopped, 32768, nil }
	n := len(g.bodies)
	if code, out := g.followUps(g.chat.ID, id); code != http.StatusConflict || !strings.Contains(out["error"].(string), "не запущена") {
		t.Fatalf("%d %v", code, out)
	}
	if len(g.bodies) != n {
		t.Fatal("the stopped model was asked")
	}
}

func TestFollowUpsFromACloudModelArePaid(t *testing.T) {
	g := newRig(t)
	g.r.Config = withFollowUps(true, "cloud:z-ai/glm-5.3-flash")
	id := g.answered()
	var got map[string]any
	or := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"{\"follow_ups\":[\"Что завтра?\"]}"}}]}`, `{"choices":[],"usage":{"prompt_tokens":300,"completion_tokens":20,"cost":0.00007}}`))
	}))
	defer or.Close()
	g.r.Cloud = func() (*openrouter.Client, error) {
		return &openrouter.Client{Key: "sk-or-v1-test", BaseURL: or.URL}, nil
	}
	g.r.Model = func(ctx context.Context, key string) (library.Model, int, error) {
		if key != "cloud:z-ai/glm-5.3-flash" {
			return library.Model{}, 0, errors.New("не та модель")
		}
		return library.Model{Key: key, Source: "cloud", Engine: "openrouter", ID: "z-ai/glm-5.3-flash", Name: "GLM", Reasoning: reasoning.Profile{Mode: reasoning.ModeNone}}, 200000, nil
	}
	code, out := g.followUps(g.chat.ID, id)
	if code != 200 || out["cost_usd"] != 0.00007 || got["model"] != "z-ai/glm-5.3-flash" {
		t.Fatalf("%d %v %v", code, out, got)
	}
	c, _ := g.r.Chats.Get(g.chat.ID)
	if c.CostUSD != 0.00007 || g.r.Spend.Days(1)[0].USD != 0.00007 {
		t.Fatalf("cost: %v %v", c.CostUSD, g.r.Spend.Days(1))
	}
}

func TestParseFollowUps(t *testing.T) {
	cases := map[string][]string{
		"```json\n{\"follow_ups\":[\"А\",\"Б\"]}\n```":           {"А", "Б"},
		"[\"Один\", \"Два\"]":                                    {"Один", "Два"},
		"1. Первый?\n2) Второй?\n• «Третий»":                     {"Первый?", "Второй?", "Третий"},
		"{\"follow_ups\":[\"1\",\"2\",\"3\",\"4\",\"5\",\"6\"]}": {"1", "2", "3", "4", "5"},
		"": nil,
	}
	for in, want := range cases {
		if got := parseFollowUps(in); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	if got := parseFollowUps(`{"follow_ups":["` + strings.Repeat("я", 400) + `"]}`); len([]rune(got[0])) != 200 {
		t.Errorf("long question not cut: %d", len([]rune(got[0])))
	}
}
