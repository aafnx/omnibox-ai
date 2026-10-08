// Package chatrun answers a message of the panel's chat: it builds the conversation for the
// model (assistant instructions, history, attached files, web search), sends it to the running
// local model through /v1 — with its queue — or to OpenRouter, streams the answer to the browser
// and stores both messages in the chat.
package chatrun

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aafnx/omnibox-ai/internal/attach"
	"github.com/aafnx/omnibox-ai/internal/chats"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/library"
	"github.com/aafnx/omnibox-ai/internal/openrouter"
	"github.com/aafnx/omnibox-ai/internal/reasoning"
	"github.com/aafnx/omnibox-ai/internal/websearch"
)

// Limits of one message.
const (
	MaxBody     = 120 << 20 // base64 of a few 25 MB files
	MaxFiles    = 10
	MaxText     = 100_000
	maxRounds   = 3 // model turns with web searches before the final answer
	maxSearches = 4
	// answerReserve is the context kept for the answer when old messages are dropped.
	answerReserve = 4096
)

// Runner answers chat messages.
type Runner struct {
	Chats      *chats.Store
	Assistants *chats.Assistants
	Config     func() *config.Config
	// Model describes a model by key; Context is the running context of a local model.
	Model func(ctx context.Context, key string) (library.Model, int, error)
	// Local sends a request to the running local model through /v1 and its queue.
	Local func(ctx context.Context, session string, body []byte) *http.Response
	// Cloud returns the OpenRouter client with the saved key.
	Cloud func() (*openrouter.Client, error)
	Spend *openrouter.Spend
	// Search asks SearXNG.
	Search func(ctx context.Context, query string) ([]websearch.Result, error)
	Now    func() time.Time
	Logf   func(format string, args ...any)

	// learned are parameters OpenRouter refused for a model; they are not sent again.
	learned sync.Map
}

// File is an attached file as the browser sends it.
type File struct {
	Name string `json:"name"`
	Data string `json:"data"` // base64
}

// Request is one message.
type Request struct {
	ChatID string `json:"chat_id"`
	Model  string `json:"model"`
	Text   string `json:"text"`
	Files  []File `json:"files"`
	Web    bool   `json:"web"`
	// Effort is the step of the reasoning scale (off, on, low…); empty is the model's default.
	Effort string `json:"effort"`
	// Assistant switches the chat's assistant; nil keeps it, "" means none.
	Assistant *string `json:"assistant_id"`
	// Retry answers an earlier user message again: what followed it is replaced.
	Retry string `json:"retry"`
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// problem is a refusal the owner can act on: shown as is, before anything is spent.
type problem struct {
	status  int
	message string
}

func (p *problem) Error() string { return p.message }

func refuse(status int, format string, args ...any) error {
	return &problem{status, fmt.Sprintf(format, args...)}
}

// ServeHTTP is POST /api/chat/send; the panel has checked the session, CSRF and Origin.
func (r *Runner) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Метод запрещён.")
		return
	}
	if !strings.HasPrefix(strings.ToLower(req.Header.Get("Content-Type")), "application/json") || req.ContentLength > MaxBody {
		writeError(w, http.StatusBadRequest, "Нужен JSON-запрос; файлы — до 25 МБ каждый.")
		return
	}
	var in Request
	dec := json.NewDecoder(io.LimitReader(req.Body, MaxBody+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "Некорректный запрос сообщения.")
		return
	}
	turn, err := r.prepare(req.Context(), in)
	if err != nil {
		var p *problem
		if errors.As(err, &p) {
			writeError(w, p.status, p.message)
			return
		}
		if errors.Is(err, chats.ErrNotFound) {
			writeError(w, http.StatusNotFound, "Чат не найден: возможно, он удалён.")
			return
		}
		r.logf("chat: %v", err)
		writeError(w, http.StatusInternalServerError, "Сообщение не сохранено. Подробности — в журнале панели.")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	out := &stream{w: w, flush: http.NewResponseController(w)}
	r.run(req.Context(), turn, out)
}

func writeError(w http.ResponseWriter, status int, message string) {
	body, _ := json.Marshal(map[string]string{"error": message})
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// stream writes server-sent events to the browser; a closed tab only stops the writing.
type stream struct {
	w     io.Writer
	flush *http.ResponseController
	mu    sync.Mutex
	gone  bool
}

func (s *stream) send(event string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gone {
		return
	}
	data, _ := json.Marshal(v)
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		s.gone = true
		return
	}
	_ = s.flush.Flush()
}

// turn is a prepared message: everything checked, the user's message stored.
type turn struct {
	chat      *chats.Chat
	user      chats.Message
	history   []chats.Message
	model     library.Model
	context   int
	assistant *chats.Assistant
	media     []map[string]any // image, audio and video parts of this message only
	web       bool
	effort    string
}

