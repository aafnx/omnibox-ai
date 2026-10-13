package chats

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	return &Store{Dir: filepath.Join(t.TempDir(), "chats"), Now: c.Now}, c
}

func mustCreate(t *testing.T, s *Store, title string, keep int) *Chat {
	t.Helper()
	c, err := s.Create(title, keep, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ids(list []Summary) string {
	var out []string
	for _, s := range list {
		out = append(out, s.Title)
	}
	return strings.Join(out, ",")
}

func TestCreateGetSave(t *testing.T) {
	s, clk := newStore(t)
	c, err := s.Create("  Первый\nчат ", 24, "0123456789abcdef01234567", "local:prismml:bonsai.gguf")
	if err != nil {
		t.Fatal(err)
	}
	if !chatID.MatchString(c.ID) || c.Title != "Первый чат" || c.KeepHours != 24 || !c.TimerStart.Equal(clk.Now()) || c.Messages == nil {
		t.Fatalf("created %+v", c)
	}
	if info, err := os.Stat(filepath.Join(s.Dir, c.ID+".json")); err != nil || (info.Mode().Perm() != 0o600 && os.PathSeparator == '/') {
		t.Fatalf("file %v %v", info, err)
	}
	if d, _ := s.Create("", 0, "", ""); d.Title != DefaultTitle {
		t.Fatalf("default title %q", d.Title)
	}

	clk.Add(time.Minute)
	c.Messages = append(c.Messages,
		Message{Role: "user", Text: "Привет", Attachments: []Attachment{{Name: "a.png", Kind: "image", Size: 10, Tokens: 5}}},
		Message{Role: "assistant", Text: "Здравствуй", Meta: &Meta{CostUSD: 0.25, Sources: []Source{{Title: "x", URL: "https://x"}}}},
		Message{Role: "assistant", Text: "Ещё", Meta: &Meta{CostUSD: 0.5}})
	c.Pinned, c.KeepHours = true, 999 // retention fields change only through SetPinned
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	if c.CostUSD != 0.75 || !c.Updated.Equal(clk.Now()) || c.Pinned || c.KeepHours != 24 {
		t.Fatalf("saved %+v", c)
	}
	got, err := s.Get(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 3 || !messageID.MatchString(got.Messages[0].ID) || got.Messages[0].Created.IsZero() ||
		got.Messages[1].Meta.Sources[0].URL != "https://x" || got.CostUSD != 0.75 {
		t.Fatalf("got %+v", got)
	}

	bad := *got
	bad.Messages = []Message{{Role: "system", Text: "x"}}
	if err := s.Save(&bad); !isInput(err) {
		t.Fatalf("bad role: %v", err)
	}
	bad.Messages = []Message{{Role: "user", Attachments: []Attachment{{Kind: "exe"}}}}
	if err := s.Save(&bad); !isInput(err) {
		t.Fatalf("bad attachment: %v", err)
	}
	if _, err := s.Create(strings.Repeat("я", MaxTitle+1), 0, "", ""); !isInput(err) {
		t.Fatalf("long title: %v", err)
	}
	if _, err := s.Create("x", -1, "", ""); !isInput(err) {
		t.Fatalf("negative keep: %v", err)
	}

	// A chat deleted while an answer was generated is not brought back by saving it.
	if err := s.Delete(c.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(got); !errors.Is(err, ErrNotFound) {
		t.Fatalf("save deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, c.ID+".json")); !os.IsNotExist(err) {
		t.Fatal("deleted chat file remains")
	}
	if err := s.Delete(c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestChatTooLarge(t *testing.T) {
	s, _ := newStore(t)
	c := mustCreate(t, s, "big", 0)
	c.Messages = []Message{{Role: "user", Text: strings.Repeat("a", MaxChatBytes)}}
	if err := s.Save(c); !isInput(err) || !strings.Contains(err.Error(), "8 МБ") {
		t.Fatalf("too large: %v", err)
	}
	if got, _ := s.Get(c.ID); len(got.Messages) != 0 {
		t.Fatal("oversized chat was written")
	}
}

func TestIDValidation(t *testing.T) {
	s, _ := newStore(t)
	outside := filepath.Join(filepath.Dir(s.Dir), "x.json")
	if err := os.WriteFile(outside, []byte(`{"id":"../x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../x", "..\\x", "", "0123456789ABCDEF01234567", "0123456789abcdef0123456", "0123456789abcdef01234567/"} {
		if _, err := s.Get(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get(%q): %v", id, err)
		}
		if err := s.Delete(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Delete(%q): %v", id, err)
		}
		if _, err := s.Rename(id, "x"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Rename(%q): %v", id, err)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("file outside the chat directory was touched")
	}
	c := mustCreate(t, s, "x", 0)
	if _, err := s.DeleteMessage(c.ID, "../x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteMessage: %v", err)
	}
}

func TestRetention(t *testing.T) {
	s, clk := newStore(t)
	short := mustCreate(t, s, "short", 1)
	forever := mustCreate(t, s, "forever", 0)
	pinned := mustCreate(t, s, "pinned", 1)
	clk.Add(time.Second)
	mustCreate(t, s, "long", 48)
	if _, err := s.SetPinned(pinned.ID, true); err != nil {
		t.Fatal(err)
	}
	if e := short.Expires(); e == nil || !e.Equal(short.Created.Add(time.Hour)) {
		t.Fatalf("expires %v", e)
	}
	if forever.Expires() != nil {
		t.Fatal("keep_hours 0 must not expire")
	}

	clk.Add(30 * time.Minute)
	if n := s.Cleanup(clk.Now()); n != 0 {
		t.Fatalf("removed %d too early", n)
	}
	clk.Add(31 * time.Minute)
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if ids(list) != "pinned,long,forever" {
		t.Fatalf("after expiry: %s", ids(list))
	}
	if list[0].Expires != nil || list[1].Expires == nil {
		t.Fatalf("summaries %+v", list)
	}
	if _, err := s.Get(short.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired chat remains: %v", err)
	}

	// Pinned for a long time, then unpinned: its own hour starts again from the unpinning.
	clk.Add(100 * time.Hour)
	if n := s.Cleanup(clk.Now()); n != 1 { // long
		t.Fatalf("removed %d", n)
	}
	c, err := s.SetPinned(pinned.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if c.KeepHours != 1 || !c.TimerStart.Equal(clk.Now()) || !c.Expires().Equal(clk.Now().Add(time.Hour)) {
		t.Fatalf("unpinned %+v", c)
	}
	clk.Add(59 * time.Minute)
	if n := s.Cleanup(clk.Now()); n != 0 {
		t.Fatalf("unpinned chat removed early: %d", n)
	}
	clk.Add(time.Minute)
	if n := s.Cleanup(clk.Now()); n != 1 {
		t.Fatalf("unpinned chat not removed: %d", n)
	}
	list, _ = s.List()
	if ids(list) != "forever" {
		t.Fatalf("left %s", ids(list))
	}
}

func TestCleanupRemovesStaleTemporaryFiles(t *testing.T) {
	s, clk := newStore(t)
	mustCreate(t, s, "x", 0)
	stale := filepath.Join(s.Dir, "0123456789abcdef01234567.json.tmp-1")
	fresh := filepath.Join(s.Dir, "0123456789abcdef01234567.json.tmp-2")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := clk.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, clk.Now(), clk.Now()); err != nil {
		t.Fatal(err)
	}
	s.Cleanup(clk.Now())
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale temporary file remains")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("temporary file of a write in progress removed")
	}
}

func TestListOrderAndRename(t *testing.T) {
	s, clk := newStore(t)
	a := mustCreate(t, s, "a", 0)
	clk.Add(time.Second)
	b := mustCreate(t, s, "b", 0)
	clk.Add(time.Second)
	mustCreate(t, s, "c", 0)
	clk.Add(time.Second)
	if _, err := s.Rename(a.ID, "a2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPinned(b.ID, true); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if ids(list) != "b,a2,c" {
		t.Fatalf("order %s", ids(list))
	}
	if _, err := s.Rename(a.ID, "   "); !isInput(err) {
		t.Fatalf("empty rename: %v", err)
	}
	empty := &Store{Dir: filepath.Join(t.TempDir(), "none")}
	if list, err := empty.List(); err != nil || list == nil || len(list) != 0 {
		t.Fatalf("missing dir: %v %v", list, err)
	}
}

func TestDeleteAll(t *testing.T) {
	s, _ := newStore(t)
	mustCreate(t, s, "a", 0)
	p := mustCreate(t, s, "p", 0)
	mustCreate(t, s, "b", 0)
	if _, err := s.SetPinned(p.ID, true); err != nil {
		t.Fatal(err)
	}
	n, err := s.DeleteAll(true)
	if err != nil || n != 2 {
		t.Fatalf("keep pinned: %d %v", n, err)
	}
	if list, _ := s.List(); ids(list) != "p" {
		t.Fatalf("left %s", ids(list))
	}
	mustCreate(t, s, "c", 0)
	n, err = s.DeleteAll(false)
	if err != nil || n != 2 {
		t.Fatalf("all: %d %v", n, err)
	}
	entries, _ := os.ReadDir(s.Dir)
	if len(entries) != 0 {
		t.Fatalf("files remain: %v", entries)
	}
}

func TestDeleteMessage(t *testing.T) {
	s, _ := newStore(t)
	c := mustCreate(t, s, "x", 0)
	c.Messages = []Message{
		{Role: "user", Text: "1"},
		{Role: "assistant", Text: "2", Meta: &Meta{CostUSD: 1}},
		{Role: "user", Text: "3"},
	}
	if err := s.Save(c); err != nil {
		t.Fatal(err)
	}
	got, err := s.DeleteMessage(c.ID, c.Messages[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[1].Text != "3" || got.CostUSD != 0 {
		t.Fatalf("after delete %+v", got)
	}
	data, _ := os.ReadFile(filepath.Join(s.Dir, c.ID+".json"))
	if strings.Contains(string(data), c.Messages[1].ID) {
		t.Fatal("deleted message remains in the file")
	}
	if _, err := s.DeleteMessage(c.ID, c.Messages[1].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestSearch(t *testing.T) {
	s, clk := newStore(t)
	a := mustCreate(t, s, "Рецепт борща", 0)
	clk.Add(time.Second)
	b := mustCreate(t, s, "Код", 0)
	b.Messages = []Message{
		{Role: "user", Text: "Как написать функцию на Go?"},
		{Role: "assistant", Text: strings.Repeat("слово ", 20) + "БОРЩ готов\nк подаче " + strings.Repeat("ещё ", 20)},
	}
	if err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	clk.Add(time.Second)
	mustCreate(t, s, "Другое", 0)

	hits, err := s.Search("  борщ ", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].ID != b.ID || hits[1].ID != a.ID {
		t.Fatalf("hits %+v", hits)
	}
	if hits[0].MessageID != b.Messages[1].ID || !strings.HasPrefix(hits[0].Snippet, "…") || !strings.HasSuffix(hits[0].Snippet, "…") ||
		!strings.Contains(hits[0].Snippet, "БОРЩ готов к подаче") {
		t.Fatalf("message hit %+v", hits[0])
	}
	if hits[1].MessageID != "" || hits[1].Snippet != "Рецепт борща" {
		t.Fatalf("title hit %+v", hits[1])
	}
	if hits, _ := s.Search("нет такого", ""); hits == nil || len(hits) != 0 {
		t.Fatalf("no hits %v", hits)
	}
	if _, err := s.Search(strings.Repeat("x", maxQuery+1), ""); !isInput(err) {
		t.Fatalf("long query: %v", err)
	}
	for i := 0; i < maxSearchHits+5; i++ {
		mustCreate(t, s, "борщ", 0)
	}
	if hits, _ := s.Search("борщ", ""); len(hits) != maxSearchHits {
		t.Fatalf("limit: %d", len(hits))
	}
}

func TestConcurrentUpdates(t *testing.T) {
	s, _ := newStore(t)
	c := mustCreate(t, s, "x", 0)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Update(c.ID, func(c *Chat) error {
				c.Messages = append(c.Messages, Message{Role: "user", Text: "x"})
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, err := s.Get(c.ID)
	if err != nil || len(got.Messages) != 20 {
		t.Fatalf("lost updates: %v %v", len(got.Messages), err)
	}
}
