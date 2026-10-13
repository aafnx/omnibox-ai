// Package automations keeps the owner's automations: a Word template with the places to fill,
// the model that fills them and what the automation may use. A run reads the owner's data,
// proposes a value for each field, and after the owner's approval builds the document. Nothing
// of a run is written to disk: the data comes with the request and the document stays in memory
// until it is downloaded or an hour passes.
package automations

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aafnx/omnibox-ai/internal/docx"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Dir is the automations' directory under the data directory.
const Dir = "automations"

const (
	maxAutomations = 100
	maxFields      = 100
	maxPlaces      = 30
	maxName        = 80
	maxHint        = 300
	maxNotes       = 4000
)

// Kinds of automation.
const (
	KindDocument = "document"
)

// Automation is one saved scenario.
type Automation struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Emoji string `json:"emoji"`
	Kind  string `json:"kind"`
	// Private keeps everything on the PC: a local model only. No automation ever searches the web.
	Private bool `json:"private"`
	// Model is the model key it runs with; "" means the model chosen in the chat.
	Model string `json:"model"`
	// Instructions are the owner's notes for the model: formats, names, what to leave blank.
	Instructions string    `json:"instructions"`
	Template     *Template `json:"template,omitempty"`
	Created      time.Time `json:"created"`
	Updated      time.Time `json:"updated"`
}

// Template is the Word file of a document automation and its fields.
type Template struct {
	Name   string  `json:"name"`
	Size   int64   `json:"size"`
	Fields []Field `json:"fields"`
}

// Field is one value of the document, written in one or more places.
type Field struct {
	Name   string  `json:"name"`
	Hint   string  `json:"hint"`
	Places []Place `json:"places"`
}

// Place is where a field's value goes: Find inside Context in paragraph Para. All fills every
// match of Context in the paragraph, not the first only.
type Place struct {
	Para    int    `json:"para"`
	Context string `json:"context"`
	Find    string `json:"find"`
	All     bool   `json:"all,omitempty"`
	// Text is the paragraph, for the owner and the model to see the place in its sentence.
	Text  string `json:"text,omitempty"`
	Where string `json:"where,omitempty"`
}

// Store keeps the automations' list and their templates, readable only by the service.
type Store struct {
	Dir string
	mu  sync.Mutex
}

type file struct {
	Automations []Automation `json:"automations"`
}

func (s *Store) listPath() string { return filepath.Join(s.Dir, "automations.json") }

func (s *Store) templatePath(id string) string {
	return filepath.Join(s.Dir, "templates", id+".docx")
}

func (s *Store) load() (file, error) {
	var f file
	data, err := platform.ReadShared(s.listPath())
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, errors.New("Список автоматизаций не прочитан.")
	}
	if json.Unmarshal(data, &f) != nil {
		return f, errors.New("Список автоматизаций повреждён.")
	}
	return f, nil
}

func (s *Store) save(f file) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(f, "", "  ")
	if err := platform.WriteAtomic(s.listPath(), data, 0o600); err != nil {
		return fmt.Errorf("Автоматизации не сохранены: %v", err)
	}
	return nil
}

// List returns the automations, the newest first.
func (s *Store) List() ([]Automation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	out := f.Automations
	slices.SortStableFunc(out, func(a, b Automation) int { return b.Created.Compare(a.Created) })
	return out, nil
}

// Get returns one automation.
func (s *Store) Get(id string) (Automation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return Automation{}, err
	}
	for _, a := range f.Automations {
		if a.ID == id {
			return a, nil
		}
	}
	return Automation{}, errors.New("Автоматизация не найдена: возможно, она удалена.")
}

// Template returns the Word file of an automation.
func (s *Store) Template(id string) ([]byte, error) {
	if !validID(id) {
		return nil, errors.New("Автоматизация не найдена.")
	}
	data, err := platform.ReadShared(s.templatePath(id))
	if err != nil {
		return nil, errors.New("Шаблон автоматизации не найден на ПК.")
	}
	return data, nil
}

// Save adds or replaces an automation; template, when given, replaces its Word file.
func (s *Store) Save(a Automation, template []byte, now time.Time) (Automation, error) {
	if err := check(&a); err != nil {
		return Automation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return Automation{}, err
	}
	i := slices.IndexFunc(f.Automations, func(x Automation) bool { return x.ID == a.ID })
	if a.ID == "" || i < 0 {
		if len(f.Automations) >= maxAutomations {
			return Automation{}, fmt.Errorf("Не больше %d автоматизаций.", maxAutomations)
		}
		a.ID, a.Created = newID(), now.UTC()
	} else {
		a.Created = f.Automations[i].Created
	}
	a.Updated = now.UTC()
	if template != nil {
		if err := os.MkdirAll(filepath.Dir(s.templatePath(a.ID)), 0o700); err != nil {
			return Automation{}, err
		}
		if err := platform.WriteAtomic(s.templatePath(a.ID), template, 0o600); err != nil {
			return Automation{}, fmt.Errorf("Шаблон не сохранён: %v", err)
		}
	} else if _, err := os.Stat(s.templatePath(a.ID)); a.Kind == KindDocument && err != nil {
		return Automation{}, errors.New("Загрузите шаблон Word.")
	}
	if i < 0 {
		f.Automations = append(f.Automations, a)
	} else {
		f.Automations[i] = a
	}
	if err := s.save(f); err != nil {
		return Automation{}, err
	}
	return a, nil
}

