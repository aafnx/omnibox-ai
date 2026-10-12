// Package chats stores the panel's built-in chat history and assistants on the PC.
//
// Chat content is kept only for the panel's own chat, by the owner's decision: requests to /v1
// from other clients are never stored here. Each chat is one file <Dir>/<id>.json readable only by
// the service. A chat is removed at once and without copies when the owner deletes it, and by
// retention: it lives keep_hours from its timer start unless it is pinned. Expired chats are
// removed by Cleanup, which runs on listing and at service start, never on a timer.
package chats

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aafnx/omnibox-ai/internal/platform"
)

const (
	// MaxChats bounds the number of chat files, so the history cannot fill the disk unnoticed.
	MaxChats = 5000
	// MaxChatBytes bounds one chat file; a longer conversation is better started anew.
	MaxChatBytes = 8 << 20
	// MaxTitle is the longest chat title in characters.
	MaxTitle = 120
	// MaxFollowUps and MaxFollowUp bound the suggested questions under an answer.
	MaxFollowUps = 5
	MaxFollowUp  = 200
	// DefaultTitle names a chat created without a title.
	DefaultTitle = "Новый чат"

	maxSearchHits = 50
	maxQuery      = 200
	maxModel      = 200
	snippetRunes  = 40
	// A temporary file older than this was left by an interrupted write, not by one in progress.
	staleTemp = time.Hour
)

var (
	chatID    = regexp.MustCompile(`^[0-9a-f]{24}$`)
	messageID = regexp.MustCompile(`^[0-9a-f]{12}$`)
	chatFile  = regexp.MustCompile(`^[0-9a-f]{24}\.json$`)
	tempFile  = regexp.MustCompile(`^[0-9a-f]{24}\.json\.tmp-`)
)

// ErrNotFound is matched (errors.Is) by every "chat, message or assistant not found" error.
var ErrNotFound = errors.New("not found")

// notFound is shown to the owner as is and matches ErrNotFound.
type notFound string

func (e notFound) Error() string      { return string(e) }
func (notFound) Is(target error) bool { return target == ErrNotFound }

// inputError is a refusal the owner can fix; its text is shown in the panel as is.
type inputError string

func (e inputError) Error() string { return string(e) }

func errorf(format string, args ...any) error { return inputError(fmt.Sprintf(format, args...)) }

func isInput(err error) bool {
	var e inputError
	return errors.As(err, &e)
}

func randomHex(bytes int) string {
	b := make([]byte, bytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewMessageID returns a random message id, for a caller that needs it before saving the
// message; Save gives one to every message that has none.
func NewMessageID() string { return randomHex(6) }

func validChatID(id string) bool { return chatID.MatchString(id) }
func hasControl(s string) bool   { return strings.IndexFunc(s, unicode.IsControl) >= 0 }
func runes(s string) int         { return utf8.RuneCountInString(s) }

func (s *Store) path(id string) string { return filepath.Join(s.Dir, id+".json") }

func (s *Store) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

var (
	errChatNotFound    = notFound("Чат не найден.")
	errMessageNotFound = notFound("Сообщение не найдено.")
)

// Chat is one conversation with everything the panel shows for it.
type Chat struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Pinned  bool      `json:"pinned"`
	// KeepHours is the retention setting when the chat was created; 0 keeps it forever.
	KeepHours int `json:"keep_hours"`
	// TimerStart is when the retention timer started: creation, or the last unpinning.
	TimerStart  time.Time `json:"timer_start"`
	AssistantID string    `json:"assistant_id"`
	Model       string    `json:"model"`
	// CostUSD is the sum of the messages' cost; the store recomputes it on every write.
	CostUSD float64 `json:"cost_usd"`
	// FollowUps turns the suggested questions on or off for this chat; nil follows the setting.
	FollowUps *bool     `json:"follow_ups,omitempty"`
	Messages  []Message `json:"messages"`
}

// Message is one turn of a chat.
type Message struct {
	ID               string       `json:"id"`
	Role             string       `json:"role"`
	Text             string       `json:"text"`
	Reasoning        string       `json:"reasoning,omitempty"`
	ReasoningSeconds float64      `json:"reasoning_seconds,omitempty"`
	Attachments      []Attachment `json:"attachments,omitempty"`
	Model            string       `json:"model,omitempty"`
	Created          time.Time    `json:"created"`
	Meta             *Meta        `json:"meta,omitempty"`
	Error            string       `json:"error,omitempty"`
}

// Attachment describes a file sent with a message; the file itself is not stored.
type Attachment struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Size   int64  `json:"size"`
	Tokens int    `json:"tokens"`
	// Text is what a text, Word or Excel file gave the model, kept so later questions about it
	// still have it. Images, audio and video are not kept.
	Text string `json:"text,omitempty"`
}