func (r *Runner) prepare(ctx context.Context, in Request) (*turn, error) {
	text := strings.TrimSpace(in.Text)
	if utf8.RuneCountInString(text) > MaxText {
		return nil, refuse(400, "Сообщение длиннее %d символов: приложите его файлом.", MaxText)
	}
	if len(in.Files) > MaxFiles {
		return nil, refuse(400, "Не больше %d файлов в одном сообщении.", MaxFiles)
	}
	if text == "" && len(in.Files) == 0 && in.Retry == "" {
		return nil, refuse(400, "Пустое сообщение.")
	}
	chat, err := r.Chats.Get(in.ChatID)
	if err != nil {
		return nil, err
	}
	model, ctxLen, err := r.Model(ctx, in.Model)
	if err != nil {
		return nil, refuse(409, "%s", err.Error())
	}
	if model.Source == "local" && !model.Running {
		return nil, refuse(409, "Модель %s не запущена. Выберите её в списке моделей чата — она запустится.", model.Name)
	}
	t := &turn{chat: chat, model: model, context: ctxLen, web: in.Web, effort: reasoning.Choose(model.Reasoning, in.Effort)}
	assistantID := chat.AssistantID
	if in.Assistant != nil {
		assistantID = *in.Assistant
	}
	if assistantID != "" && r.Assistants != nil {
		list, err := r.Assistants.Load()
		if err != nil {
			return nil, err
		}
		if a, ok := list.Find(assistantID); ok {
			t.assistant = &a
		} else {
			assistantID = ""
		}
	}
	// Files are checked before anything is stored or sent: a file the model cannot read is
	// refused, so no tokens are spent on it.
	var attachments []chats.Attachment
	for _, f := range in.Files {
		kind := attach.KindOf(f.Name)
		if kind == "" {
			return nil, refuse(400, "«%s»: такой тип файла не поддерживается. Можно: изображения, видео, аудио, текст, Word, Excel.", f.Name)
		}
		if base64.StdEncoding.DecodedLen(len(f.Data)) > attach.MaxFile+3 {
			return nil, refuse(400, "«%s»: больше 25 МБ.", f.Name)
		}
		if need := attach.Needs(kind); !understands(model, need) {
			return nil, refuse(400, "%s не понимает %s — «%s» не отправлен.", model.Name, kindName(kind), f.Name)
		}
		data, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil || len(data) > attach.MaxFile {
			return nil, refuse(400, "«%s» не прочитан или больше 25 МБ.", f.Name)
		}
		a := chats.Attachment{Name: clip(f.Name, 200), Kind: kind, Size: int64(len(data))}
		switch kind {
		case attach.Image:
			a.Tokens = 1500
			t.media = append(t.media, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + attach.MIME(f.Name) + ";base64," + f.Data}})
		case attach.Audio:
			t.media = append(t.media, map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": f.Data, "format": attach.AudioFormat(f.Name)}})
		case attach.Video:
			t.media = append(t.media, map[string]any{"type": "video_url", "video_url": map[string]any{"url": "data:" + attach.MIME(f.Name) + ";base64," + f.Data}})
		default:
			body, err := attach.ExtractText(f.Name, data)
			if err != nil {
				return nil, refuse(400, "%s", err.Error())
			}
			a.Text, a.Tokens = body, estimateTokens(body)
		}
		attachments = append(attachments, a)
	}
	// Store the question first: it stays in the chat even if the answer fails.
	var stored *chats.Chat
	if in.Retry != "" {
		stored, err = r.Chats.Update(chat.ID, func(c *chats.Chat) error {
			i := slices.IndexFunc(c.Messages, func(m chats.Message) bool { return m.ID == in.Retry && m.Role == "user" })
			if i < 0 {
				return refuse(404, "Сообщение не найдено: возможно, оно удалено.")
			}
			c.Messages = c.Messages[:i+1]
			c.Model, c.AssistantID = in.Model, assistantID
			return nil
		})
	} else {
		msg := chats.Message{ID: chats.NewMessageID(), Role: "user", Text: text, Attachments: attachments, Created: r.now().UTC()}
		stored, err = r.Chats.Update(chat.ID, func(c *chats.Chat) error {
			c.Messages = append(c.Messages, msg)
			c.Model, c.AssistantID = in.Model, assistantID
			if c.Title == chats.DefaultTitle && text != "" {
				c.Title = titleOf(text)
			}
			return nil
		})
	}
	if err != nil {
		return nil, err
	}
	t.chat = stored
	t.user = stored.Messages[len(stored.Messages)-1]
	t.history = stored.Messages[:len(stored.Messages)-1]
	return t, nil
}

func understands(m library.Model, need string) bool {
	switch need {
	case "vision":
		return m.Caps.Vision
	case "audio":
		return m.Caps.Audio
	case "video":
		return m.Caps.Video
	}
	return true
}

func kindName(kind string) string {
	switch kind {
	case attach.Image:
		return "изображения"
	case attach.Video:
		return "видео"
	case attach.Audio:
		return "аудио"
	}
	return "такие файлы"
}

func titleOf(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return clip(strings.Join(strings.Fields(line), " "), 60)
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n-1])) + "…"
}

// estimateTokens is a rough count for trimming the history: about three characters a token in
// Russian and code, a little more in English.
func estimateTokens(s string) int { return utf8.RuneCountInString(s)/3 + 1 }

func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}
