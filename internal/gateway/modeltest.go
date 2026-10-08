package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// internalRequest marks the panel's own requests to /v1.
type internalRequest struct{}

// testPrompt is fixed, so results after a settings change compare with earlier ones.
const testPrompt = "Перечисли словами числа от одного до тридцати через запятую, без пояснений."

// TestResult is a short measurement of the running model.
type TestResult struct {
	Engine          string  `json:"engine"`
	Model           string  `json:"model"`
	FirstToken      float64 `json:"first_token_seconds"`
	PromptTokens    int     `json:"prompt_tokens"`
	PromptPerSecond float64 `json:"prompt_tokens_per_second"`
	OutputTokens    int     `json:"output_tokens"`
	OutputPerSecond float64 `json:"output_tokens_per_second"`
	Total           float64 `json:"total_seconds"`
}

// String is the line the panel shows.
func (r TestResult) String() string {
	return fmt.Sprintf("%s: первый токен через %.2f с · чтение запроса %.0f ток/с (%d) · генерация %.1f ток/с (%d) · всего %.1f с",
		r.Model, r.FirstToken, r.PromptPerSecond, r.PromptTokens, r.OutputPerSecond, r.OutputTokens, r.Total)
}

// timedWriter keeps a streamed answer with the moment its first text arrived.
type timedWriter struct {
	mu     sync.Mutex
	header http.Header
	status int
	body   bytes.Buffer
	first  time.Time
	now    func() time.Time
}

func (w *timedWriter) Header() http.Header { return w.header }
func (w *timedWriter) WriteHeader(s int)   { w.status = s }
func (w *timedWriter) Flush()              {}
func (w *timedWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.first.IsZero() && bytes.Contains(b, []byte(`"content":"`)) && !bytes.Contains(b, []byte(`"content":""`)) {
		w.first = w.now()
	}
	return w.body.Write(b)
}

// Test sends a short fixed request to the running model through the queue, like any client, and
// measures the time to the first token and the reading and generation speeds.
func (g *Gateway) Test(ctx context.Context) (TestResult, error) {
	t, _, err := g.resolve()
	if err != nil {
		return TestResult{}, errors.New("Нет запущенной модели: запустите её в панели.")
	}
	body, _ := json.Marshal(map[string]any{"model": t.Model, "stream": true, "max_tokens": 160, "temperature": 0,
		"stream_options": map[string]any{"include_usage": true},
		"messages":       []map[string]string{{"role": "user", "content": testPrompt}}})
	req, err := http.NewRequestWithContext(context.WithValue(ctx, internalRequest{}, "test"), http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return TestResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Omnibox-Session", "model-test")
	w := &timedWriter{header: http.Header{}, now: g.now}
	start := g.now()
	g.ServeHTTP(w, req)
	end := g.now()
	if w.status != http.StatusOK {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		_ = json.Unmarshal(w.body.Bytes(), &e)
		return TestResult{}, fmt.Errorf("Тест не выполнен (%d): %s", w.status, e.Error.Message)
	}
	r := TestResult{Engine: t.Engine, Model: t.Model, Total: round(end.Sub(start).Seconds(), 2)}
	var prompt, output float64
	scanner := bufio.NewScanner(bytes.NewReader(w.body.Bytes()))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line, ok := strings.CutPrefix(scanner.Text(), "data:")
		if !ok {
			continue
		}
		var chunk struct {
			Usage *struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
			} `json:"usage"`
			// llama-server reports its own speeds.
			Timings *struct {
				Prompt    float64 `json:"prompt_per_second"`
				Predicted float64 `json:"predicted_per_second"`
			} `json:"timings"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(line)), &chunk) != nil {
			continue
		}
		if chunk.Usage != nil {
			r.PromptTokens, r.OutputTokens = chunk.Usage.Prompt, chunk.Usage.Completion
		}
		if chunk.Timings != nil {
			prompt, output = chunk.Timings.Prompt, chunk.Timings.Predicted
		}
	}
	if w.first.IsZero() {
		return TestResult{}, errors.New("Модель не вернула текст: проверьте журнал движка.")
	}
	first := w.first.Sub(start).Seconds()
	r.FirstToken = round(first, 2)
	if prompt == 0 && first > 0 {
		prompt = float64(r.PromptTokens) / first
	}
	if gen := end.Sub(w.first).Seconds(); output == 0 && gen > 0 && r.OutputTokens > 1 {
		output = float64(r.OutputTokens-1) / gen
	}
	r.PromptPerSecond, r.OutputPerSecond = round(prompt, 1), round(output, 1)
	return r, nil
}

func round(v float64, digits int) float64 {
	p := 1.0
	for range digits {
		p *= 10
	}
	return float64(int64(v*p+0.5)) / p
}