// Delete removes an automation and its template at once, without a copy.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(f.Automations, func(x Automation) bool { return x.ID == id })
	if i < 0 {
		return errors.New("Автоматизация не найдена: возможно, она уже удалена.")
	}
	f.Automations = slices.Delete(f.Automations, i, i+1)
	if err := s.save(f); err != nil {
		return err
	}
	if err := os.Remove(s.templatePath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Шаблон не удалён: %v", err)
	}
	return nil
}

// check validates what the panel sent; the fields are checked against the template elsewhere.
func check(a *Automation) error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || utf8.RuneCountInString(a.Name) > maxName {
		return fmt.Errorf("Название: от 1 до %d символов.", maxName)
	}
	if utf8.RuneCountInString(a.Emoji) > 8 {
		return errors.New("Значок — один эмодзи.")
	}
	if a.Kind != KindDocument {
		return errors.New("Неизвестный вид автоматизации.")
	}
	if utf8.RuneCountInString(a.Instructions) > maxNotes {
		return fmt.Errorf("Указания для модели: не больше %d символов.", maxNotes)
	}
	if a.Template == nil {
		return errors.New("Загрузите шаблон Word.")
	}
	if len(a.Template.Fields) > maxFields {
		return fmt.Errorf("Не больше %d полей.", maxFields)
	}
	seen := map[string]bool{}
	for i := range a.Template.Fields {
		f := &a.Template.Fields[i]
		f.Name = strings.TrimSpace(f.Name)
		if f.Name == "" || utf8.RuneCountInString(f.Name) > maxName {
			return fmt.Errorf("Название поля: от 1 до %d символов.", maxName)
		}
		if seen[strings.ToLower(f.Name)] {
			return fmt.Errorf("Поле «%s» повторяется.", f.Name)
		}
		seen[strings.ToLower(f.Name)] = true
		if utf8.RuneCountInString(f.Hint) > maxHint {
			f.Hint = string([]rune(f.Hint)[:maxHint])
		}
		if len(f.Places) == 0 || len(f.Places) > maxPlaces {
			return fmt.Errorf("Поле «%s»: от 1 до %d мест в документе.", f.Name, maxPlaces)
		}
	}
	return nil
}

// Verify checks every place against the template and fills in the paragraph text for display.
func Verify(t *Template, d *docx.Doc) error {
	paras := map[int]docx.Para{}
	for _, p := range d.Paragraphs() {
		paras[p.N] = p
	}
	for i := range t.Fields {
		f := &t.Fields[i]
		for j := range f.Places {
			p := &f.Places[j]
			para, ok := paras[p.Para]
			if !ok || p.Find == "" {
				return fmt.Errorf("Поле «%s»: место не найдено в шаблоне.", f.Name)
			}
			if p.Context == "" {
				p.Context = p.Find
			}
			if !strings.Contains(p.Context, p.Find) || d.Count(p.Para, p.Context) == 0 {
				return fmt.Errorf("Поле «%s»: текст «%s» не найден в абзаце %d.", f.Name, clip(p.Context, 40), p.Para+1)
			}
			p.Text, p.Where = clip(para.Text, 400), para.Where
		}
	}
	return nil
}

// Edits turns the approved values into the template's edits; a field left empty keeps its place
// as it is in the template.
func Edits(t *Template, values map[string]string) []docx.Edit {
	var out []docx.Edit
	for _, f := range t.Fields {
		v, ok := values[f.Name]
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		for _, p := range f.Places {
			occ := 0
			if p.All {
				occ = -1
			}
			out = append(out, docx.Edit{Para: p.Para, Context: p.Context, Find: p.Find, Occurrence: occ, Value: v})
		}
	}
	return out
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func validID(id string) bool {
	if len(id) != 16 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Results keeps built documents in memory until they are downloaded again or an hour passes;
// expired ones go when the next is added or asked for, without a timer.
type Results struct {
	mu    sync.Mutex
	items map[string]result
	Now   func() time.Time
}

type result struct {
	name    string
	data    []byte
	expires time.Time
}

// ResultTTL is how long a document can be downloaded.
const ResultTTL = time.Hour

const maxResults = 20

func (r *Results) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Results) purge(now time.Time) {
	for id, it := range r.items {
		if now.After(it.expires) {
			delete(r.items, id)
		}
	}
}

// Put keeps a document and returns its id.
func (r *Results) Put(name string, data []byte) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if r.items == nil {
		r.items = map[string]result{}
	}
	r.purge(now)
	// The oldest go first when the owner builds many in an hour.
	for len(r.items) >= maxResults {
		var oldest string
		for id, it := range r.items {
			if oldest == "" || it.expires.Before(r.items[oldest].expires) {
				oldest = id
			}
		}
		delete(r.items, oldest)
	}
	id := newID()
	r.items[id] = result{name: name, data: data, expires: now.Add(ResultTTL)}
	return id
}

// Get returns a kept document.
func (r *Results) Get(id string) (string, []byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.purge(r.now())
	it, ok := r.items[id]
	return it.name, it.data, ok
}

// Drop forgets a document now.
func (r *Results) Drop(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, id)
}

// FileName is the built document's name: the automation and the date, without characters that
// a file name cannot hold.
func FileName(name string, now time.Time) string {
	clean := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < ' ' {
			return ' '
		}
		return r
	}, name)
	clean = strings.Join(strings.Fields(clean), " ")
	if clean == "" {
		clean = "Документ"
	}
	return clean + " " + now.Format("2006-01-02") + ".docx"
}
