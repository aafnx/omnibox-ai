package openrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Sonar's answer is the summary, its citations the results, its cost comes back.
func TestSonar(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Курс евро 95,1 ₽ [1].","annotations":[{"type":"url_citation","url_citation":{"url":"https://cbr.example/","title":"ЦБ","content":"95,1"}}]}}],
			"citations":["https://cbr.example/","https://news.example/euro"],"usage":{"cost":0.0071}}`))
	}))
	defer srv.Close()
	c := &Client{Key: "sk-or-v1-0123456789abcdef0123", BaseURL: srv.URL}
	found, err := c.Sonar(context.Background(), "perplexity/sonar", "Какой курс евро?", "Сейчас понедельник, 12 октября 2026 г.")
	if err != nil {
		t.Fatal(err)
	}
	if found.Summary != "Курс евро 95,1 ₽ [1]." || found.CostUSD != 0.0071 || len(found.Results) != 2 ||
		found.Results[0].Title != "ЦБ" || found.Results[1].Title != "news.example" {
		t.Fatalf("%+v", found)
	}
	if body["model"] != "perplexity/sonar" || !strings.Contains(mustJSON(body["messages"]), "12 октября 2026") {
		t.Fatalf("%v", body)
	}
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