// Meta is how an answer was produced.
type Meta struct {
	Seconds      float64 `json:"seconds"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	// ContextTokens is how much of the model's context the chat filled after this answer.
	ContextTokens   int      `json:"context_tokens,omitempty"`
	TokensPerSecond float64  `json:"tokens_per_second"`
	CostUSD         float64  `json:"cost_usd"`
	Sources         []Source `json:"sources,omitempty"`
	Searches        []string `json:"searches,omitempty"`
	// FollowUps are questions the user may ask next, written after the answer on request.
	FollowUps []string `json:"follow_ups,omitempty"`
}

// Source is a web page an answer used.
type Source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Summary is a chat as the history list shows it, without messages.
type Summary struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Created     time.Time  `json:"created"`
	Updated     time.Time  `json:"updated"`
	Pinned      bool       `json:"pinned"`
	KeepHours   int        `json:"keep_hours"`
	Expires     *time.Time `json:"expires"`
	AssistantID string     `json:"assistant_id"`
	Model       string     `json:"model"`
	Messages    int        `json:"messages"`
	CostUSD     float64    `json:"cost_usd"`
	FollowUps   *bool      `json:"follow_ups,omitempty"`
}

// Hit is a chat found by Search; MessageID is empty when the title matched.
type Hit struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Updated   time.Time `json:"updated"`
	Pinned    bool      `json:"pinned"`
	MessageID string    `json:"message_id,omitempty"`
	Snippet   string    `json:"snippet"`
}

// Expires is when retention removes the chat, or nil when it is pinned or kept forever.
func (c *Chat) Expires() *time.Time {
	if c.Pinned || c.KeepHours <= 0 {
		return nil
	}
	t := c.TimerStart.Add(time.Duration(c.KeepHours) * time.Hour)
	return &t
}

func (c *Chat) expired(now time.Time) bool {
	e := c.Expires()
	return e != nil && !now.Before(*e)
}

// Summary returns the chat as the history list shows it.
func (c *Chat) Summary() Summary {
	return Summary{ID: c.ID, Title: c.Title, Created: c.Created, Updated: c.Updated, Pinned: c.Pinned,
		KeepHours: c.KeepHours, Expires: c.Expires(), AssistantID: c.AssistantID, Model: c.Model,
		Messages: len(c.Messages), CostUSD: c.CostUSD, FollowUps: c.FollowUps}
}

// Store keeps chats as files in Dir. Writes are serialized, so a read-modify-write of a chat
// cannot lose a concurrent change or bring back a chat deleted meanwhile.
type Store struct {
	Dir string
	Now func() time.Time

	mu sync.Mutex
}

// Create starts an empty chat; keepHours is the retention setting now (0 keeps it forever).
func (s *Store) Create(title string, keepHours int, assistantID, model string) (*Chat, error) {
	title = cleanTitle(title)
	if title == "" {
		title = DefaultTitle
	}
	if err := checkTitle(title); err != nil {
		return nil, err
	}
	if keepHours < 0 {
		return nil, errorf("Срок хранения чатов не может быть отрицательным.")
	}
	if assistantID != "" && !assistantPattern.MatchString(assistantID) {
		return nil, errorf("Некорректный идентификатор ассистента.")
	}
	if err := checkModel(model); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	names, err := chatNames(s.Dir)
	if err != nil {
		return nil, err
	}
	if len(names) >= MaxChats {
		return nil, errorf("Сохранено %d чатов — это предел. Удалите ненужные чаты.", MaxChats)
	}
	now := s.now()
	c := &Chat{ID: randomHex(12), Title: title, Created: now, Updated: now, KeepHours: keepHours,
		TimerStart: now, AssistantID: assistantID, Model: model, Messages: []Message{}}
	if err := s.write(c, now); err != nil {
		return nil, err
	}
	return c, nil
}

// Get reads one chat.
func (s *Store) Get(id string) (*Chat, error) {
	if !validChatID(id) {
		return nil, errChatNotFound
	}
	return s.read(id)
}

// Save writes a chat changed by the caller: it sets Updated, recomputes CostUSD, gives new
// messages an id and a time, and copies the result back into c. Retention fields (Created,
// Pinned, KeepHours, TimerStart) stay as stored: they change only through SetPinned. A chat
// deleted meanwhile is not recreated: Save returns ErrNotFound.
func (s *Store) Save(c *Chat) error {
	saved, err := s.Update(c.ID, func(stored *Chat) error {
		*stored = *c
		return nil
	})
	if err == nil {
		*c = *saved
	}
	return err
}

// Update changes a stored chat under the store's lock, so the change is applied to the latest
// version of the chat. fn may change the content; retention fields are kept as stored.
func (s *Store) Update(id string, fn func(*Chat) error) (*Chat, error) {
	if !validChatID(id) {
		return nil, errChatNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.update(id, fn)
}

func (s *Store) update(id string, fn func(*Chat) error) (*Chat, error) {
	c, err := s.read(id)
	if err != nil {
		return nil, err
	}
	keep := *c
	if err := fn(c); err != nil {
		return nil, err
	}
	c.ID, c.Created, c.Pinned, c.KeepHours, c.TimerStart = keep.ID, keep.Created, keep.Pinned, keep.KeepHours, keep.TimerStart
	if err := s.write(c, s.now()); err != nil {
		return nil, err
	}
	return c, nil
}

// SetFollowUps turns the suggested questions on or off for a chat; nil follows the setting.
func (s *Store) SetFollowUps(id string, on *bool) (*Chat, error) {
	return s.Update(id, func(c *Chat) error { c.FollowUps = on; return nil })
}

// SetSuggestions stores the questions suggested under an answer of the chat.
func (s *Store) SetSuggestions(id, messageID string, questions []string, cost float64) (*Chat, error) {
	return s.Update(id, func(c *Chat) error {
		i := slices.IndexFunc(c.Messages, func(m Message) bool { return m.ID == messageID && m.Role == "assistant" })
		if i < 0 {
			return errMessageNotFound
		}
		m := &c.Messages[i]
		if m.Meta == nil {
			m.Meta = &Meta{}
		}
		m.Meta.FollowUps = questions
		m.Meta.CostUSD += cost
		return nil
	})
}

// Rename sets a chat's title.
func (s *Store) Rename(id, title string) (*Chat, error) {
	title = cleanTitle(title)
	if title == "" {
		return nil, errorf("Название чата не может быть пустым.")
	}
	if err := checkTitle(title); err != nil {
		return nil, err
	}
	return s.Update(id, func(c *Chat) error { c.Title = title; return nil })
}

// SetPinned pins or unpins a chat. Unpinning restarts the retention timer from now with the
// chat's own keep_hours, so a chat kept for months is not removed the moment it is unpinned.
func (s *Store) SetPinned(id string, pinned bool) (*Chat, error) {
	if !validChatID(id) {
		return nil, errChatNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.read(id)
	if err != nil {
		return nil, err
	}
	if c.Pinned == pinned {
		return c, nil
	}
	now := s.now()
	c.Pinned = pinned
	if !pinned {
		c.TimerStart = now
	}
	if err := s.write(c, now); err != nil {
		return nil, err
	}
	return c, nil
}

// Delete removes a chat's file; nothing of it is kept.
func (s *Store) Delete(id string) error {
	if !validChatID(id) {
		return errChatNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return errChatNotFound
	}
	return err
}

// DeleteAll removes every chat, or every unpinned one when keepPinned is set, and returns how
// many were removed. With keepPinned a chat that cannot be read is kept: it may be pinned.
func (s *Store) DeleteAll(keepPinned bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names, err := chatNames(s.Dir)
	if err != nil {
		return 0, err
	}
	deleted := 0
	var firstErr error
	for _, name := range names {
		id := strings.TrimSuffix(name, ".json")
		if keepPinned {
			c, err := s.read(id)
			if err != nil || c.Pinned {
				continue
			}
		}
		if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		deleted++
	}
	return deleted, firstErr
}

// DeleteMessage removes one message from a chat and rewrites the chat without it.
func (s *Store) DeleteMessage(chatID, msgID string) (*Chat, error) {
	if !messageID.MatchString(msgID) {
		return nil, errMessageNotFound
	}
	return s.Update(chatID, func(c *Chat) error {
		i := slices.IndexFunc(c.Messages, func(m Message) bool { return m.ID == msgID })
		if i < 0 {
			return errMessageNotFound
		}
		c.Messages = slices.Delete(c.Messages, i, i+1)
		return nil
	})
}

// List removes expired chats and returns the rest: pinned first, then the latest updated.
// A file that cannot be read is skipped, not removed.
func (s *Store) List() ([]Summary, error) {
	out := []Summary{}
	_, err := s.scan(s.now(), func(c *Chat) { out = append(out, c.Summary()) })
	slices.SortFunc(out, func(a, b Summary) int {
		switch {
		case a.Pinned != b.Pinned:
			if a.Pinned {
				return -1
			}
			return 1
		case !a.Updated.Equal(b.Updated):
			return b.Updated.Compare(a.Updated)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, err
}

// Cleanup removes chats whose retention has passed by now and temporary files left by an
// interrupted write, and returns how many chats it removed. It is called at service start and
// on listing; there is no timer.
func (s *Store) Cleanup(now time.Time) int {
	n, _ := s.scan(now.UTC(), func(*Chat) {})
	return n
}

// scan reads every chat under the lock, removes the expired ones and visits the rest; it
// returns how many it removed.
func (s *Store) scan(now time.Time, visit func(*Chat)) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if tempFile.MatchString(name) {
			if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > staleTemp {
				_ = os.Remove(filepath.Join(s.Dir, name))
			}
			continue
		}
		if !chatFile.MatchString(name) || !e.Type().IsRegular() {
			continue
		}
		c, err := s.read(strings.TrimSuffix(name, ".json"))
		if err != nil {
			continue
		}
		if c.expired(now) {
			if err := os.Remove(s.path(c.ID)); err == nil {
				removed++
			}
			continue
		}
		visit(c)
	}
	return removed, nil
}

// Search finds chats whose title or message text contains q, ignoring case: up to 50, the
// latest updated first, each with a short snippet around the first match.
func (s *Store) Search(q string) ([]Hit, error) {
	q = strings.Join(strings.Fields(q), " ")
	if q == "" {
		return []Hit{}, nil
	}
	if runes(q) > maxQuery {
		return nil, errorf("Запрос для поиска длиннее %d символов.", maxQuery)
	}
	needle := lower(q)
	hits := []Hit{}
	_, err := s.scan(s.now(), func(c *Chat) {
		if snip, ok := snippet(c.Title, needle); ok {
			hits = append(hits, Hit{ID: c.ID, Title: c.Title, Updated: c.Updated, Pinned: c.Pinned, Snippet: snip})
			return
		}
		for _, m := range c.Messages {
			if snip, ok := snippet(m.Text, needle); ok {
				hits = append(hits, Hit{ID: c.ID, Title: c.Title, Updated: c.Updated, Pinned: c.Pinned, MessageID: m.ID, Snippet: snip})
				return
			}
		}
	})
	slices.SortFunc(hits, func(a, b Hit) int {
		if c := b.Updated.Compare(a.Updated); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	if len(hits) > maxSearchHits {
		hits = hits[:maxSearchHits]
	}
	return hits, err
}

// lower folds case rune by rune, so rune positions in the result match the original text.
func lower(s string) []rune {
	r := []rune(s)
	for i, c := range r {
		r[i] = unicode.ToLower(c)
	}
	return r
}

// snippet returns the text around the first match of needle (already lowered), on one line.
func snippet(text string, needle []rune) (string, bool) {
	hay := []rune(text)
	low := lower(text)
	at := -1
	for i := 0; i+len(needle) <= len(low); i++ {
		if slices.Equal(low[i:i+len(needle)], needle) {
			at = i
			break
		}
	}
	if at < 0 {
		return "", false
	}
	start, end := max(at-snippetRunes, 0), min(at+len(needle)+snippetRunes, len(hay))
	out := strings.Join(strings.Fields(string(hay[start:end])), " ")
	if start > 0 {
		out = "…" + out
	}
	if end < len(hay) {
		out += "…"
	}
	return out, true
}

func (s *Store) read(id string) (*Chat, error) {
	data, err := platform.ReadShared(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, errChatNotFound
	}
	if err != nil {
		return nil, err
	}
	var c Chat
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("chat %s: %w", id, err)
	}
	if c.ID != id {
		return nil, fmt.Errorf("chat %s: file holds chat %q", id, c.ID)
	}
	if c.Messages == nil {
		c.Messages = []Message{}
	}
	return &c, nil
}

// write checks a chat, completes new messages and puts the file in place atomically.
func (s *Store) write(c *Chat, now time.Time) error {
	if c.Messages == nil {
		c.Messages = []Message{}
	}
	c.Title = cleanTitle(c.Title)
	if c.Title == "" {
		c.Title = DefaultTitle
	}
	if err := checkTitle(c.Title); err != nil {
		return err
	}
	if err := checkModel(c.Model); err != nil {
		return err
	}
	cost := 0.0
	seen := map[string]bool{}
	for i := range c.Messages {
		m := &c.Messages[i]
		if m.ID == "" {
			m.ID = NewMessageID()
		}
		if m.Created.IsZero() {
			m.Created = now
		}
		if err := checkMessage(m); err != nil {
			return err
		}
		if seen[m.ID] {
			return errorf("Повторяется идентификатор сообщения %s.", m.ID)
		}
		seen[m.ID] = true
		if m.Meta != nil {
			cost += m.Meta.CostUSD
		}
	}
	c.CostUSD = cost
	c.Updated = now
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if len(data) > MaxChatBytes {
		return errorf("Чат больше %d МБ и не сохранён. Начните новый чат или удалите часть сообщений.", MaxChatBytes>>20)
	}
	return platform.WriteAtomic(s.path(c.ID), data, 0o600)
}

var attachmentKinds = []string{"image", "video", "audio", "text", "word", "excel"}

func checkMessage(m *Message) error {
	if !messageID.MatchString(m.ID) {
		return errorf("Некорректный идентификатор сообщения.")
	}
	if m.Role != "user" && m.Role != "assistant" {
		return errorf("Роль сообщения — user или assistant.")
	}
	if m.ReasoningSeconds < 0 {
		return errorf("Время рассуждения не может быть отрицательным.")
	}
	if err := checkModel(m.Model); err != nil {
		return err
	}
	for _, a := range m.Attachments {
		if !slices.Contains(attachmentKinds, a.Kind) {
			return errorf("Неизвестный тип вложения %q.", a.Kind)
		}
		if a.Size < 0 || a.Tokens < 0 {
			return errorf("Размер вложения не может быть отрицательным.")
		}
	}
	if x := m.Meta; x != nil && (x.Seconds < 0 || x.InputTokens < 0 || x.OutputTokens < 0 || x.TokensPerSecond < 0 || x.CostUSD < 0) {
		return errorf("Статистика ответа не может быть отрицательной.")
	}
	if x := m.Meta; x != nil {
		if len(x.FollowUps) > MaxFollowUps {
			return errorf("Не больше %d предложенных вопросов.", MaxFollowUps)
		}
		for _, q := range x.FollowUps {
			if q == "" || runes(q) > MaxFollowUp || !utf8.ValidString(q) || hasControl(q) {
				return errorf("Некорректный предложенный вопрос.")
			}
		}
	}
	return nil
}

// cleanTitle puts a title on one line: line breaks and runs of spaces become one space.
func cleanTitle(t string) string { return strings.Join(strings.Fields(t), " ") }

func checkTitle(t string) error {
	if !utf8.ValidString(t) || hasControl(t) {
		return errorf("Название чата содержит недопустимые символы.")
	}
	if runes(t) > MaxTitle {
		return errorf("Название чата длиннее %d символов.", MaxTitle)
	}
	return nil
}

func checkModel(m string) error {
	if len(m) > maxModel || !utf8.ValidString(m) || hasControl(m) {
		return errorf("Некорректное имя модели: до %d байт, без управляющих символов.", maxModel)
	}
	return nil
}

// chatNames lists the chat files in dir; a missing directory has none.
func chatNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if chatFile.MatchString(e.Name()) && e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
