package openrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/aafnx/omnibox-ai/internal/websearch"
)

// SonarModels are the Perplexity models the chat can search with through OpenRouter.
var SonarModels = []string{"perplexity/sonar", "perplexity/sonar-pro"}

// Sonar searches with a Perplexity model: it answers the question briefly from the web, and its
// sources become the results. The answer is a summary for the chat's own model, which checks
// it against the sources; its cost comes back with it. dateLine, when given, tells Sonar what day
// it is.
func (c *Client) Sonar(ctx context.Context, model, question, dateLine string) (websearch.Found, error) {
	system := "Найди в интернете актуальные данные по вопросу и кратко изложи факты с датами, числами и названиями. " +
		"Отвечай на языке вопроса. Если точных данных нет, так и скажи."
	if dateLine != "" {
		system = dateLine + " " + system
	}
	body, _ := json.Marshal(map[string]any{
		"model": model, "stream": false, "usage": map[string]any{"include": true},
		"messages": []map[string]any{{"role": "system", "content": system}, {"role": "user", "content": question}},
	})
	resp, err := c.do(ctx, http.MethodPost, "/chat/completions", body)
	if err != nil {
		return websearch.Found{}, err
	}
	defer resp.Body.Close()
	var v struct {
		Choices []struct {
			Message struct {
				Content     string `json:"content"`
				Annotations []struct {
					Type string `json:"type"`
					URL  struct {
						URL     string `json:"url"`
						Title   string `json:"title"`
						Content string `json:"content"`
					} `json:"url_citation"`
				} `json:"annotations"`
			} `json:"message"`
		} `json:"choices"`
		Citations []string `json:"citations"`
		Usage     struct {
			Cost float64 `json:"cost"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, jsonLimit)).Decode(&v); err != nil || len(v.Choices) == 0 {
		return websearch.Found{}, &Error{Status: resp.StatusCode, Message: "Ответ Perplexity Sonar не прочитан."}
	}
	m := v.Choices[0].Message
	found := websearch.Found{Summary: strings.TrimSpace(m.Content), CostUSD: v.Usage.Cost}
	seen := map[string]bool{}
	add := func(u, title, snippet string) {
		if u == "" || seen[u] || !(strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")) {
			return
		}
		seen[u] = true
		if title == "" {
			title = websearch.Host(u)
		}
		found.Results = append(found.Results, websearch.Result{Title: title, URL: u, Snippet: snippet})
	}
	for _, a := range m.Annotations {
		if a.Type == "url_citation" {
			add(a.URL.URL, a.URL.Title, a.URL.Content)
		}
	}
	for _, u := range v.Citations {
		add(u, "", "")
	}
	if found.Summary == "" && len(found.Results) == 0 {
		return found, &Error{Status: resp.StatusCode, Message: "Perplexity Sonar ничего не нашёл."}
	}
	return found, nil
}
