package chats

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func newHandler(t *testing.T) (*Handler, *clock) {
	t.Helper()
	s, clk := newStore(t)
	return &Handler{Chats: s, Assistants: &Assistants{Path: filepath.Join(t.TempDir(), "assistants.json")}, KeepHours: func() int { return 72 }}, clk
}

func serve(t *testing.T, h *Handler, method, target, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Content-Type") != "application/json; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%s %s: headers %v", method, target, w.Header())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: %v %s", method, target, err, w.Body)
	}
	return w.Code, out
}

func TestHandlerChats(t *testing.T) {
	h, _ := newHandler(t)
	code, c := serve(t, h, "POST", "/api/chats", `{"action":"create","title":"Первый","model":"local:prismml:a.gguf"}`)
	if code != 200 || c["title"] != "Первый" || c["keep_hours"] != 72.0 || c["model"] != "local:prismml:a.gguf" {
		t.Fatalf("create %d %v", code, c)
	}
	if _, ok := c["messages"].([]any); !ok {
		t.Fatalf("messages must be an array: %v", c)
	}
	id := c["id"].(string)

	code, list := serve(t, h, "GET", "/api/chats", "")
	chats, _ := list["chats"].([]any)
	if code != 200 || len(chats) != 1 || list["keep_hours"] != 72.0 {
		t.Fatalf("list %d %v", code, list)
	}
	sum := chats[0].(map[string]any)
	for _, k := range []string{"id", "title", "created", "updated", "pinned", "keep_hours", "expires", "assistant_id", "model", "messages", "cost_usd"} {
		if _, ok := sum[k]; !ok {
			t.Fatalf("summary lacks %s: %v", k, sum)
		}
	}

	if code, got := serve(t, h, "GET", "/api/chats?id="+id, ""); code != 200 || got["id"] != id {
		t.Fatalf("get %d %v", code, got)
	}
	if code, got := serve(t, h, "GET", "/api/chats?id=../x", ""); code != 404 || got["error"] != "Чат не найден." {
		t.Fatalf("get bad id %d %v", code, got)
	}
	if code, got := serve(t, h, "GET", "/api/chats?q="+url.QueryEscape("перв"), ""); code != 200 || len(got["hits"].([]any)) != 1 {
		t.Fatalf("search %d %v", code, got)
	}
	if code, _ := serve(t, h, "GET", "/api/chats?x=1", ""); code != 400 {
		t.Fatalf("unknown query %d", code)
	}
	if code, _ := serve(t, h, "GET", "/api/chats?id="+id+"&q=x", ""); code != 400 {
		t.Fatalf("id and q %d", code)
	}

	if code, got := serve(t, h, "POST", "/api/chats", `{"action":"rename","id":"`+id+`","title":"Второй"}`); code != 200 || got["title"] != "Второй" {
		t.Fatalf("rename %d %v", code, got)
	}
	if code, got := serve(t, h, "POST", "/api/chats", `{"action":"pin","id":"`+id+`"}`); code != 200 || got["pinned"] != true || got["expires"] != nil {
		t.Fatalf("pin %d %v", code, got)
	}
	if code, got := serve(t, h, "POST", "/api/chats", `{"action":"unpin","id":"`+id+`"}`); code != 200 || got["pinned"] != false || got["expires"] == nil {
		t.Fatalf("unpin %d %v", code, got)
	}

	chat, _ := h.Chats.Get(id)
	chat.Messages = []Message{{Role: "user", Text: "x"}, {Role: "assistant", Text: "y"}}
	if err := h.Chats.Save(chat); err != nil {
		t.Fatal(err)
	}
	code, got := serve(t, h, "POST", "/api/chats", `{"action":"delete-message","id":"`+id+`","message_id":"`+chat.Messages[0].ID+`"}`)
	if code != 200 || got["messages"] != 1.0 {
		t.Fatalf("delete-message %d %v", code, got)
	}
	if code, _ := serve(t, h, "POST", "/api/chats", `{"action":"delete-message","id":"`+id+`","message_id":"000000000000"}`); code != 404 {
		t.Fatalf("delete missing message %d", code)
	}

	if code, got := serve(t, h, "POST", "/api/chats", `{"action":"delete","id":"`+id+`"}`); code != 200 || got["ok"] != true {
		t.Fatalf("delete %d %v", code, got)
	}
	if code, _ := serve(t, h, "POST", "/api/chats", `{"action":"delete","id":"`+id+`"}`); code != 404 {
		t.Fatalf("delete twice %d", code)
	}

	serve(t, h, "POST", "/api/chats", `{"action":"create"}`)
	_, p := serve(t, h, "POST", "/api/chats", `{"action":"create"}`)
	serve(t, h, "POST", "/api/chats", `{"action":"pin","id":"`+p["id"].(string)+`"}`)
	if code, got := serve(t, h, "POST", "/api/chats", `{"action":"delete-all","keep_pinned":true}`); code != 200 || got["deleted"] != 1.0 {
		t.Fatalf("delete-all keep pinned %d %v", code, got)
	}
	if code, got := serve(t, h, "POST", "/api/chats", `{"action":"delete-all"}`); code != 200 || got["deleted"] != 1.0 {
		t.Fatalf("delete-all %d %v", code, got)
	}
}

