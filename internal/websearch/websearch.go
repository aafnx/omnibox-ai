// Package websearch asks the owner's SearXNG for web results for the panel's chat. SearXNG is
// asked only when a chat message needs a search; nothing is polled.
package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Result is one web page found.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Client is a SearXNG instance with the JSON format enabled.
type Client struct {
	URL  string
	HTTP *http.Client
}

var defaultHTTP = &http.Client{
	Timeout:       20 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second},
}

const maxResponse = 4 << 20

// Search returns up to n results; lang is "", ru or en ("" lets SearXNG decide).
func (c Client) Search(ctx context.Context, query, lang string, n int) ([]Result, error) {
	if c.URL == "" {
		return nil, errors.New("Веб-поиск не настроен: укажите адрес SearXNG в «Настройки → Веб-поиск».")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("Пустой поисковый запрос.")
	}
	if utf8.RuneCountInString(query) > 400 {
		query = string([]rune(query)[:400])
	}
	q := url.Values{"q": {query}, "format": {"json"}, "safesearch": {"0"}}
	if lang != "" {
		q.Set("language", lang)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil, errors.New("Некорректный адрес SearXNG.")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OmniBox-AI")
	client := c.HTTP
	if client == nil {
		client = defaultHTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("SearXNG не ответил: проверьте адрес и что сервер работает.")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return nil, errors.New("SearXNG не отдаёт JSON: в settings.yml добавьте formats: [html, json] и перезапустите его.")
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, errors.New("SearXNG ограничил частоту запросов (limiter). Повторите позже или отключите limiter для адреса ПК.")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("SearXNG ответил HTTP %d.", resp.StatusCode)
	}
	var body struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponse)).Decode(&body); err != nil {
		return nil, errors.New("SearXNG вернул не JSON: включите формат json в settings.yml.")
	}
	out := []Result{}
	seen := map[string]bool{}
	for _, r := range body.Results {
		u, err := url.Parse(r.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || seen[r.URL] {
			continue
		}
		seen[r.URL] = true
		out = append(out, Result{Title: clip(r.Title, 200), URL: r.URL, Snippet: clip(r.Content, 1500)})
	}
	out = Relevant(out, query)
	if n < 1 {
		n = 1
	}
	if n > 20 {
		n = 20
	}
	if len(out) > n {
		out = out[:n]
	}
	return out, nil
}

// Relevant prefers identifiable subjects (cities, products, names) to generic question words.
// If no snippets match, retain SearXNG's ranking: results may be in a different language.
func Relevant(results []Result, query string) []Result {
	stop := " какая какие какой как что где когда сегодня сейчас завтра вчера погода погоды температура температур прогноз прогнозы пожалуйста расскажи покажи найди the what how where when today weather temperature forecast "
	terms := []string{}
	for _, word := range words(query) {
		if len([]rune(word)) < 3 || strings.Contains(stop, " "+word+" ") {
			continue
		}
		terms = append(terms, stem(word))
	}
	scores := map[string]int{}
	matched := false
	for _, res := range results {
		tokens := words(res.Title + " " + res.Snippet)
		for _, term := range terms {
			for _, token := range tokens {
				if stem(token) == term {
					scores[res.URL]++
					matched = true
					break
				}
			}
		}
	}
	if !matched {
		return results
	}
	out := []Result{}
	for _, res := range results {
		if scores[res.URL] > 0 {
			out = append(out, res)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return scores[out[i].URL] > scores[out[j].URL] })
	return out
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(strings.ReplaceAll(s, "ё", "е")), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func stem(s string) string {
	s = strings.TrimSuffix(s, "ь")
	r := []rune(s)
	if len(r) > 4 && strings.ContainsRune("аяеиоуыю", r[len(r)-1]) {
		return string(r[:len(r)-1])
	}
	return s
}

// Check makes a test search and reports how long it took.
func (c Client) Check(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	_, err := c.Search(ctx, "OmniBox", "", 1)
	return time.Since(start), err
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// Host is the site's name shown under a source: www. is dropped.
func Host(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}
