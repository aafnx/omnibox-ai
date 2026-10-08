package chats

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/aafnx/omnibox-ai/internal/platform"
)

const (
	// MaxAssistants bounds the assistants file.
	MaxAssistants   = 50
	maxEmoji        = 16
	maxName         = 40
	maxDescription  = 120
	maxInstructions = 20000
)

var (
	assistantPattern     = regexp.MustCompile(`^[0-9a-f]{24}$`)
	styles               = []string{"", "precise", "balanced", "creative"}
	errAssistantNotFound = notFound("Ассистент не найден.")
)

// Assistant is a saved role for chats: its instructions (Markdown) become the system prompt.
type Assistant struct {
	ID           string `json:"id"`
	Emoji        string `json:"emoji"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
	// Model is a model key, such as "local:prismml:<file>" or "cloud:<openrouter id>"; empty
	// means the model chosen in the chat.
	Model string `json:"model"`
	// Style is "", "precise", "balanced" or "creative".
	Style     string `json:"style"`
	WebSearch bool   `json:"web_search"`
}

// List is the assistants file: every assistant and the one new chats start with ("" = none).
type List struct {
	Assistants []Assistant `json:"assistants"`
	Default    string      `json:"default"`
}

// Find returns the assistant with the id.
func (l List) Find(id string) (Assistant, bool) {
	i := slices.IndexFunc(l.Assistants, func(a Assistant) bool { return a.ID == id })
	if i < 0 {
		return Assistant{}, false
	}
	return l.Assistants[i], true
}

// Assistants keeps the assistants in one file at Path; writes are serialized.
type Assistants struct {
	Path string

	mu sync.Mutex
}

// Load reads the assistants; a missing file means none.
func (s *Assistants) Load() (List, error) {
	data, err := platform.ReadShared(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return List{Assistants: []Assistant{}}, nil
	}
	if err != nil {
		return List{}, err
	}
	var l List
	if err := json.Unmarshal(data, &l); err != nil {
		return List{}, err
	}
	if l.Assistants == nil {
		l.Assistants = []Assistant{}
	}
	return l, nil
}

// Save checks an assistant and stores it: a new one when its ID is empty, otherwise in place of
// the assistant with that ID. It returns the assistant as stored.
func (s *Assistants) Save(a Assistant) (Assistant, error) {
	a.Emoji = strings.TrimSpace(a.Emoji)
	a.Name = strings.TrimSpace(a.Name)
	a.Description = strings.TrimSpace(a.Description)
	a.Instructions = strings.TrimSpace(a.Instructions)
	a.Model = strings.TrimSpace(a.Model)
	if err := checkAssistant(a); err != nil {
		return Assistant{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.Load()
	if err != nil {
		return Assistant{}, err
	}
	for _, o := range l.Assistants {
		if o.ID != a.ID && strings.EqualFold(o.Name, a.Name) {
			return Assistant{}, errorf("Ассистент «%s» уже есть.", o.Name)
		}
	}
	if a.ID == "" {
		if len(l.Assistants) >= MaxAssistants {
			return Assistant{}, errorf("Можно сохранить до %d ассистентов.", MaxAssistants)
		}
		a.ID = randomHex(12)
		l.Assistants = append(l.Assistants, a)
	} else {
		i := slices.IndexFunc(l.Assistants, func(o Assistant) bool { return o.ID == a.ID })
		if i < 0 {
			return Assistant{}, errAssistantNotFound
		}
		l.Assistants[i] = a
	}
	return a, s.write(l)
}

// Delete removes an assistant; if it was the default, new chats start without one.
func (s *Assistants) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.Load()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(l.Assistants, func(a Assistant) bool { return a.ID == id })
	if i < 0 {
		return errAssistantNotFound
	}
	l.Assistants = slices.Delete(l.Assistants, i, i+1)
	if l.Default == id {
		l.Default = ""
	}
	return s.write(l)
}

// SetDefault chooses the assistant new chats start with; "" means none.
func (s *Assistants) SetDefault(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.Load()
	if err != nil {
		return err
	}
	if _, ok := l.Find(id); id != "" && !ok {
		return errAssistantNotFound
	}
	l.Default = id
	return s.write(l)
}

func (s *Assistants) write(l List) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	return platform.WriteAtomic(s.Path, data, 0o600)
}

func checkAssistant(a Assistant) error {
	if a.ID != "" && !assistantPattern.MatchString(a.ID) {
		return errAssistantNotFound
	}
	for _, f := range []string{a.Emoji, a.Name, a.Description, a.Instructions, a.Model} {
		if !utf8.ValidString(f) {
			return errorf("Поля ассистента должны быть в UTF-8.")
		}
	}
	switch {
	case len(a.Emoji) > maxEmoji || hasControl(a.Emoji):
		return errorf("Эмодзи ассистента — до %d байт.", maxEmoji)
	case a.Name == "":
		return errorf("Укажите имя ассистента.")
	case runes(a.Name) > maxName || hasControl(a.Name):
		return errorf("Имя ассистента — до %d символов, в одну строку.", maxName)
	case runes(a.Description) > maxDescription || hasControl(a.Description):
		return errorf("Описание ассистента — до %d символов, в одну строку.", maxDescription)
	case runes(a.Instructions) > maxInstructions:
		return errorf("Инструкции ассистента — до %d символов.", maxInstructions)
	case !slices.Contains(styles, a.Style):
		return errorf("Стиль ответов — precise, balanced или creative.")
	}
	return checkModel(a.Model)
}
