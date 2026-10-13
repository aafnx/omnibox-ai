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
	// MaxBranches bounds the versions kept from one question on; the oldest goes first.
	MaxBranches = 10
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
	// TitleSet means the owner named the chat; TitleAuto that the model did. Either way the
	// model does not rename it again.
	TitleSet  bool `json:"title_set,omitempty"`
	TitleAuto bool `json:"title_auto,omitempty"`
	// FollowUps turns the suggested questions on or off for this chat; nil follows the setting.
	FollowUps *bool `json:"follow_ups,omitempty"`
	// ProjectID is the project the chat belongs to: its instructions and documents come with it.
	ProjectID string `json:"project_id,omitempty"`
	// Temporary chats live in the service's memory only, never on disk, and go when the owner
	// leaves them, when the service restarts, or a day after they were last used.
	Temporary bool      `json:"temporary,omitempty"`
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
	// Branches are the other versions of the conversation from this user message on — an
	// earlier wording of the question or an earlier answer, each with what followed it. Branch
	// is the place of the shown version among all of them.
	Branches [][]Message `json:"branches,omitempty"`
	Branch   int         `json:"branch,omitempty"`
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
	// Pages are the addresses of the sources read in full for this answer.
	Pages []string `json:"pages,omitempty"`
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
	ProjectID   string     `json:"project_id,omitempty"`
	Temporary   bool       `json:"temporary,omitempty"`
}

// Hit is a chat found by Search; MessageID is empty when the title matched.
type Hit struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Updated   time.Time `json:"updated"`
	Pinned    bool      `json:"pinned"`
	ProjectID string    `json:"project_id,omitempty"`
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
		Messages: len(c.Messages), CostUSD: c.CostUSD, FollowUps: c.FollowUps, ProjectID: c.ProjectID, Temporary: c.Temporary}
}

// Store keeps chats as files in Dir. Writes are serialized, so a read-modify-write of a chat
// cannot lose a concurrent change or bring back a chat deleted meanwhile.
type Store struct {
	Dir string
	Now func() time.Time

	mu sync.Mutex
	// temp holds the temporary chats, encoded as their files would be: nothing of them is written.
	temp map[string][]byte
}

// tempKeep is how long an untouched temporary chat stays in memory.
const tempKeep = 24 * time.Hour

// New is a chat to start.
type New struct {
	Title       string
	KeepHours   int // the retention setting now; 0 keeps the chat forever
	AssistantID string
	Model       string
	ProjectID   string
	Temporary   bool
}

// Create starts an empty chat; keepHours is the retention setting now (0 keeps it forever).
func (s *Store) Create(title string, keepHours int, assistantID, model string) (*Chat, error) {
	return s.Start(New{Title: title, KeepHours: keepHours, AssistantID: assistantID, Model: model})
}

// Start starts an empty chat, in a project or temporary when asked.
func (s *Store) Start(n New) (*Chat, error) {
	title, keepHours, assistantID, model := n.Title, n.KeepHours, n.AssistantID, n.Model
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
	if n.ProjectID != "" && !validProjectID(n.ProjectID) {
		return nil, errorf("Некорректный идентификатор проекта.")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if n.Temporary {
		s.purgeTemp()
		now := s.now()
		c := &Chat{ID: randomHex(12), Title: title, Created: now, Updated: now, AssistantID: assistantID, Model: model,
			ProjectID: n.ProjectID, Temporary: true, Messages: []Message{}}
		return c, s.write(c, now)
	}
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
		TimerStart: now, AssistantID: assistantID, Model: model, ProjectID: n.ProjectID, Messages: []Message{}}
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
	// The lock guards the temporary chats in memory; files are replaced atomically anyway.
	s.mu.Lock()
	defer s.mu.Unlock()
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
	c.Temporary = keep.Temporary
	if err := s.write(c, s.now()); err != nil {
		return nil, err
	}
	return c, nil
}

// SetAutoTitle gives the chat the title its model wrote, unless the owner named it meanwhile.
func (s *Store) SetAutoTitle(id, title string) (*Chat, error) {
	title = cleanTitle(title)
	if title == "" {
		return nil, errorf("Пустое название.")
	}
	return s.Update(id, func(c *Chat) error {
		if !c.TitleSet {
			c.Title = title
		}
		c.TitleAuto = true
		return nil
	})
}

