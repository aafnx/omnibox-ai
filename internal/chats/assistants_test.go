package chats

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssistants(t *testing.T) {
	s := &Assistants{Path: filepath.Join(t.TempDir(), "data", "assistants.json")}
	l, err := s.Load()
	if err != nil || l.Assistants == nil || len(l.Assistants) != 0 || l.Default != "" {
		t.Fatalf("empty: %+v %v", l, err)
	}
	a, err := s.Save(Assistant{Emoji: "📝", Name: "  Редактор ", Description: "Правит тексты", Instructions: "# Роль\nТы редактор.",
		Model: "cloud:openai/gpt-5", Style: "precise", WebSearch: true})
	if err != nil {
		t.Fatal(err)
	}
	if !assistantPattern.MatchString(a.ID) || a.Name != "Редактор" {
		t.Fatalf("saved %+v", a)
	}
	b, err := s.Save(Assistant{Name: "Переводчик"})
	if err != nil {
		t.Fatal(err)
	}

	for name, bad := range map[string]Assistant{
		"empty name":         {Name: "   "},
		"duplicate name":     {Name: "редактор"},
		"long name":          {Name: strings.Repeat("я", maxName+1)},
		"long description":   {Name: "x", Description: strings.Repeat("я", maxDescription+1)},
		"long instructions":  {Name: "x", Instructions: strings.Repeat("я", maxInstructions+1)},
		"bad style":          {Name: "x", Style: "wild"},
		"long emoji":         {Name: "x", Emoji: strings.Repeat("😀", 5)},
		"long model":         {Name: "x", Model: strings.Repeat("m", maxModel+1)},
		"multi-line name":    {Name: "a\nb"},
		"rename to existing": {ID: b.ID, Name: "Редактор"},
	} {
		if _, err := s.Save(bad); !isInput(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.Save(Assistant{ID: "0123456789abcdef01234567", Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if _, err := s.Save(Assistant{ID: "../x", Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bad id: %v", err)
	}

	// Editing keeps the id and may keep the same name in another case.
	b.Name, b.Style = "ПЕРЕВОДЧИК", "creative"
	if _, err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefault(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefault("0123456789abcdef01234567"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown default: %v", err)
	}
	l, _ = s.Load()
	if len(l.Assistants) != 2 || l.Default != a.ID || l.Assistants[1].Style != "creative" || l.Assistants[0].Instructions != "# Роль\nТы редактор." {
		t.Fatalf("loaded %+v", l)
	}

	if err := s.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ = s.Load(); l.Default != "" || len(l.Assistants) != 1 {
		t.Fatalf("default not cleared: %+v", l)
	}
	if err := s.Delete(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if err := s.SetDefault(b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefault(""); err != nil {
		t.Fatal(err)
	}
	if l, _ = s.Load(); l.Default != "" {
		t.Fatalf("default none: %+v", l)
	}
}

func TestAssistantsLimit(t *testing.T) {
	s := &Assistants{Path: filepath.Join(t.TempDir(), "assistants.json")}
	for i := 0; i < MaxAssistants; i++ {
		if _, err := s.Save(Assistant{Name: "a" + strings.Repeat("x", i%30) + string(rune('A'+i%26)) + string(rune('0'+i/26))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Save(Assistant{Name: "one more"}); !isInput(err) {
		t.Fatalf("limit: %v", err)
	}
}