func TestHandlerRejects(t *testing.T) {
	h, _ := newHandler(t)
	for _, tc := range []struct {
		method, target, body string
		code                 int
	}{
		{"GET", "/api/other", "", 404},
		{"GET", "/api/chats/x", "", 404},
		{"DELETE", "/api/chats", "", 405},
		{"PUT", "/api/assistants", "", 405},
		{"POST", "/api/chats", `[]`, 400},
		{"POST", "/api/chats", `{"title":"x"}`, 400},
		{"POST", "/api/chats", `{"action":"explode"}`, 400},
		{"POST", "/api/chats", `{"action":"create","extra":1}`, 400},
		{"POST", "/api/chats", `{"action":"create","keep_pinned":true}`, 400},
		{"POST", "/api/chats", `{"action":"create","title":5}`, 400},
		{"POST", "/api/chats", `{"action":"create","title":null}`, 400},
		{"POST", "/api/chats", `{"action":"delete-all","keep_pinned":"yes"}`, 400},
		{"POST", "/api/chats", `{"action":"create","title":"` + strings.Repeat("я", MaxTitle+1) + `"}`, 400},
		{"POST", "/api/chats", `{"action":"create","title":"` + strings.Repeat("a", maxBody) + `"}`, 400},
		{"POST", "/api/chats", `{"action":"create","assistant_id":"0123456789abcdef01234567"}`, 404},
		{"POST", "/api/assistants", `{"action":"save","name":""}`, 400},
		{"POST", "/api/assistants", `{"action":"save","name":"x","web_search":"on"}`, 400},
		{"POST", "/api/assistants", `{"action":"delete","id":"0123456789abcdef01234567"}`, 404},
		{"POST", "/api/assistants", `{"action":"default","name":"x"}`, 400},
		{"GET", "/api/assistants?x=1", "", 400},
	} {
		code, got := serve(t, h, tc.method, tc.target, tc.body)
		msg, _ := got["error"].(string)
		if code != tc.code || msg == "" {
			t.Errorf("%s %s %.60s: %d %v", tc.method, tc.target, tc.body, code, got)
		}
	}
	if _, got := serve(t, h, "GET", "/api/nothing", ""); got["error"] != "Не найдено." {
		t.Fatalf("404 message %v", got)
	}
	r := httptest.NewRequest("POST", "/api/chats", strings.NewReader(`{"action":"create"}`))
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("text/plain %d", w.Code)
	}
}

func TestHandlerAssistants(t *testing.T) {
	h, _ := newHandler(t)
	code, got := serve(t, h, "GET", "/api/assistants", "")
	if code != 200 || len(got["assistants"].([]any)) != 0 || got["default"] != "" {
		t.Fatalf("empty %d %v", code, got)
	}
	code, got = serve(t, h, "POST", "/api/assistants", `{"action":"save","emoji":"🧑‍🍳","name":"Повар","description":"Рецепты","instructions":"Отвечай рецептами.","model":"cloud:x/y","style":"balanced","web_search":true}`)
	if code != 200 || len(got["assistants"].([]any)) != 1 {
		t.Fatalf("save %d %v", code, got)
	}
	a := got["assistant"].(map[string]any)
	id := a["id"].(string)
	if a["name"] != "Повар" || a["web_search"] != true || a["style"] != "balanced" {
		t.Fatalf("assistant %v", a)
	}

	// A new chat starts with the default assistant unless the request names one, or none.
	if code, got = serve(t, h, "POST", "/api/assistants", `{"action":"default","id":"`+id+`"}`); code != 200 || got["default"] != id {
		t.Fatalf("default %d %v", code, got)
	}
	if _, c := serve(t, h, "POST", "/api/chats", `{"action":"create"}`); c["assistant_id"] != id {
		t.Fatalf("default assistant not applied: %v", c)
	}
	if _, c := serve(t, h, "POST", "/api/chats", `{"action":"create","assistant_id":""}`); c["assistant_id"] != "" {
		t.Fatalf("explicit none: %v", c)
	}

	if code, got = serve(t, h, "POST", "/api/assistants", `{"action":"save","id":"`+id+`","name":"Шеф"}`); code != 200 || got["assistant"].(map[string]any)["name"] != "Шеф" || got["default"] != id {
		t.Fatalf("edit %d %v", code, got)
	}
	if code, got = serve(t, h, "POST", "/api/assistants", `{"action":"delete","id":"`+id+`"}`); code != 200 || got["default"] != "" || len(got["assistants"].([]any)) != 0 {
		t.Fatalf("delete %d %v", code, got)
	}
	if _, ok := got["assistant"]; ok {
		t.Fatal("delete answers with the list only")
	}
}
