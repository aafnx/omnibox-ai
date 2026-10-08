package chatrun

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aafnx/omnibox-ai/internal/chats"
	"github.com/aafnx/omnibox-ai/internal/openrouter"
	"github.com/aafnx/omnibox-ai/internal/reasoning"
	"github.com/aafnx/omnibox-ai/internal/websearch"
)

// searchTool is the one tool the chat gives a model that calls functions.
var searchTool = map[string]any{"type": "function", "function": map[string]any{
	"name":        "web_search",
	"description": "Search the web for current or specific information. Use for facts you are not sure about, news, prices, versions, documentation.",
	"parameters": map[string]any{"type": "object", "required": []string{"query"},
		"properties": map[string]any{"query": map[string]any{"type": "string", "description": "A short search query."}}},
}}

// answer is what the model produced over all its rounds.
type answer struct {
	text, thinking string
	thinkStart     time.Time
	thinkSeconds   float64
	input, output  int
	cost           float64
	first, last    time.Time
	sources        []chats.Source
	searches       []string
	err            string
	llamaSpeed     float64
}

func (r *Runner) run(ctx context.Context, t *turn, out *stream) {
	start := r.now()
	out.send("user", t.user)
	a := &answer{}
	messages := r.conversation(t)
	tools := t.web && t.model.Caps.Tools && r.Search != nil
	if t.web && !tools && r.Search != nil {
		// Without function calls the question itself is searched first.
		if q := searchQuery(t.user.Text); q != "" {
			messages = r.searchInto(ctx, messages, a, q, out)
		}
	}
	for round := 0; round < maxRounds && ctx.Err() == nil; round++ {
		body := r.request(t, messages, tools && round < maxRounds-1)
		calls, err := r.generate(ctx, t, body, a, out)
		if err != nil {
			a.err = err.Error()
			break
		}
		if len(calls) == 0 {
			break
		}
		// The model asked to search: run its queries and give it the results.
		assistant := map[string]any{"role": "assistant", "content": nil, "tool_calls": calls}
		messages = append(messages, assistant)
		for _, c := range calls {
			id, _ := c["id"].(string)
			fn, _ := c["function"].(map[string]any)
			var args struct {
				Query string `json:"query"`
			}
			raw, _ := fn["arguments"].(string)
			_ = json.Unmarshal([]byte(raw), &args)
			content := "Поиск не выполнен: пустой запрос."
			if q := strings.TrimSpace(args.Query); q != "" && len(a.searches) < maxSearches {
				content = r.search(ctx, a, q, out)
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": id, "content": content})
		}
	}
	if ctx.Err() != nil && a.err == "" {
		a.err = "Ответ остановлен."
	}
	r.finish(t, a, start, out)
}

// conversation is the system prompt, the history that fits the context and this message.
func (r *Runner) conversation(t *turn) []map[string]any {
	system := []string{"Сегодня " + r.now().Format("2006-01-02") + "."}
	if t.assistant != nil && strings.TrimSpace(t.assistant.Instructions) != "" {
		system = append(system, t.assistant.Instructions)
	}
	if t.web && r.Search != nil {
		system = append(system, "Если используешь результаты веб-поиска, ссылайся на источники их номерами в квадратных скобках, например [1].")
	}
	head := []map[string]any{{"role": "system", "content": strings.Join(system, "\n\n")}}
	budget := t.context - answerReserve
	if budget < 2048 {
		budget = 2048
	}
	used := estimateTokens(head[0]["content"].(string))
	current := map[string]any{"role": "user", "content": userContent(t.user, t.media)}
	used += estimateTokens(withFiles(t.user)) + 1500*len(t.media)
	var past []map[string]any
	for i := len(t.history) - 1; i >= 0; i-- {
		m := t.history[i]
		text := m.Text
		if m.Role == "user" {
			text = withFiles(m)
		}
		if strings.TrimSpace(text) == "" || (m.Role != "user" && m.Role != "assistant") {
			continue
		}
		n := estimateTokens(text)
		if used+n > budget {
			break
		}
		used += n
		past = append([]map[string]any{{"role": m.Role, "content": text}}, past...)
	}
	return append(append(head, past...), current)
}

// withFiles is a user message with the text of its files.
func withFiles(m chats.Message) string {
	var b strings.Builder
	b.WriteString(m.Text)
	for _, a := range m.Attachments {
		if a.Text != "" {
			fmt.Fprintf(&b, "\n\n--- Файл «%s» ---\n%s\n--- конец файла ---", a.Name, a.Text)
		}
	}
	return b.String()
}

func userContent(m chats.Message, media []map[string]any) any {
	text := withFiles(m)
	if len(media) == 0 {
		return text
	}
	parts := []map[string]any{{"type": "text", "text": text}}
	return append(parts, media...)
}

// request is the body for the model with the reasoning depth and the assistant's style.
func (r *Runner) request(t *turn, messages []map[string]any, tools bool) map[string]any {
	body := map[string]any{"model": t.model.Alias, "messages": messages, "stream": true}
	if t.model.Source == "cloud" {
		body["model"] = t.model.ID
		body["usage"] = map[string]any{"include": true}
	}
	if tools {
		body["tools"] = []any{searchTool}
		body["tool_choice"] = "auto"
	}
	if t.assistant != nil {
		switch t.assistant.Style {
		case "precise":
			body["temperature"] = 0.2
		case "balanced":
			body["temperature"] = 0.7
		case "creative":
			body["temperature"] = 1.0
		}
	}
	p := t.model.Reasoning
	switch t.model.Engine {
	case "openrouter":
		reasoning.ApplyOpenRouter(p, t.effort, body)
		if v, ok := r.learned.Load(t.model.ID); ok {
			for _, k := range v.([]string) {
				delete(body, k)
			}
		}
	case "prismml":
		reasoning.ApplyLlama(p, t.effort, body)
	case "ollama":
		// Ollama's OpenAI API takes reasoning_effort; "none" switches thinking off.
		switch {
		case t.effort == reasoning.StepOff && p.Mode == reasoning.ModeToggle:
			body["reasoning_effort"] = "none"
		case len(p.Efforts) > 0 && t.effort != "":
			body["reasoning_effort"] = t.effort
		}
	}
	return body
}

// generate runs one model turn and streams it; it returns the tool calls the model made.
func (r *Runner) generate(ctx context.Context, t *turn, body map[string]any, a *answer, out *stream) ([]map[string]any, error) {
	resp, err := r.send(ctx, t, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	type call struct {
		ID   string
		Name string
		Args strings.Builder
	}
	var calls []*call
	think := &thinkSplitter{}
	for {
		line, err := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			data = strings.TrimSpace(data)
			if data == "[DONE]" {
				// The rest of the stream is read, so the request ends as completed, not cut.
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
				break
			}
			var c chunk
			if json.Unmarshal([]byte(data), &c) != nil {
				continue
			}
			if c.Error != nil {
				return nil, fmt.Errorf("Модель вернула ошибку: %s", clip(c.Error.Message, 300))
			}
			if c.Usage != nil {
				a.input += c.Usage.Prompt
				a.output += c.Usage.Completion
				a.cost += c.Usage.Cost
			}
			if c.Timings != nil && c.Timings.Predicted > 0 {
				a.llamaSpeed = c.Timings.Predicted
			}
			for _, ch := range c.Choices {
				d := ch.Delta
				if th := d.Reasoning + d.ReasoningContent; th != "" {
					r.thought(a, th, out)
				}
				if d.Content != "" {
					text, th := think.split(d.Content)
					if th != "" {
						r.thought(a, th, out)
					}
					if text != "" {
						r.wrote(a, text, out)
					}
				}
				for _, tc := range d.ToolCalls {
					for len(calls) <= tc.Index {
						calls = append(calls, &call{})
					}
					x := calls[tc.Index]
					if tc.ID != "" {
						x.ID = tc.ID
					}
					if tc.Function.Name != "" {
						x.Name = tc.Function.Name
					}
					x.Args.WriteString(tc.Function.Arguments)
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				break
			}
			return nil, errors.New("Соединение с моделью прервалось.")
		}
	}
	var out2 []map[string]any
	for i, c := range calls {
		if c.Name != "web_search" {
			continue
		}
		if c.ID == "" {
			c.ID = fmt.Sprintf("call_%d", i)
		}
		out2 = append(out2, map[string]any{"id": c.ID, "type": "function", "function": map[string]any{"name": c.Name, "arguments": c.Args.String()}})
	}
	return out2, nil
}

type chunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
	Usage *struct {
		Prompt     int     `json:"prompt_tokens"`
		Completion int     `json:"completion_tokens"`
		Cost       float64 `json:"cost"`
	} `json:"usage"`
	Timings *struct {
		Predicted float64 `json:"predicted_per_second"`
	} `json:"timings"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (r *Runner) thought(a *answer, text string, out *stream) {
	if a.thinkStart.IsZero() {
		a.thinkStart = r.now()
	}
	if a.first.IsZero() {
		a.first = r.now()
	}
	a.thinking += text
	a.last = r.now()
	out.send("reasoning", map[string]string{"t": text})
}

func (r *Runner) wrote(a *answer, text string, out *stream) {
	if !a.thinkStart.IsZero() && a.thinkSeconds == 0 {
		a.thinkSeconds = r.now().Sub(a.thinkStart).Seconds()
	}
	if a.first.IsZero() {
		a.first = r.now()
	}
	a.text += text
	a.last = r.now()
	out.send("delta", map[string]string{"t": text})
}

// send reaches the model: the local one through /v1, a cloud one through OpenRouter. A
// parameter OpenRouter refuses for this model is dropped and the request sent once more.
func (r *Runner) send(ctx context.Context, t *turn, body map[string]any) (*http.Response, error) {
	if t.model.Source == "local" {
		data, _ := json.Marshal(body)
		resp := r.Local(ctx, "panel-chat-"+t.chat.ID, data)
		if resp.StatusCode != http.StatusOK {
			defer resp.Body.Close()
			var e struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
			return nil, localError(resp.StatusCode, e.Error.Message)
		}
		return resp, nil
	}
	if r.Cloud == nil {
		return nil, errors.New("OpenRouter не подключён: добавьте ключ в «Настройки → OpenRouter».")
	}
	client, err := r.Cloud()
	if err != nil {
		return nil, err
	}
	for attempt := 0; ; attempt++ {
		data, _ := json.Marshal(body)
		resp, err := client.Chat(ctx, data)
		var oe *openrouter.Error
		if err == nil || attempt > 0 || !errors.As(err, &oe) || oe.Status != http.StatusBadRequest {
			return resp, err
		}
		refused := refusedParams(oe.Detail, body)
		if len(refused) == 0 {
			return nil, err
		}
		prev, _ := r.learned.Load(t.model.ID)
		list, _ := prev.([]string)
		for _, k := range refused {
			delete(body, k)
			list = append(list, k)
		}
		r.learned.Store(t.model.ID, list)
		r.logf("chat: OpenRouter refused %v for %s; sent again without them", refused, t.model.ID)
	}
}

// refusedParams are the request's optional parameters an error message names.
func refusedParams(detail string, body map[string]any) []string {
	detail = strings.ToLower(detail)
	var out []string
	for _, k := range []string{"temperature", "top_p", "top_k", "min_p", "seed", "presence_penalty", "frequency_penalty", "repetition_penalty", "reasoning", "tool_choice"} {
		if _, ok := body[k]; ok && strings.Contains(detail, k) {
			out = append(out, k)
		}
	}
	return out
}

func localError(status int, message string) error {
	switch status {
	case http.StatusServiceUnavailable:
		return errors.New("Локальная модель не запущена или ещё загружается.")
	case http.StatusConflict:
		return errors.New("Работающая модель сменилась. Выберите модель в списке ещё раз.")
	case http.StatusTooManyRequests:
		return errors.New("Очередь занята другими запросами. Повторите через минуту.")
	case http.StatusGatewayTimeout:
		return errors.New("Ответ прерван: модель остановлена или ответ занял слишком долго.")
	}
	if message != "" {
		return fmt.Errorf("Модель ответила ошибкой %d: %s", status, clip(message, 300))
	}
	return fmt.Errorf("Модель ответила ошибкой %d.", status)
}

// searchQuery is the question itself, cut to a query's length.
func searchQuery(text string) string {
	return clip(strings.Join(strings.Fields(text), " "), 200)
}

// searchInto runs one search before the answer and puts its results into the system prompt.
func (r *Runner) searchInto(ctx context.Context, messages []map[string]any, a *answer, q string, out *stream) []map[string]any {
	results := r.search(ctx, a, q, out)
	sys := messages[0]["content"].(string)
	messages[0]["content"] = sys + "\n\nРезультаты веб-поиска по вопросу пользователя:\n" + results
	return messages
}

// search asks SearXNG and returns the results numbered after the earlier ones.
func (r *Runner) search(ctx context.Context, a *answer, q string, out *stream) string {
	a.searches = append(a.searches, q)
	out.send("search", map[string]string{"query": q})
	results, err := r.Search(ctx, q)
	if err != nil {
		out.send("status", map[string]string{"text": err.Error()})
		return "Поиск не удался: " + err.Error()
	}
	if len(results) == 0 {
		return "Ничего не найдено."
	}
	var b strings.Builder
	for _, res := range results {
		n := len(a.sources) + 1
		for i, s := range a.sources {
			if s.URL == res.URL {
				n = i + 1
			}
		}
		if n == len(a.sources)+1 {
			a.sources = append(a.sources, chats.Source{Title: res.Title, URL: res.URL})
		}
		fmt.Fprintf(&b, "[%d] %s — %s\n%s\n\n", n, res.Title, websearch.Host(res.URL), res.Snippet)
	}
	out.send("sources", a.sources)
	return b.String()
}

// finish stores the answer and tells the browser what it cost and how fast it was.
func (r *Runner) finish(t *turn, a *answer, start time.Time, out *stream) {
	if a.thinkSeconds == 0 && !a.thinkStart.IsZero() {
		a.thinkSeconds = a.last.Sub(a.thinkStart).Seconds()
	}
	msg := chats.Message{ID: chats.NewMessageID(), Role: "assistant", Text: strings.TrimSpace(a.text), Reasoning: strings.TrimSpace(a.thinking),
		ReasoningSeconds: round(a.thinkSeconds), Model: t.chat.Model, Created: r.now().UTC(), Error: a.err}
	meta := &chats.Meta{Seconds: round(r.now().Sub(start).Seconds()), InputTokens: a.input, OutputTokens: a.output, CostUSD: a.cost,
		Sources: a.sources, Searches: a.searches}
	if a.llamaSpeed > 0 {
		meta.TokensPerSecond = round(a.llamaSpeed)
	} else if gen := a.last.Sub(a.first).Seconds(); gen > 0.2 && a.output > 1 {
		meta.TokensPerSecond = round(float64(a.output-1) / gen)
	}
	msg.Meta = meta
	if t.model.Source == "cloud" && a.cost > 0 && r.Spend != nil {
		if err := r.Spend.Add(t.model.ID, a.cost, openrouter.SourceChat); err != nil {
			r.logf("chat: spend ledger: %v", err)
		}
	}
	if msg.Text == "" && msg.Error == "" {
		msg.Error = "Модель не вернула текст."
	}
	saved, err := r.Chats.Update(t.chat.ID, func(c *chats.Chat) error {
		c.Messages = append(c.Messages, msg)
		return nil
	})
	if err != nil {
		r.logf("chat: answer not stored: %v", err)
		out.send("done", map[string]any{"message": msg})
		return
	}
	out.send("done", map[string]any{"message": saved.Messages[len(saved.Messages)-1], "chat": saved.Summary()})
}

func round(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// thinkSplitter separates <think>…</think> from the text of models whose engine does not.
type thinkSplitter struct {
	inside bool
	seen   bool
	buf    string
}

func (s *thinkSplitter) split(chunk string) (text, think string) {
	s.buf += chunk
	for s.buf != "" {
		if !s.inside {
			if !s.seen {
				trimmed := strings.TrimLeft(s.buf, " \n")
				if strings.HasPrefix(trimmed, "<think>") {
					s.inside, s.seen, s.buf = true, true, strings.TrimPrefix(trimmed, "<think>")
					continue
				}
				if len(trimmed) < len("<think>") && strings.HasPrefix("<think>", trimmed) {
					return text, think // wait for more
				}
				s.seen = true
			}
			text += s.buf
			s.buf = ""
			break
		}
		if i := strings.Index(s.buf, "</think>"); i >= 0 {
			think += s.buf[:i]
			s.buf, s.inside = strings.TrimLeft(s.buf[i+len("</think>"):], "\n"), false
			continue
		}
		// Keep a possible start of </think> for the next chunk.
		keep := 0
		for k := len("</think>") - 1; k > 0; k-- {
			if strings.HasSuffix(s.buf, "</think>"[:k]) {
				keep = k
				break
			}
		}
		think += s.buf[:len(s.buf)-keep]
		s.buf = s.buf[len(s.buf)-keep:]
		break
	}
	return text, think
}
