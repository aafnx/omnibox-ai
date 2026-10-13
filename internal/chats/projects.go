package chats

import (
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
	"unicode/utf8"

	"github.com/aafnx/omnibox-ai/internal/platform"
)

// A project groups chats around one matter: its instructions and its documents come with every
// chat in it. Projects and the text of their documents are kept on the PC, readable only by the
// service, until the owner removes them; the files themselves are not kept, only their text.

const (
	maxProjects        = 50
	maxProjectFiles    = 50
	maxProjectName     = 80
	maxProjectNotes    = 8000
	maxProjectChars    = 3_000_000 // the text of all of a project's documents
	projectsFile       = "projects.json"
	projectFileTextExt = ".txt"
)

var projectPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

func validProjectID(id string) bool { return projectPattern.MatchString(id) }

// Project is one project with the list of its documents.
type Project struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Emoji        string        `json:"emoji"`
	Instructions string        `json:"instructions"`
	Files        []ProjectFile `json:"files"`
	Created      time.Time     `json:"created"`
	Updated      time.Time     `json:"updated"`
}

// ProjectFile is a document of a project; its text is kept beside the list.
type ProjectFile struct {
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Kind  string    `json:"kind"`
	Size  int64     `json:"size"`
	Chars int       `json:"chars"`
	Added time.Time `json:"added"`
}

// Doc is a document's name and text, as the model reads it.
type Doc struct {
	Name string
	Text string
}

// Projects keeps the projects in Dir: the list in projects.json, each document's text in
// <project>/<file>.txt.
type Projects struct {
	Dir string
	Now func() time.Time

	mu sync.Mutex
}

var errProjectNotFound = notFound("Проект не найден: возможно, он удалён.")

func (p *Projects) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func (p *Projects) load() ([]Project, error) {
	data, err := platform.ReadShared(filepath.Join(p.Dir, projectsFile))
	if errors.Is(err, os.ErrNotExist) {
		return []Project{}, nil
	}
	if err != nil {
		return nil, errors.New("Список проектов не прочитан.")
	}
	var list []Project
	if json.Unmarshal(data, &list) != nil {
		return nil, errors.New("Список проектов повреждён.")
	}
	for i := range list {
		if list[i].Files == nil {
			list[i].Files = []ProjectFile{}
		}
	}
	return list, nil
}

func (p *Projects) save(list []Project) error {
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(list, "", "  ")
	return platform.WriteAtomic(filepath.Join(p.Dir, projectsFile), data, 0o600)
}

