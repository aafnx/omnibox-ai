package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Search engines with an API key, besides the owner's SearXNG. Each is asked only when a chat
// message needs a search, with the query alone; their keys are kept encrypted (keys.go) and
// never leave for another address: redirects are not followed.
const (
	EngineSearXNG    = ""
	EnginePerplexity = "perplexity"
	EngineTavily     = "tavily"
	EngineBrave      = "brave"
	// EngineSonar is Perplexity Sonar through the owner's OpenRouter key (package openrouter).
	EngineSonar = "sonar"
)

// KeyEngines are the engines whose key is kept here.
var KeyEngines = []string{EnginePerplexity, EngineTavily, EngineBrave}

// EngineName is an engine's name for people.
func EngineName(engine string) string {
	switch engine {
	case EnginePerplexity:
		return "Perplexity"
	case EngineTavily:
		return "Tavily"
	case EngineBrave:
		return "Brave Search"
	case EngineSonar:
		return "Perplexity Sonar"
	}
	return "SearXNG"
}

// Engine is a search engine with a key.
type Engine struct {
	Name string
	Key  string
	// BaseURL replaces the engine's address in tests.
	BaseURL string
	HTTP    *http.Client
}

var engineHTTP = &http.Client{
	Timeout:       25 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second},
}

// snippetLimit is longer than SearXNG's: these engines return the relevant text of the page.
const snippetLimit = 3000

func (e Engine) base(def string) string {
	if e.BaseURL != "" {
		return strings.TrimRight(e.BaseURL, "/")
	}
	return def
}

// Search returns up to n results; lang is "", ru or en.
func (e Engine) Search(ctx context.Context, query, lang string, n int) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("Пустой поисковый запрос.")
	}
	if e.Key == "" {
		return nil, fmt.Errorf("Нет ключа %s: добавьте его в «Настройки → Веб-поиск».", EngineName(e.Name))
	}
	query = clip(query, 380)
	n = min(max(n, 1), 20)
	var req *http.Request
	var err error
	switch e.Name {
	case EnginePerplexity:
		body := map[string]any{"query": query, "max_results": n, "max_tokens_per_page": 1024}
		if lang != "" {
			body["search_language_filter"] = []string{lang}
		}
		req, err = jsonRequest(ctx, e.base("https://api.perplexity.ai")+"/search", body)
		if req != nil {
			req.Header.Set("Authorization", "Bearer "+e.Key)
		}
	case EngineTavily:
		body := map[string]any{"query": query, "max_results": n, "search_depth": "basic", "include_answer": false}
		req, err = jsonRequest(ctx, e.base("https://api.tavily.com")+"/search", body)
		if req != nil {
			req.Header.Set("Authorization", "Bearer "+e.Key)
		}
	case EngineBrave:
		q := url.Values{"q": {query}, "count": {strconv.Itoa(n)}, "text_decorations": {"false"}, "extra_snippets": {"true"}}
		if lang != "" {
			q.Set("search_lang", lang)
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, e.base("https://api.search.brave.com")+"/res/v1/web/search?"+q.Encode(), nil)
		if req != nil {
			req.Header.Set("X-Subscription-Token", e.Key)
		}
	default:
		return nil, fmt.Errorf("Неизвестная поисковая система: %s.", e.Name)
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "OmniBox-AI")
	client := e.HTTP
	if client == nil {
		client = engineHTTP
	}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s недоступен: проверьте соединение с интернетом.", EngineName(e.Name))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, e.statusError(resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, fmt.Errorf("Ответ %s не дочитан.", EngineName(e.Name))
	}
	var raw []Result
	switch e.Name {
	case EnginePerplexity:
		var v struct {
			Results []struct {
				Title, URL, Snippet, Date string
			} `json:"results"`
		}
		err = json.Unmarshal(data, &v)
		for _, r := range v.Results {
			raw = append(raw, Result{Title: r.Title, URL: r.URL, Snippet: dated(r.Snippet, r.Date)})
		}
	case EngineTavily:
		var v struct {
			Results []struct {
				Title     string `json:"title"`
				URL       string `json:"url"`
				Content   string `json:"content"`
				Published string `json:"published_date"`
			} `json:"results"`
		}
		err = json.Unmarshal(data, &v)
		for _, r := range v.Results {
			raw = append(raw, Result{Title: r.Title, URL: r.URL, Snippet: dated(r.Content, r.Published)})
		}
	case EngineBrave:
		var v struct {
			Web struct {
				Results []struct {
					Title       string   `json:"title"`
					URL         string   `json:"url"`
					Description string   `json:"description"`
					Extra       []string `json:"extra_snippets"`
					Age         string   `json:"page_age"`
				} `json:"results"`
			} `json:"web"`
		}
		err = json.Unmarshal(data, &v)
		for _, r := range v.Web.Results {
			raw = append(raw, Result{Title: r.Title, URL: r.URL, Snippet: dated(strings.Join(append([]string{r.Description}, r.Extra...), " … "), r.Age)})
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%s вернул непонятный ответ.", EngineName(e.Name))
	}
	out := []Result{}
	seen := map[string]bool{}
	for _, r := range raw {
		u, err := url.Parse(r.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || seen[r.URL] {
			continue
		}
		seen[r.URL] = true
		out = append(out, Result{Title: clip(r.Title, 200), URL: r.URL, Snippet: clip(r.Snippet, snippetLimit)})
		if len(out) == n {
			break
		}
	}
	return out, nil
}

// dated puts the page's date before its text: the model can tell fresh news from old.
func dated(text, date string) string {
	if date = strings.TrimSpace(date); date != "" {
		if len(date) > 10 && date[4] == '-' {
			date = date[:10]
		}
		return "(" + date + ") " + text
	}
	return text
}

func jsonRequest(ctx context.Context, address string, body any) (*http.Request, error) {
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// statusError explains an engine's refusal; its body is not quoted (it could echo the key).
func (e Engine) statusError(status int) error {
	name := EngineName(e.Name)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden || (e.Name == EngineBrave && status == http.StatusUnprocessableEntity):
		return fmt.Errorf("%s не принял ключ: проверьте его в «Настройки → Веб-поиск».", name)
	case status == http.StatusPaymentRequired || status == 432 || status == 433:
		return fmt.Errorf("У ключа %s закончились запросы или деньги на счёте.", name)
	case status == http.StatusTooManyRequests:
		return fmt.Errorf("%s ограничил частоту запросов или исчерпан лимит ключа. Повторите позже.", name)
	}
	return fmt.Errorf("%s ответил HTTP %d.", name, status)
}
