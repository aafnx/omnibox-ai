// Package automations keeps the owner's automations: steps run one after another — a task in the
// owner's words, fields taken from the data, a Word template filled with them — each with its
// model and what it may use. A run's data comes with the request and is not written to disk; its
// result is kept only in an automation whose history is on, and a built document stays in memory
// until it is downloaded again or an hour passes.
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
	maxSteps       = 20
)

// Kinds of automation: a task answered in text, fields taken from the data, or a Word template
// filled with them.
const (
	KindTask     = "task"
	KindFields   = "fields"
	KindDocument = "document"
)

// maxPrompt bounds a task's own words.
const maxPrompt = 20000

// Automation is one saved scenario.
type Automation struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Emoji string `json:"emoji"`
	// Kind is the last step's kind: what the run gives back. Set from the steps.
	Kind string `json:"kind"`
	// Private keeps everything on the PC: a local model only, no search, no history, no schedule.
	Private bool `json:"private"`
	// Model is the model key the steps run with unless a step has its own; "" means the model
	// chosen in the chat.
	Model string `json:"model"`
	// Steps run in order; each sees the run's data and the results of the steps before it.
	Steps []Step `json:"steps"`
	// Prompt and Fields are a one-step automation as 0.29 sent and kept it before steps: they
	// become its step when it is read or saved.
	Prompt string  `json:"prompt,omitempty"`
	Fields []Field `json:"fields,omitempty"`
	// Instructions are the owner's notes for the model, for every step: formats, names, what to
	// leave blank.
	Instructions string `json:"instructions"`
	// Web and Read tell whether any step searches the web or opens the pages found. Set from the
	// steps.
	Web      bool      `json:"web,omitempty"`
	Read     bool      `json:"read,omitempty"`
	Template *Template `json:"template,omitempty"`
	// History keeps the runs' results on the PC, for as long as chats are kept. Never when private.
	History bool `json:"history,omitempty"`
	// Schedule runs a task by itself; Vars are its values of the prompt's {{names}}.
	Schedule *Schedule         `json:"schedule,omitempty"`
	Vars     map[string]string `json:"vars,omitempty"`
	Created  time.Time         `json:"created"`
	Updated  time.Time         `json:"updated"`
}

// Step is one stage of an automation.
type Step struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	// Prompt is the step's task in the owner's words; {{name}} in it is asked at each run (Vars).
	Prompt string `json:"prompt,omitempty"`
	// Model is the step's own model; "" means the automation's.
	Model string `json:"model,omitempty"`
	// Web lets the step search the web; Read lets it open the pages found. Never when private.
	Web  bool `json:"web,omitempty"`
	Read bool `json:"read,omitempty"`
	// Fields are what a fields step takes from the data; a document step's are its template's.
	Fields []Field `json:"fields,omitempty"`
}

// Title is how the step is named to the owner and to the next steps.
func (s Step) Title(i int) string {
	if s.Name != "" {
		return s.Name
	}
	return fmt.Sprintf("Шаг %d", i+1)
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
	Places []Place `json:"places,omitempty"`
	// Check is how a value is verified: one of Checks.
	Check string `json:"check,omitempty"`
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
	// OnChange is told after an automation is saved or deleted: the schedule is set again.
	OnChange func()
	mu       sync.Mutex
}

