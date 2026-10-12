package chatrun

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func (g *rig) title() (int, map[string]any) {
	data, _ := json.Marshal(TitleRequest{ChatID: g.chat.ID})
	r := httptest.NewRequest(http.MethodPost, "/api/chat/title", bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.r.Title(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// After the first answer the model names the chat once; a chat the owner named keeps its name.
func TestModelNamesTheChat(t *testing.T) {
	g := newRig(t)
	if code, _ := g.title(); code != http.StatusConflict {
		t.Fatalf("before an answer: %d", code)
	}
	g.answered()
	g.replies = append(g.replies, sse(`{"choices":[{"delta":{"content":"<think>хм</think>**«Погода в Казани.»**\nлишнее"}}]}`))
	code, out := g.title()
	if code != 200 || out["title"] != "Погода в Казани" {
		t.Fatalf("%d %v", code, out)
	}
	if body := g.bodies[len(g.bodies)-1]; body["model"] != "a" {
		t.Fatalf("model: %v", body)
	}
	if code, _ := g.title(); code != http.StatusConflict {
		t.Fatalf("named twice: %d", code)
	}
	g2 := newRig(t)
	g2.answered()
	g2.r.Chats.Rename(g2.chat.ID, "Моё")
	if code, _ := g2.title(); code != http.StatusConflict {
		t.Fatalf("renamed by the owner: %d", code)
	}
}

func TestParseTitle(t *testing.T) {
	for in, want := range map[string]string{"Название: Сон и пробуждение ПК": "Сон и пробуждение ПК", "\n\n# Wake-on-LAN\n": "Wake-on-LAN", "\"Рецепт борща\".": "Рецепт борща", "": ""} {
		if got := parseTitle(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
