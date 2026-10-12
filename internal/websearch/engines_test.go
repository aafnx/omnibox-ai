package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Each engine is asked in its own format and its results become the same list.
func TestEnginesSearch(t *testing.T) {
	var got map[string]any
	var header http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		got = map[string]any{"path": r.URL.Path, "query": r.URL.Query().Encode()}
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			got["body"] = body
		}
		switch r.URL.Path {
		case "/search":
			if strings.HasPrefix(header.Get("Authorization"), "Bearer pplx-") {
				_, _ = w.Write([]byte(`{"id":"1","results":[{"title":"Курс евро","url":"https://cbr.example/euro","snippet":"Курс евро на 13 октября","date":"2026-10-12"},{"title":"dup","url":"https://cbr.example/euro"},{"title":"bad","url":"ftp://x"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"query":"q","results":[{"title":"Tavily","url":"https://t.example/a","content":"текст страницы","score":0.9}]}`))
		case "/res/v1/web/search":
			_, _ = w.Write([]byte(`{"web":{"results":[{"title":"Brave","url":"https://b.example/","description":"описание","extra_snippets":["ещё"],"page_age":"2026-10-11T08:00:00"}]}}`))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	res, err := Engine{Name: EnginePerplexity, Key: "pplx-0123456789abcdef", BaseURL: srv.URL}.Search(ctx, "курс евро", "ru", 5)
	if err != nil || len(res) != 1 || res[0].Snippet != "(2026-10-12) Курс евро на 13 октября" {
		t.Fatalf("perplexity %v %+v", err, res)
	}
	body := got["body"].(map[string]any)
	if body["query"] != "курс евро" || body["max_results"] != 5.0 || body["search_language_filter"].([]any)[0] != "ru" {
		t.Fatalf("perplexity request %v", body)
	}
	res, err = Engine{Name: EngineTavily, Key: "tvly-0123456789abcdef", BaseURL: srv.URL}.Search(ctx, "q", "", 3)
	if err != nil || len(res) != 1 || res[0].Snippet != "текст страницы" || header.Get("Authorization") != "Bearer tvly-0123456789abcdef" {
		t.Fatalf("tavily %v %+v", err, res)
	}
	res, err = Engine{Name: EngineBrave, Key: "BSA0123456789abcdef", BaseURL: srv.URL}.Search(ctx, "q", "ru", 3)
	if err != nil || len(res) != 1 || res[0].Snippet != "(2026-10-11) описание … ещё" || header.Get("X-Subscription-Token") != "BSA0123456789abcdef" ||
		!strings.Contains(got["query"].(string), "search_lang=ru") || !strings.Contains(got["query"].(string), "count=3") {
		t.Fatalf("brave %v %+v %v", err, res, got)
	}
	// Fresh news: each engine in its own words.
	Engine{Name: EnginePerplexity, Key: "pplx-0123456789abcdef", BaseURL: srv.URL, Recency: "week", News: true}.Search(ctx, "q", "", 3)
	if body := got["body"].(map[string]any); body["search_recency_filter"] != "week" {
		t.Fatalf("perplexity recency %v", body)
	}
	Engine{Name: EngineTavily, Key: "tvly-0123456789abcdef", BaseURL: srv.URL, Recency: "day", News: true}.Search(ctx, "q", "", 3)
	if body := got["body"].(map[string]any); body["topic"] != "news" || body["time_range"] != "day" {
		t.Fatalf("tavily news %v", body)
	}
	Engine{Name: EngineBrave, Key: "BSA0123456789abcdef", BaseURL: srv.URL, Recency: "month"}.Search(ctx, "q", "", 3)
	if !strings.Contains(got["query"].(string), "freshness=pm") {
		t.Fatalf("brave freshness %v", got)
	}
	if _, err := (Engine{Name: EngineBrave, BaseURL: srv.URL}).Search(ctx, "q", "", 3); err == nil || !strings.Contains(err.Error(), "Нет ключа Brave Search") {
		t.Fatalf("no key: %v", err)
	}
}

// A refusal is explained without the engine's text (it could quote the key).
func TestEngineErrors(t *testing.T) {
	status := 401
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"detail":{"error":"bad key tvly-0123456789abcdef"}}`))
	}))
	defer srv.Close()
	for code, want := range map[int]string{401: "не принял ключ", 432: "закончились запросы", 429: "ограничил частоту", 500: "HTTP 500"} {
		status = code
		_, err := Engine{Name: EngineTavily, Key: "tvly-0123456789abcdef", BaseURL: srv.URL}.Search(context.Background(), "q", "", 3)
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "tvly-") {
			t.Errorf("%d: %v", code, err)
		}
	}
}

func TestKeysAreKeptEncryptedAndHinted(t *testing.T) {
	k := Keys{Path: filepath.Join(t.TempDir(), "run", "search-keys.bin")}
	if err := k.Save(EngineTavily, "sk-wrong-prefix-123456", time.Now()); err == nil {
		t.Fatal("a Tavily key starts with tvly-")
	}
	if err := k.Save("searxng", "anything-long-enough-1", time.Now()); err == nil {
		t.Fatal("SearXNG has no key")
	}
	if err := k.Save(EnginePerplexity, " pplx-0123456789abcdefWXYZ ", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := k.Save(EngineBrave, "BSA0123456789abcdef", time.Now()); err != nil {
		t.Fatal(err)
	}
	if k.Get(EnginePerplexity) != "pplx-0123456789abcdefWXYZ" || k.Info()[EnginePerplexity].Hint != "…WXYZ" || len(k.Info()) != 2 {
		t.Fatalf("%v", k.Info())
	}
	info, _ := json.Marshal(k.Info())
	if strings.Contains(string(info), "pplx-") {
		t.Fatal("the key reached the panel")
	}
	_ = k.Save(EnginePerplexity, "", time.Now())
	_ = k.Save(EngineBrave, "", time.Now())
	if len(k.Info()) != 0 || k.Get(EngineBrave) != "" {
		t.Fatal("keys not forgotten")
	}
}