// AddCost adds what a side request for an answer cost (its title, say) to that answer.
func (s *Store) AddCost(id, messageID string, cost float64) (*Chat, error) {
	return s.Update(id, func(c *Chat) error {
		i := slices.IndexFunc(c.Messages, func(m Message) bool { return m.ID == messageID })
		if i < 0 {
			return errMessageNotFound
		}
		if c.Messages[i].Meta == nil {
			c.Messages[i].Meta = &Meta{}
		}
		c.Messages[i].Meta.CostUSD += cost
		return nil
	})
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
	return s.Update(id, func(c *Chat) error { c.Title, c.TitleSet = title, true; return nil })
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
	if c.Temporary && pinned {
		return nil, errorf("Временный чат не закрепляется: он исчезает, когда вы из него уходите.")
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
	if _, ok := s.temp[id]; ok {
		delete(s.temp, id)
		return nil
	}
	err := os.Remove(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return errChatNotFound
	}
	return err
}

// purgeTemp forgets temporary chats untouched for a day; it runs when one is started and when the
// chats are listed or searched, not on a timer.
func (s *Store) purgeTemp() {
	now := s.now()
	for id, data := range s.temp {
		var c struct{ Updated time.Time }
		if json.Unmarshal(data, &c) != nil || now.Sub(c.Updated) > tempKeep {
			delete(s.temp, id)
		}
	}
}

// SetProject puts a chat into a project, or takes it out with "".
func (s *Store) SetProject(id, projectID string) (*Chat, error) {
	if projectID != "" && !validProjectID(projectID) {
		return nil, errorf("Некорректный идентификатор проекта.")
	}
	return s.Update(id, func(c *Chat) error {
		c.ProjectID = projectID
		return nil
	})
}

// LeaveProject takes every chat out of a deleted project; the chats themselves stay.
func (s *Store) LeaveProject(projectID string) error {
	var ids []string
	if _, err := s.scan(s.now(), func(c *Chat) {
		if c.ProjectID == projectID {
			ids = append(ids, c.ID)
		}
	}); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.SetProject(id, ""); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// DeleteAll removes every chat, or every unpinned one when keepPinned is set, and returns how
// many were removed. With keepPinned a chat that cannot be read is kept: it may be pinned.
func (s *Store) DeleteAll(keepPinned bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Temporary chats are never pinned: they go with the rest.
	deleted := len(s.temp)
	s.temp = nil
	names, err := chatNames(s.Dir)
	if err != nil {
		return deleted, err
	}
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
	// A temporary chat left open (the browser closed) goes a day after its last message.
	s.purgeTemp()
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
func (s *Store) Search(q, project string) ([]Hit, error) {
	q = strings.Join(strings.Fields(q), " ")
	if q == "" {
		return []Hit{}, nil
	}
	if runes(q) > maxQuery {
		return nil, errorf("Запрос для поиска длиннее %d символов.", maxQuery)
	}
	if project != "" && !validProjectID(project) {
		return nil, errorf("Некорректный идентификатор проекта.")
	}
	needle := lower(q)
	words := slices.Compact(slices.Sorted(slices.Values(strings.Fields(strings.ToLower(q)))))
	hits := []Hit{}
	_, err := s.scan(s.now(), func(c *Chat) {
		if project != "" && c.ProjectID != project {
			return
		}
		hit := Hit{ID: c.ID, Title: c.Title, Updated: c.Updated, Pinned: c.Pinned, ProjectID: c.ProjectID}
		// The words as typed come first; then a chat that has all of them, in any order and in
		// any of its messages, shown at the message that has the most.
		if snip, ok := snippet(c.Title, needle); ok {
			hit.Snippet = snip
			hits = append(hits, hit)
			return
		}
		for _, m := range c.Messages {
			if snip, ok := snippet(m.Text, needle); ok {
				hit.MessageID, hit.Snippet = m.ID, snip
				hits = append(hits, hit)
				return
			}
		}
		if len(words) < 2 {
			return
		}
		texts := []string{strings.ToLower(c.Title)}
		for _, m := range c.Messages {
			texts = append(texts, strings.ToLower(m.Text))
		}
		best, bestN := -1, 0
		for _, w := range words {
			if !slices.ContainsFunc(texts, func(t string) bool { return strings.Contains(t, w) }) {
				return
			}
		}
		for i, t := range texts {
			n := 0
			for _, w := range words {
				if strings.Contains(t, w) {
					n++
				}
			}
			if n > bestN {
				best, bestN = i, n
			}
		}
		text := c.Title
		if best > 0 {
			text, hit.MessageID = c.Messages[best-1].Text, c.Messages[best-1].ID
		}
		for _, w := range words {
			if snip, ok := snippet(text, []rune(w)); ok {
				hit.Snippet = snip
				break
			}
		}
		hits = append(hits, hit)
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
	data, temp := s.temp[id]
	var err error
	if !temp {
		data, err = platform.ReadShared(s.path(id))
	}
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
	cost, err := checkMessages(c.Messages, now, 0)
	if err != nil {
		return err
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
	if c.Temporary {
		if s.temp == nil {
			s.temp = map[string][]byte{}
		}
		s.temp[c.ID] = data
		return nil
	}
	return platform.WriteAtomic(s.path(c.ID), data, 0o600)
}

// checkMessages completes and checks a list of messages and the branches they keep, and returns
// what they cost: the versions not shown were paid for too.
func checkMessages(list []Message, now time.Time, depth int) (float64, error) {
	if depth > 50 {
		return 0, errorf("Слишком глубокое ветвление чата.")
	}
	cost := 0.0
	seen := map[string]bool{}
	for i := range list {
		m := &list[i]
		if m.ID == "" {
			m.ID = NewMessageID()
		}
		if m.Created.IsZero() {
			m.Created = now
		}
		if err := checkMessage(m); err != nil {
			return 0, err
		}
		if seen[m.ID] {
			return 0, errorf("Повторяется идентификатор сообщения %s.", m.ID)
		}
		seen[m.ID] = true
		if m.Meta != nil {
			cost += m.Meta.CostUSD
		}
		if len(m.Branches) > 0 && (m.Role != "user" || len(m.Branches) >= MaxBranches || m.Branch < 0 || m.Branch > len(m.Branches)) {
			return 0, errorf("Некорректные версии сообщения %s.", m.ID)
		}
		for _, b := range m.Branches {
			if len(b) == 0 || b[0].Role != "user" {
				return 0, errorf("Некорректные версии сообщения %s.", m.ID)
			}
			c, err := checkMessages(b, now, depth+1)
			if err != nil {
				return 0, err
			}
			cost += c
		}
	}
	return cost, nil
}

// Fork starts a new version of the conversation at user message i: what was from i on is kept
// among its branches, and next takes its place (the same question to answer again, or an edited
// one). The oldest version goes when there are too many.
func Fork(c *Chat, i int, next Message) {
	tail := slices.Clone(c.Messages[i:])
	all := slices.Clone(tail[0].Branches)
	pos := min(max(tail[0].Branch, 0), len(all))
	tail[0].Branches, tail[0].Branch = nil, 0
	all = slices.Insert(all, pos, tail)
	if len(all) >= MaxBranches {
		all = all[len(all)-MaxBranches+1:]
	}
	next.Branches, next.Branch = all, len(all)
	c.Messages = append(c.Messages[:i:i], next)
}

// SwitchBranch shows version j of the conversation from user message msgID on.
func (s *Store) SwitchBranch(chatID, msgID string, j int) (*Chat, error) {
	return s.Update(chatID, func(c *Chat) error {
		i := slices.IndexFunc(c.Messages, func(m Message) bool { return m.ID == msgID && m.Role == "user" })
		if i < 0 {
			return errMessageNotFound
		}
		first := c.Messages[i]
		if j < 0 || j > len(first.Branches) {
			return errorf("Такой версии нет.")
		}
		if j == first.Branch {
			return nil
		}
		tail := slices.Clone(c.Messages[i:])
		tail[0].Branches, tail[0].Branch = nil, 0
		all := slices.Insert(slices.Clone(first.Branches), min(max(first.Branch, 0), len(first.Branches)), tail)
		chosen := slices.Clone(all[j])
		chosen[0].Branches, chosen[0].Branch = slices.Delete(all, j, j+1), j
		c.Messages = append(c.Messages[:i:i], chosen...)
		return nil
	})
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
