package websearch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchParsesSearXNG(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/search" || q.Get("format") != "json" || q.Get("q") != "wake on lan" || q.Get("language") != "ru" {
			t.Errorf("request: %s", r.URL)
		}
		w.Write([]byte(`{"results":[{"title":"A","url":"https://www.example.com/a","content":"one\n two"},
			{"title":"dup","url":"https://www.example.com/a"},{"title":"bad","url":"javascript:alert(1)"},
			{"title":"B","url":"http://b.example/","content":"x"},{"title":"C","url":"https://c.example/"}]}`))
	}))
	defer srv.Close()
	got, err := Client{URL: srv.URL + "/"}.Search(context.Background(), " wake on lan ", "ru", 2)
	if err != nil || len(got) != 2 || got[0].Snippet != "one two" || got[1].Title != "B" {
		t.Fatalf("%+v %v", got, err)
	}
	if Host(got[0].URL) != "example.com" {
		t.Fatal(Host(got[0].URL))
	}
}

func TestSearchErrorsAreExplained(t *testing.T) {
	for code, want := range map[int]string{403: "formats", 429: "limiter", 500: "HTTP 500"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		_, err := Client{URL: srv.URL}.Search(context.Background(), "x", "", 3)
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%d: %v", code, err)
		}
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound) }))
	defer redirect.Close()
	if _, err := (Client{URL: redirect.URL}).Search(context.Background(), "x", "", 3); err == nil {
		t.Fatal("a redirect was followed")
	}
	if _, err := (Client{}).Search(context.Background(), "x", "", 3); err == nil || !strings.Contains(err.Error(), "не настроен") {
		t.Fatal(err)
	}
}
