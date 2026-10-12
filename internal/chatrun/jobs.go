package chatrun

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"
)

// An answer is written by a job of the service, not by the browser's request: leaving the chat,
// another tab, a phone that suspends the page or a reload do not stop the model — only the stop
// button does (or the time limit). A browser that comes back follows the job again from its start.
const (
	jobTimeout = 30 * time.Minute
	maxJobs    = 8
)

// job is one answer being written: its events so far, replayed to whoever follows it.
type job struct {
	chat   string
	cancel context.CancelFunc
	mu     sync.Mutex
	log    [][]byte
	done   bool
	wake   chan struct{}
}

func (j *job) publish(event []byte) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.log = append(j.log, event)
	close(j.wake)
	j.wake = make(chan struct{})
}

func (j *job) finish() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.done = true
	close(j.wake)
	j.wake = make(chan struct{})
}

// follow writes the job's events to one browser until the job ends or the browser goes.
func (j *job) follow(ctx context.Context, w http.ResponseWriter) {
	flush := http.NewResponseController(w)
	for i := 0; ; {
		j.mu.Lock()
		batch, done, wake := j.log[i:], j.done, j.wake
		i = len(j.log)
		j.mu.Unlock()
		for _, e := range batch {
			if _, err := w.Write(e); err != nil {
				return
			}
		}
		_ = flush.Flush()
		if done {
			return
		}
		select {
		case <-wake:
		case <-ctx.Done():
			return
		}
	}
}

// reserve claims the chat for a new answer; a chat answers one message at a time.
func (r *Runner) reserve(chatID string) (*job, error) {
	r.jobsMu.Lock()
	defer r.jobsMu.Unlock()
	if r.jobs == nil {
		r.jobs = map[string]*job{}
	}
	if _, busy := r.jobs[chatID]; busy {
		return nil, refuse(http.StatusConflict, "В этом чате модель ещё отвечает: дождитесь ответа или остановите его.")
	}
	if len(r.jobs) >= maxJobs {
		return nil, refuse(http.StatusTooManyRequests, "Слишком много ответов пишется сразу. Дождитесь одного из них.")
	}
	j := &job{chat: chatID, wake: make(chan struct{})}
	r.jobs[chatID] = j
	return j, nil
}

func (r *Runner) release(j *job) {
	r.jobsMu.Lock()
	defer r.jobsMu.Unlock()
	if r.jobs[j.chat] == j {
		delete(r.jobs, j.chat)
	}
}

func (r *Runner) jobOf(chatID string) *job {
	r.jobsMu.Lock()
	defer r.jobsMu.Unlock()
	return r.jobs[chatID]
}

// Active is GET /api/chat/active: the chats whose answer is being written now.
func (r *Runner) Active(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Метод запрещён.")
		return
	}
	r.jobsMu.Lock()
	ids := make([]string, 0, len(r.jobs))
	for id := range r.jobs {
		ids = append(ids, id)
	}
	r.jobsMu.Unlock()
	slices.Sort(ids)
	writeJSON(w, map[string]any{"chats": ids})
}

// Follow is GET /api/chat/stream?chat_id=…: the answer being written, from its start. Without
// one the answer is 204, and the browser reads the chat as stored.
func (r *Runner) Follow(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "Метод запрещён.")
		return
	}
	j := r.jobOf(req.URL.Query().Get("chat_id"))
	if j == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	sseHeaders(w)
	j.follow(req.Context(), w)
}

// Stop is POST /api/chat/stop {"chat_id"}: the model stops, and the answer so far is stored.
func (r *Runner) Stop(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Метод запрещён.")
		return
	}
	var in struct {
		ChatID string `json:"chat_id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, req.Body, 4<<10)).Decode(&in) != nil {
		writeError(w, http.StatusBadRequest, "Некорректный запрос.")
		return
	}
	j := r.jobOf(in.ChatID)
	if j != nil {
		j.cancel()
	}
	writeJSON(w, map[string]any{"stopped": j != nil})
}

func sseHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, v any) {
	body, _ := json.Marshal(v)
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// event is one server-sent event as written to the browser.
func event(name string, v any) []byte {
	data, _ := json.Marshal(v)
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", name, data))
}
