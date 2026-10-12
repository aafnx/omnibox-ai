package chatrun

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// slowModel answers through a pipe the test writes to, so an answer can be cut mid-way.
func slowModel(g *rig) *io.PipeWriter {
	pr, pw := io.Pipe()
	g.r.Local = func(ctx context.Context, session string, body []byte) *http.Response {
		go func() { <-ctx.Done(); pw.CloseWithError(ctx.Err()) }()
		return &http.Response{StatusCode: 200, Body: pr}
	}
	return pw
}

func (g *rig) waitIdle() {
	g.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); g.r.jobOf(g.chat.ID) != nil; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			g.t.Fatal("the answer did not end")
		}
	}
}

func (g *rig) startSend(ctx context.Context, text string) {
	data, _ := json.Marshal(Request{ChatID: g.chat.ID, Model: local.Key, Text: text, Effort: "off"})
	r := httptest.NewRequest(http.MethodPost, "/api/chat/send", bytes.NewReader(data)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	go g.r.ServeHTTP(httptest.NewRecorder(), r)
	for deadline := time.Now().Add(5 * time.Second); g.r.jobOf(g.chat.ID) == nil; time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			g.t.Fatal("no answer started")
		}
	}
}

// Closing the tab does not stop the model: the answer is written to the end and stored.
func TestAnswerGoesOnWithoutTheBrowser(t *testing.T) {
	g := newRig(t)
	pw := slowModel(g)
	ctx, leave := context.WithCancel(context.Background())
	g.startSend(ctx, "Длинный вопрос")
	io.WriteString(pw, "data: {\"choices\":[{\"delta\":{\"content\":\"Нача\"}}]}\n\n")
	leave()
	time.Sleep(20 * time.Millisecond)
	// Another browser follows the answer from its start.
	follow := httptest.NewRecorder()
	followed := make(chan struct{})
	go func() {
		g.r.Follow(follow, httptest.NewRequest(http.MethodGet, "/api/chat/stream?chat_id="+g.chat.ID, nil))
		close(followed)
	}()
	io.WriteString(pw, "data: {\"choices\":[{\"delta\":{\"content\":\"ло и конец\"}}]}\n\ndata: [DONE]\n\n")
	pw.Close()
	g.waitIdle()
	<-followed
	c, _ := g.r.Chats.Get(g.chat.ID)
	if len(c.Messages) != 2 || c.Messages[1].Text != "Начало и конец" || c.Messages[1].Error != "" {
		t.Fatalf("stored: %+v", c.Messages)
	}
	if got := follow.Body.String(); !strings.Contains(got, "event: user") || !strings.Contains(got, `"t":"Нача"`) || !strings.Contains(got, "event: done") {
		t.Fatalf("followed: %s", got)
	}
	// Nothing to follow now: the chat is read as stored.
	if w := httptest.NewRecorder(); func() int {
		g.r.Follow(w, httptest.NewRequest(http.MethodGet, "/api/chat/stream?chat_id="+g.chat.ID, nil))
		return w.Code
	}() != http.StatusNoContent {
		t.Fatal("a finished answer is followed")
	}
}

// Only the stop button stops the model; a second message waits for the answer.
func TestStopAndOneAnswerAtATime(t *testing.T) {
	g := newRig(t)
	pw := slowModel(g)
	g.startSend(context.Background(), "Вопрос")
	io.WriteString(pw, "data: {\"choices\":[{\"delta\":{\"content\":\"Часть\"}}]}\n\n")
	if code, ev := g.send(Request{ChatID: g.chat.ID, Model: local.Key, Text: "Ещё", Effort: "off"}); code != http.StatusConflict || !strings.Contains(ev["error"][0], "ещё отвечает") {
		t.Fatalf("a second message: %d %v", code, ev)
	}
	active := httptest.NewRecorder()
	g.r.Active(active, httptest.NewRequest(http.MethodGet, "/api/chat/active", nil))
	if !strings.Contains(active.Body.String(), g.chat.ID) {
		t.Fatalf("active: %s", active.Body)
	}
	stop := httptest.NewRecorder()
	g.r.Stop(stop, httptest.NewRequest(http.MethodPost, "/api/chat/stop", strings.NewReader(`{"chat_id":"`+g.chat.ID+`"}`)))
	if !strings.Contains(stop.Body.String(), `"stopped":true`) {
		t.Fatalf("stop: %s", stop.Body)
	}
	g.waitIdle()
	c, _ := g.r.Chats.Get(g.chat.ID)
	if len(c.Messages) != 2 || c.Messages[1].Text != "Часть" || c.Messages[1].Error != "Ответ остановлен." {
		t.Fatalf("stopped answer: %+v", c.Messages)
	}
}