func (s *Store) changed() {
	if s.OnChange != nil {
		s.OnChange()
	}
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
	for i := range f.Automations {
		f.Automations[i].oneStep()
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
	defer s.changed()
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
	defer s.changed()
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
	if err := os.RemoveAll(s.runsDir(id)); err != nil {
		return fmt.Errorf("История не удалена: %v", err)
	}
	return nil
}

// StepFields are the fields a step fills: a fields step's own, a document step's template's.
func (a Automation) StepFields(i int) []Field {
	if a.Steps[i].Kind == KindDocument && a.Template != nil {
		return a.Template.Fields
	}
	return a.Steps[i].Fields
}

// FieldList are the fields of the last step: what the run gives back.
func (a Automation) FieldList() []Field {
	if len(a.Steps) == 0 {
		return nil
	}
	return a.StepFields(len(a.Steps) - 1)
}

// VarNames are the {{names}} of all the steps, each once, in order.
func (a Automation) VarNames() []string {
	var out []string
	for _, s := range a.Steps {
		for _, v := range Vars(s.Prompt) {
			if !contains(out, v) {
				out = append(out, v)
			}
		}
	}
	return out
}

// oneStep turns an automation kept before steps into its one step.
func (a *Automation) oneStep() {
	if len(a.Steps) > 0 || a.Kind == "" {
		return
	}
	a.Steps = []Step{{Kind: a.Kind, Prompt: a.Prompt, Web: a.Web, Read: a.Read, Fields: a.Fields}}
	a.Prompt, a.Fields = "", nil
}

// check validates what the panel sent and applies the rules of privacy; the places of a
// template's fields are checked against it elsewhere.
func check(a *Automation) error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || utf8.RuneCountInString(a.Name) > maxName {
		return fmt.Errorf("Название: от 1 до %d символов.", maxName)
	}
	if utf8.RuneCountInString(a.Emoji) > 8 {
		return errors.New("Значок — один эмодзи.")
	}
	if utf8.RuneCountInString(a.Instructions) > maxNotes {
		return fmt.Errorf("Указания для модели: не больше %d символов.", maxNotes)
	}
	a.oneStep()
	if len(a.Steps) == 0 || len(a.Steps) > maxSteps {
		return fmt.Errorf("Шагов: от 1 до %d.", maxSteps)
	}
	a.Web, a.Read = false, false
	document := false
	for i := range a.Steps {
		st := &a.Steps[i]
		st.Name = strings.TrimSpace(st.Name)
		if utf8.RuneCountInString(st.Name) > maxName {
			return fmt.Errorf("Название шага: не больше %d символов.", maxName)
		}
		st.Prompt = strings.TrimSpace(st.Prompt)
		if utf8.RuneCountInString(st.Prompt) > maxPrompt {
			return fmt.Errorf("%s: задача не больше %d символов.", st.Title(i), maxPrompt)
		}
		var fields []Field
		switch st.Kind {
		case KindTask:
			if st.Prompt == "" {
				return fmt.Errorf("%s: опишите задачу — что модель должна сделать.", st.Title(i))
			}
			st.Fields = nil
		case KindFields:
			if len(st.Fields) == 0 {
				return fmt.Errorf("%s: добавьте хотя бы одно поле.", st.Title(i))
			}
			fields = st.Fields
		case KindDocument:
			// The owner checks the values before the document is built: nothing comes after it.
			if i != len(a.Steps)-1 {
				return errors.New("Документ по шаблону — последний шаг: его значения вы проверяете перед сборкой.")
			}
			if a.Template == nil {
				return errors.New("Загрузите шаблон Word.")
			}
			st.Fields, document = nil, true
			fields = a.Template.Fields
		default:
			return errors.New("Неизвестный вид шага.")
		}
		if err := checkFields(fields, st.Kind); err != nil {
			return err
		}
		// Private means nothing leaves the PC.
		if a.Private {
			st.Web = false
		}
		if !st.Web {
			st.Read = false
		}
		a.Web, a.Read = a.Web || st.Web, a.Read || st.Read
	}
	if !document {
		a.Template = nil
	}
	a.Kind = a.Steps[len(a.Steps)-1].Kind
	// Private means nothing of a run stays on the PC either.
	if a.Private {
		a.History = false
		if a.Schedule != nil {
			a.Schedule.Enabled = false
		}
	}
	if len(a.Vars) > 20 {
		return errors.New("Не больше 20 переменных.")
	}
	for k, v := range a.Vars {
		if utf8.RuneCountInString(k) > maxName || utf8.RuneCountInString(v) > maxHint {
			return errors.New("Значение переменной слишком длинное.")
		}
	}
	if a.Schedule != nil {
		if err := a.Schedule.check(); err != nil {
			return err
		}
		if a.Schedule.Enabled {
			switch {
			case a.Steps[0].Kind != KindTask || document:
				return errors.New("По расписанию запускаются автоматизации, которые начинаются с задачи и не собирают документ: полям нужны данные, документу — ваша проверка.")
			case !a.History:
				return errors.New("Для запуска по расписанию включите историю запусков: результат нужно где-то сохранить.")
			}
			for i, st := range a.Steps {
				if st.Model == "" && a.Model == "" {
					return fmt.Errorf("Для запуска по расписанию выберите модель (%s): в это время в чате может быть выбрана любая.", st.Title(i))
				}
			}
			for _, v := range a.VarNames() {
				if strings.TrimSpace(a.Vars[v]) == "" {
					return fmt.Errorf("Для запуска по расписанию задайте значение «%s».", v)
				}
			}
		}
	}
	return nil
}

// checkFields validates a step's fields: names, hints, checks, and a template's places.
func checkFields(fields []Field, kind string) error {
	if len(fields) > maxFields {
		return fmt.Errorf("Не больше %d полей.", maxFields)
	}
	seen := map[string]bool{}
	for i := range fields {
		f := &fields[i]
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
		if !slices.Contains(Checks, f.Check) {
			return fmt.Errorf("Поле «%s»: неизвестная проверка.", f.Name)
		}
		if kind == KindDocument && (len(f.Places) == 0 || len(f.Places) > maxPlaces) {
			return fmt.Errorf("Поле «%s»: от 1 до %d мест в документе.", f.Name, maxPlaces)
		}
		if kind == KindFields {
			f.Places = nil
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
