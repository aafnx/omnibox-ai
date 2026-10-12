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
		if r.URL.Path != "/search" || q.Get("format") != "json" || q.Get("q") != "wake on lan" || q.Get("language") != "ru-RU" {
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
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/", http.StatusFound)
	}))
	defer redirect.Close()
	if _, err := (Client{URL: redirect.URL}).Search(context.Background(), "x", "", 3); err == nil {
		t.Fatal("a redirect was followed")
	}
	if _, err := (Client{}).Search(context.Background(), "x", "", 3); err == nil || !strings.Contains(err.Error(), "не настроен") {
		t.Fatal(err)
	}
}

func TestWeatherResultsKeepTheRequestedCity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"results":[
			{"title":"Погода в Кохеме", "url":"https://wrong.example", "content":"Температура сегодня +15"},
			{"title":"Температура — Википедия", "url":"https://wiki.example", "content":"Физическая величина"},
			{"title":"Прогноз погоды в Казани", "url":"https://right.example", "content":"Казань: сегодня +8"}]}`))
	}))
	defer srv.Close()
	got, err := (Client{URL: srv.URL}).Search(context.Background(), "Какая сегодня погода в Казани", "ru", 5)
	if err != nil || len(got) != 1 || got[0].URL != "https://right.example" {
		t.Fatalf("wrong-city sources survived: %+v %v", got, err)
	}
}

func TestRelevancePreservesUnmatchedForeignLanguageResults(t *testing.T) {
	input := []Result{{Title: "Weather in Kazan", URL: "https://example.com", Snippet: "8 degrees"}}
	if got := Relevant(input, "Погода в Казани"); len(got) != 1 {
		t.Fatalf("a foreign-language result was discarded: %+v", got)
	}
}

// Russian asks SearXNG for Russia's sites: plain "ru" brought sinoptik.ua for the weather in Kazan.
func TestRegion(t *testing.T) {
	for _, c := range []struct{ lang, query, want string }{
		{"ru", "погода Казань", "ru-RU"}, {"", "погода Казань", "ru-RU"}, {"", "weather Kazan", ""},
		{"", "погода у Києві", ""}, {"en", "погода", "en"},
	} {
		if got := region(c.lang, c.query); got != c.want {
			t.Errorf("%q %q: %q", c.lang, c.query, got)
		}
	}
}

// News are asked from SearXNG's news category among fresh pages; when that finds nothing the
// search widens to fresh pages of any kind, then to any page.
func TestSearXNGNewsWidenWhenEmpty(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		asked = append(asked, q.Get("categories")+"/"+q.Get("time_range"))
		if q.Get("time_range") != "" {
			w.Write([]byte(`{"results":[]}`))
			return
		}
		w.Write([]byte(`{"results":[{"title":"ИИ","url":"https://a.example/","content":"новости ИИ"}]}`))
	}))
	defer srv.Close()
	got, err := Client{URL: srv.URL, Recency: "week", News: true}.Search(context.Background(), "новости ИИ", "ru", 5)
	if err != nil || len(got) != 1 || strings.Join(asked, " ") != "news/week /week /" {
		t.Fatalf("%v %v %v", got, err, asked)
	}
}
