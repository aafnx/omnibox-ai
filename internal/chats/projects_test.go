package chats

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjects(t *testing.T) {
	clk := &clock{}
	p := &Projects{Dir: filepath.Join(t.TempDir(), "projects"), Now: clk.Now}
	if list, err := p.List(); err != nil || len(list) != 0 {
		t.Fatalf("empty %v %v", list, err)
	}
	x, err := p.Save(Project{Name: "  Дом\n и дача ", Emoji: "🏡", Instructions: "Отвечай кратко."})
	if err != nil || !validProjectID(x.ID) || x.Name != "Дом и дача" || x.Files == nil {
		t.Fatalf("save %+v %v", x, err)
	}
	for _, bad := range []Project{{Name: ""}, {Name: strings.Repeat("я", maxProjectName+1)}, {Name: "x", Emoji: "123456789"},
		{Name: "x", Instructions: strings.Repeat("я", maxProjectNotes+1)}, {ID: "0123456789abcdef", Name: "x"}} {
		if _, err := p.Save(bad); err == nil {
			t.Fatalf("saved %+v", bad)
		}
	}
	f, err := p.AddFile(x.ID, `C:\docs\Договор.pdf`, "pdf", 1000, "  Цена договора: 100 рублей.  ")
	if err != nil || f.Name != "Договор.pdf" || f.Chars != len([]rune("Цена договора: 100 рублей.")) {
		t.Fatalf("add %+v %v", f, err)
	}
	if _, err := p.AddFile(x.ID, "пусто.txt", "text", 1, "  "); !isInput(err) {
		t.Fatalf("empty file: %v", err)
	}
	if _, err := p.AddFile("0123456789abcdef", "a.txt", "text", 1, "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
	got, docs, err := p.Docs(x.ID)
	if err != nil || len(got.Files) != 1 || len(docs) != 1 || docs[0].Text != "Цена договора: 100 рублей." {
		t.Fatalf("docs %+v %+v %v", got, docs, err)
	}
	text := filepath.Join(p.Dir, x.ID, f.ID+".txt")
	if info, err := os.Stat(text); err != nil || (info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/') {
		t.Fatalf("text file %v %v", info, err)
	}
	// Renaming keeps the documents.
	if y, err := p.Save(Project{ID: x.ID, Name: "Дача"}); err != nil || y.Name != "Дача" || len(y.Files) != 1 || y.Emoji != "" {
		t.Fatalf("rename %+v %v", y, err)
	}
	if err := p.RemoveFile(x.ID, f.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(text); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("document text left: %v", err)
	}
	if err := p.RemoveFile(x.ID, f.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed twice: %v", err)
	}
	if _, err := p.AddFile(x.ID, "b.txt", "text", 1, "b"); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(x.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.Dir, x.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("project documents left: %v", err)
	}
	if _, err := p.Get(x.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted project found: %v", err)
	}
	if err := p.Delete("../x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}
}

func TestProjectLimits(t *testing.T) {
	p := &Projects{Dir: t.TempDir()}
	x, err := p.Save(Project{Name: "Большой"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.AddFile(x.ID, "big.txt", "text", 1, strings.Repeat("я", maxProjectChars+1)); !isInput(err) {
		t.Fatalf("too much text: %v", err)
	}
	for i := 0; i < maxProjectFiles; i++ {
		if _, err := p.AddFile(x.ID, "a.txt", "text", 1, "a"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.AddFile(x.ID, "a.txt", "text", 1, "a"); !isInput(err) {
		t.Fatalf("too many files: %v", err)
	}
	for i := 1; i < maxProjects; i++ {
		if _, err := p.Save(Project{Name: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Save(Project{Name: "p"}); !isInput(err) {
		t.Fatalf("too many projects: %v", err)
	}
	if err := p.DeleteAll(); err != nil {
		t.Fatal(err)
	}
	if list, err := p.List(); err != nil || len(list) != 0 {
		t.Fatalf("after delete all %v %v", list, err)
	}
}

func TestTemporaryChat(t *testing.T) {
	s, clk := newStore(t)
	kept := mustCreate(t, s, "Обычный", 0)
	c, err := s.Start(New{Title: "Секрет", Temporary: true})
	if err != nil || !c.Temporary {
		t.Fatalf("start %+v %v", c, err)
	}
	if _, err := s.Update(c.ID, func(c *Chat) error {
		c.Messages = append(c.Messages, Message{ID: NewMessageID(), Role: "user", Text: "тайна вклада"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(c.ID); err != nil || len(got.Messages) != 1 || !got.Summary().Temporary {
		t.Fatalf("get %+v %v", got, err)
	}
	entries, _ := os.ReadDir(s.Dir)
	if len(entries) != 1 || entries[0].Name() != kept.ID+".json" {
		t.Fatalf("temporary chat on disk: %v", entries)
	}
	if list, _ := s.List(); len(list) != 1 {
		t.Fatalf("temporary chat listed: %v", ids(list))
	}
	if hits, _ := s.Search("тайна", ""); len(hits) != 0 {
		t.Fatalf("temporary chat found: %v", hits)
	}
	if _, err := s.SetPinned(c.ID, true); !isInput(err) {
		t.Fatalf("pinned: %v", err)
	}
	if err := s.Delete(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted temporary chat: %v", err)
	}
	// One left behind (the browser closed) goes after a day, when another starts.
	old, _ := s.Start(New{Temporary: true})
	clk.Add(tempKeep + 1)
	if _, err := s.Start(New{Temporary: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(old.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old temporary chat kept: %v", err)
	}
	if n, err := s.DeleteAll(false); err != nil || n != 2 {
		t.Fatalf("delete all %d %v", n, err)
	}
}

func TestChatProjects(t *testing.T) {
	s, _ := newStore(t)
	const pid = "0123456789abcdef"
	if _, err := s.Start(New{ProjectID: "../x"}); !isInput(err) {
		t.Fatalf("bad project: %v", err)
	}
	a, err := s.Start(New{Title: "Смета", ProjectID: pid})
	if err != nil || a.ProjectID != pid || a.Summary().ProjectID != pid {
		t.Fatalf("start %+v %v", a, err)
	}
	b := mustCreate(t, s, "Отдельный", 0)
	if c, err := s.SetProject(b.ID, pid); err != nil || c.ProjectID != pid {
		t.Fatalf("set %+v %v", c, err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if _, err := s.Update(id, func(c *Chat) error {
			c.Messages = append(c.Messages, Message{ID: NewMessageID(), Role: "user", Text: "крыша протекает весной"})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.LeaveProject(pid); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if c, _ := s.Get(id); c.ProjectID != "" {
			t.Fatalf("chat kept in a deleted project: %+v", c)
		}
	}
	if _, err := s.SetProject(a.ID, pid); err != nil {
		t.Fatal(err)
	}
	if hits, _ := s.Search("крыша", pid); len(hits) != 1 || hits[0].ID != a.ID || hits[0].ProjectID != pid {
		t.Fatalf("search in project: %+v", hits)
	}
	if hits, _ := s.Search("крыша", ""); len(hits) != 2 {
		t.Fatalf("search everywhere: %+v", hits)
	}
	if _, err := s.Search("крыша", "x"); !isInput(err) {
		t.Fatalf("bad project filter: %v", err)
	}
}

func TestSearchWordsInAnyOrder(t *testing.T) {
	s, _ := newStore(t)
	c := mustCreate(t, s, "Ремонт", 0)
	other := mustCreate(t, s, "Другое", 0)
	var second string
	if _, err := s.Update(c.ID, func(c *Chat) error {
		c.Messages = append(c.Messages,
			Message{ID: NewMessageID(), Role: "user", Text: "Сколько стоит покраска стен?"},
			Message{ID: NewMessageID(), Role: "assistant", Text: "Покраска стен в Москве — от 300 рублей за метр."})
		second = c.Messages[1].ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(other.ID, func(c *Chat) error {
		c.Messages = append(c.Messages, Message{ID: NewMessageID(), Role: "user", Text: "Москва"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("москве покраска", "")
	if err != nil || len(hits) != 1 || hits[0].ID != c.ID || hits[0].MessageID != second || !strings.Contains(hits[0].Snippet, "Москве") {
		t.Fatalf("words in any order: %+v %v", hits, err)
	}
	// Words from different messages still find the chat.
	if hits, _ := s.Search("сколько рублей", ""); len(hits) != 1 || hits[0].ID != c.ID {
		t.Fatalf("words across messages: %+v", hits)
	}
	if hits, _ := s.Search("покраска крыши", ""); len(hits) != 0 {
		t.Fatalf("a missing word matched: %+v", hits)
	}
}

func TestHandlerProjects(t *testing.T) {
	h, _ := newHandler(t)
	h.Projects = &Projects{Dir: filepath.Join(t.TempDir(), "projects")}
	code, got := serve(t, h, "POST", "/api/projects", `{"action":"save","name":"Дача","emoji":"🏡","instructions":"Кратко."}`)
	if code != 200 {
		t.Fatalf("save %d %v", code, got)
	}
	pid := got["project"].(map[string]any)["id"].(string)
	data := base64.StdEncoding.EncodeToString([]byte("Смета: 500 рублей."))
	if code, got = serve(t, h, "POST", "/api/projects", `{"action":"file-add","id":"`+pid+`","name":"смета.txt","data":"`+data+`"}`); code != 200 || got["file"].(map[string]any)["kind"] != "text" {
		t.Fatalf("file-add %d %v", code, got)
	}
	fid := got["file"].(map[string]any)["id"].(string)
	for _, bad := range []string{
		`{"action":"file-add","id":"` + pid + `","name":"фото.png","data":"` + data + `"}`,
		`{"action":"file-add","id":"` + pid + `","name":"a.txt","data":"%%%"}`,
		`{"action":"save","name":""}`,
		`{"action":"save","name":"x","path":"/etc"}`,
	} {
		if code, got := serve(t, h, "POST", "/api/projects", bad); code != 400 {
			t.Fatalf("%s: %d %v", bad, code, got)
		}
	}
	code, list := serve(t, h, "GET", "/api/projects", "")
	if ps := list["projects"].([]any); code != 200 || len(ps) != 1 || len(ps[0].(map[string]any)["files"].([]any)) != 1 {
		t.Fatalf("list %d %v", code, list)
	}

	code, c := serve(t, h, "POST", "/api/chats", `{"action":"create","project_id":"`+pid+`"}`)
	if code != 200 || c["project_id"] != pid {
		t.Fatalf("create in project %d %v", code, c)
	}
	if code, got := serve(t, h, "POST", "/api/chats", `{"action":"create","project_id":"fedcba9876543210"}`); code != 404 {
		t.Fatalf("unknown project %d %v", code, got)
	}
	other := mustCreate(t, h.Chats, "Свой", 0)
	if code, got := serve(t, h, "POST", "/api/chats", `{"action":"project","id":"`+other.ID+`","project_id":"`+pid+`"}`); code != 200 || got["project_id"] != pid {
		t.Fatalf("move %d %v", code, got)
	}
	if code, got := serve(t, h, "GET", "/api/chats?q=x&project="+pid, ""); code != 200 {
		t.Fatalf("search in project %d %v", code, got)
	}
	if code, got := serve(t, h, "GET", "/api/chats?project="+pid, ""); code != 400 {
		t.Fatalf("project without q %d %v", code, got)
	}
	code, tmp := serve(t, h, "POST", "/api/chats", `{"action":"create","temporary":true}`)
	if code != 200 || tmp["temporary"] != true {
		t.Fatalf("temporary %d %v", code, tmp)
	}

	if code, got = serve(t, h, "POST", "/api/projects", `{"action":"file-remove","id":"`+pid+`","file_id":"`+fid+`"}`); code != 200 {
		t.Fatalf("file-remove %d %v", code, got)
	}
	if code, got = serve(t, h, "POST", "/api/projects", `{"action":"delete","id":"`+pid+`"}`); code != 200 || len(got["projects"].([]any)) != 0 {
		t.Fatalf("delete %d %v", code, got)
	}
	if moved, _ := h.Chats.Get(other.ID); moved.ProjectID != "" {
		t.Fatalf("chat left in a deleted project: %+v", moved)
	}
}