// List returns the projects by name.
func (p *Projects) List() ([]Project, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	list, err := p.load()
	slices.SortFunc(list, func(a, b Project) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return list, err
}

// Get returns one project.
func (p *Projects) Get(id string) (Project, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	list, err := p.load()
	if err != nil {
		return Project{}, err
	}
	i := slices.IndexFunc(list, func(x Project) bool { return x.ID == id })
	if i < 0 {
		return Project{}, errProjectNotFound
	}
	return list[i], nil
}

// Save creates a project (without an ID) or changes its name, emoji and instructions.
func (p *Projects) Save(x Project) (Project, error) {
	x.Name = cleanTitle(x.Name)
	x.Emoji = strings.TrimSpace(x.Emoji)
	x.Instructions = strings.TrimSpace(x.Instructions)
	switch {
	case x.Name == "" || runes(x.Name) > maxProjectName:
		return Project{}, errorf("Название проекта: от 1 до %d символов.", maxProjectName)
	case runes(x.Emoji) > 8:
		return Project{}, errorf("Значок — один эмодзи.")
	case runes(x.Instructions) > maxProjectNotes:
		return Project{}, errorf("Указания проекта: не больше %d символов.", maxProjectNotes)
	case hasControl(x.Name):
		return Project{}, errorf("Название проекта содержит управляющие символы.")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	list, err := p.load()
	if err != nil {
		return Project{}, err
	}
	now := p.now()
	if x.ID == "" {
		if len(list) >= maxProjects {
			return Project{}, errorf("Не больше %d проектов.", maxProjects)
		}
		x.ID, x.Created, x.Updated, x.Files = randomHex(8), now, now, []ProjectFile{}
		list = append(list, x)
		return x, p.save(list)
	}
	i := slices.IndexFunc(list, func(y Project) bool { return y.ID == x.ID })
	if i < 0 {
		return Project{}, errProjectNotFound
	}
	list[i].Name, list[i].Emoji, list[i].Instructions, list[i].Updated = x.Name, x.Emoji, x.Instructions, now
	return list[i], p.save(list)
}

// Delete removes a project and the text of its documents at once, without a copy.
func (p *Projects) Delete(id string) error {
	if !validProjectID(id) {
		return errProjectNotFound
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	list, err := p.load()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list, func(x Project) bool { return x.ID == id })
	if i < 0 {
		return errProjectNotFound
	}
	if err := p.save(slices.Delete(list, i, i+1)); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(p.Dir, id)); err != nil {
		return fmt.Errorf("Документы проекта не удалены: %v", err)
	}
	return nil
}

// DeleteAll removes every project and its documents.
func (p *Projects) DeleteAll() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := os.RemoveAll(p.Dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// AddFile keeps the text of a document in a project.
func (p *Projects) AddFile(id, name, kind string, size int64, text string) (ProjectFile, error) {
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	text = strings.TrimSpace(text)
	if name == "" || runes(name) > 200 {
		return ProjectFile{}, errorf("Имя файла: от 1 до 200 символов.")
	}
	if text == "" {
		return ProjectFile{}, errorf("«%s»: в файле нет текста.", name)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	list, err := p.load()
	if err != nil {
		return ProjectFile{}, err
	}
	i := slices.IndexFunc(list, func(x Project) bool { return x.ID == id })
	if i < 0 {
		return ProjectFile{}, errProjectNotFound
	}
	total := utf8.RuneCountInString(text)
	for _, f := range list[i].Files {
		total += f.Chars
	}
	switch {
	case len(list[i].Files) >= maxProjectFiles:
		return ProjectFile{}, errorf("В проекте не больше %d документов.", maxProjectFiles)
	case total > maxProjectChars:
		return ProjectFile{}, errorf("Документы проекта вместе — не больше %d млн знаков текста.", maxProjectChars/1_000_000)
	}
	f := ProjectFile{ID: randomHex(8), Name: name, Kind: kind, Size: size, Chars: utf8.RuneCountInString(text), Added: p.now()}
	dir := filepath.Join(p.Dir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ProjectFile{}, err
	}
	if err := platform.WriteAtomic(filepath.Join(dir, f.ID+projectFileTextExt), []byte(text), 0o600); err != nil {
		return ProjectFile{}, fmt.Errorf("Документ не сохранён: %v", err)
	}
	list[i].Files = append(list[i].Files, f)
	list[i].Updated = p.now()
	if err := p.save(list); err != nil {
		_ = os.Remove(filepath.Join(dir, f.ID+projectFileTextExt))
		return ProjectFile{}, err
	}
	return f, nil
}

// RemoveFile removes a document of a project at once.
func (p *Projects) RemoveFile(id, fileID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	list, err := p.load()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list, func(x Project) bool { return x.ID == id })
	if i < 0 {
		return errProjectNotFound
	}
	j := slices.IndexFunc(list[i].Files, func(f ProjectFile) bool { return f.ID == fileID })
	if j < 0 || !validProjectID(fileID) {
		return notFound("Документ не найден.")
	}
	list[i].Files = slices.Delete(list[i].Files, j, j+1)
	list[i].Updated = p.now()
	if err := p.save(list); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(p.Dir, id, fileID+projectFileTextExt)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("Документ не удалён: %v", err)
	}
	return nil
}

// Docs are the documents of a project with their text; one that cannot be read is skipped.
func (p *Projects) Docs(id string) (Project, []Doc, error) {
	x, err := p.Get(id)
	if err != nil {
		return Project{}, nil, err
	}
	var docs []Doc
	for _, f := range x.Files {
		data, err := platform.ReadShared(filepath.Join(p.Dir, id, f.ID+projectFileTextExt))
		if err == nil {
			docs = append(docs, Doc{Name: f.Name, Text: string(data)})
		}
	}
	return x, docs, nil
}
