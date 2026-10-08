package gateway

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
)

func TestContentSearchAndSessionExport(t *testing.T) {
	f := newFixture(t, func(c *config.Config) { c.Storage.RequestLog = "content" })
	for _, text := range []string{"Расскажи про Байкал", "Что такое GGUF?"} {
		body := map[string]any{"model": "ornith-27b", "messages": []any{map[string]any{"role": "user", "content": text}}}
		f.request("/v1/chat/completions", body, nil)
	}
	matches := f.g.SearchContent("байкал", "")
	if len(matches) != 1 || !strings.Contains(matches[0].Snippet, "Байкал") || matches[0].Model != "ornith-27b" {
		t.Fatalf("%+v", matches)
	}
	// The answer is searched too.
	if m := f.g.SearchContent("ok", ""); len(m) != 2 {
		t.Fatalf("answer text: %+v", m)
	}
	if m := f.g.SearchContent("x", ""); len(m) != 0 {
		t.Fatal("a one-letter search ran")
	}
	if m := f.g.SearchContent("байкал", "../etc"); len(m) != 0 {
		t.Fatal("a bad session id accepted")
	}
	export, ok := f.g.ExportSession(matches[0].SessionID)
	if !ok || bytes.Count(export, []byte("\n")) != 2 || !bytes.Contains(export, []byte("GGUF")) {
		t.Fatalf("%v %s", ok, export)
	}
	if _, ok := f.g.ExportSession("nope"); ok {
		t.Fatal("a bad session exported")
	}
}
