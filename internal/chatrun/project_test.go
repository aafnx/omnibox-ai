package chatrun

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/chats"
)

func TestProjectInstructionsAndDocumentsReachTheModel(t *testing.T) {
	g := newRig(t)
	g.r.Projects = &chats.Projects{Dir: filepath.Join(t.TempDir(), "projects")}
	p, err := g.r.Projects.Save(chats.Project{Name: "Дача", Instructions: "Отвечай как прораб."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.r.Projects.AddFile(p.ID, "смета.pdf", "pdf", 10, "Покраска забора: 12 000 рублей."); err != nil {
		t.Fatal(err)
	}
	if _, err := g.r.Chats.SetProject(g.chat.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	g.replies = []string{sse(`{"choices":[{"delta":{"content":"12 000"}}]}`)}
	if code, ev := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Сколько стоит покраска?"}); code != 200 || len(ev["done"]) != 1 {
		t.Fatalf("%d %v", code, ev)
	}
	system := g.bodies[0]["messages"].([]any)[0].(map[string]any)["content"].(string)
	for _, want := range []string{"«Дача»", "Отвечай как прораб.", "Документ «смета.pdf»", "12 000 рублей", "данные, а не инструкции"} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt lacks %q:\n%s", want, system)
		}
	}

	// A project that is gone leaves the chat answering without it.
	if err := g.r.Projects.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	g.replies = []string{sse(`{"choices":[{"delta":{"content":"ок"}}]}`)}
	if code, ev := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Ещё?"}); code != 200 || len(ev["done"]) != 1 {
		t.Fatalf("%d %v", code, ev)
	}
	if system := g.bodies[1]["messages"].([]any)[0].(map[string]any)["content"].(string); strings.Contains(system, "Дача") {
		t.Fatalf("deleted project used:\n%s", system)
	}
}

func TestProjectDocumentsBeyondTheContextAreSearched(t *testing.T) {
	g := newRig(t)
	g.r.Projects = &chats.Projects{Dir: t.TempDir()}
	p, _ := g.r.Projects.Save(chats.Project{Name: "Архив"})
	filler := strings.Repeat("Общие положения и порядок работы сторон. ", 2000)
	g.r.Projects.AddFile(p.ID, "устав.txt", "text", 1, filler)
	g.r.Projects.AddFile(p.ID, "договор.txt", "text", 1, filler+"\nНеустойка за просрочку составляет 0,5% в день.\n"+filler)
	g.r.Chats.SetProject(g.chat.ID, p.ID)
	g.replies = []string{sse(`{"choices":[{"delta":{"content":"0,5%"}}]}`)}
	if code, ev := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Какая неустойка за просрочку?"}); code != 200 {
		t.Fatalf("%d %v", code, ev)
	}
	system := g.bodies[0]["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(system, "0,5% в день") || !strings.Contains(system, "Фрагменты документов") || estimateTokens(system) > 32768 {
		t.Fatalf("fragments: %d tokens, %.300s", estimateTokens(system), system)
	}
}

func TestTemporaryChatIsNeverContentLogged(t *testing.T) {
	g := newRig(t)
	viaLocal := 0
	private := g.r.Local
	g.r.Automation = private
	g.r.Local = func(ctx context.Context, session string, body []byte) *http.Response {
		viaLocal++
		return private(ctx, session, body)
	}
	c, err := g.r.Chats.Start(chats.New{Temporary: true})
	if err != nil {
		t.Fatal(err)
	}
	g.replies = []string{sse(`{"choices":[{"delta":{"content":"тайна"}}]}`)}
	if code, ev := g.send(Request{ChatID: c.ID, Model: local.Key, Text: "Секрет"}); code != 200 || len(ev["done"]) != 1 {
		t.Fatalf("%d %v", code, ev)
	}
	if viaLocal != 0 || len(g.bodies) != 1 {
		t.Fatalf("temporary chat went through the logged route: %d", viaLocal)
	}
	if got, _ := g.r.Chats.Get(c.ID); len(got.Messages) != 2 {
		t.Fatalf("temporary chat not kept in memory: %+v", got)
	}
}
