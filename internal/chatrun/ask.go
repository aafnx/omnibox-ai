package chatrun

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aafnx/omnibox-ai/internal/chats"
	"github.com/aafnx/omnibox-ai/internal/openrouter"
	"github.com/aafnx/omnibox-ai/internal/reasoning"
)

// Ask is one request of an automation to a model, outside any chat: no history, no tools, no
// web search. Private keeps it on the PC: only a local model is allowed.
type Ask struct {
	Model    string
	Private  bool
	Session  string // groups the requests in the activity list, such as "panel-auto-<id>"
	Messages []map[string]any
	Timeout  time.Duration
}

// ErrNotLocal is the refusal of a cloud model for a private automation.
var ErrNotLocal = errors.New("Эта автоматизация конфиденциальная: она работает только с моделью на ПК. Выберите локальную модель.")

// Ask runs the request and returns the model's text and its cost in dollars. The model is never
// started for it: a local model that is not running is refused, as the panel's rule says.
func (r *Runner) Ask(ctx context.Context, q Ask) (string, float64, error) {
	if q.Model == "" {
		return "", 0, errors.New("Выберите модель для автоматизации.")
	}
	model, ctxLen, err := r.Model(ctx, q.Model)
	if err != nil {
		return "", 0, err
	}
	if q.Private && model.Source != "local" {
		return "", 0, ErrNotLocal
	}
	if model.Source == "local" && !model.Running {
		return "", 0, fmt.Errorf("Модель %s не запущена: запустите её в списке моделей над чатом.", model.Name)
	}
	if q.Timeout <= 0 {
		q.Timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, q.Timeout)
	defer cancel()
	t := &turn{chat: &chats.Chat{ID: q.Session}, model: model, context: ctxLen, private: true}
	body := r.request(t, q.Messages, false)
	// Filling a form is not a creative task: the same data gives the same values.
	body["temperature"] = 0.1
	a := &answer{}
	_, err = r.generate(ctx, t, body, a, &stream{})
	if model.Source == "cloud" && a.cost > 0 && r.Spend != nil {
		if err := r.Spend.Add(model.ID, a.cost, openrouter.SourceChat); err != nil {
			r.logf("automation: spend ledger: %v", err)
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		err = errors.New("Модель не ответила вовремя. Попробуйте ещё раз или выберите модель быстрее.")
	}
	return a.text, a.cost, err
}

// automationRun marks the context of an automation's task: its side requests (the search
// planner) take the automation's entrance too, so no text of it reaches the content log.
type automationRun struct{}

// Task is an automation's task run like a chat answer — with web search and page reading when
// allowed — but stored nowhere: the answer is returned to the caller.
type Task struct {
	Model   string
	Private bool
	Session string
	// Prompt is the task in the owner's words; Text is the data given with this run.
	Prompt    string
	Text      string
	Files     []chats.Attachment // documents with their text
	Media     []map[string]any   // images for a model that sees
	Web, Read bool
	// Sites keep the search on these sites and let the task read their pages and follow the
	// links between them; the owner listed them in the automation.
	Sites   []string
	Timeout time.Duration
}

// taskNote tells the model nobody waits to answer its questions: a scheduled run is read later.
const taskNote = "Это задача автоматизации: выполни её полностью и сразу дай итоговый результат, без уточняющих вопросов и предложений продолжить — ответ прочитают позже. Если данных не хватает, сделай что можно и прямо скажи, чего не хватило."

// sitesNote tells a task that walks sites where it may go: a model told only "addresses of the
// results" does not open the links it is shown.
func sitesNote(sites []string) string {
	if len(sites) == 0 {
		return ""
	}
	return " Поиск идёт только по сайтам " + strings.Join(sites, ", ") + ". Инструментом read_page можно открыть любую страницу этих сайтов: из результатов поиска, из задачи и из списков ссылок под прочитанными страницами. " +
		"Если нужного на странице нет, открой подходящие ссылки — списки, поиск, карточки документов — и только потом отвечай. Для каждого факта давай ссылку на страницу сайта."
}

// Task runs the task and returns the answer: its text, sources, searches and cost in Meta, or
// its error in Error. A private task never searches, whatever it asks for.
func (r *Runner) Task(ctx context.Context, q Task) (chats.Message, error) {
	if q.Model == "" {
		return chats.Message{}, errors.New("Выберите модель для автоматизации.")
	}
	model, ctxLen, err := r.Model(ctx, q.Model)
	if err != nil {
		return chats.Message{}, err
	}
	if q.Private && model.Source != "local" {
		return chats.Message{}, ErrNotLocal
	}
	if model.Source == "local" && !model.Running {
		return chats.Message{}, fmt.Errorf("Модель %s не запущена: запустите её в списке моделей над чатом.", model.Name)
	}
	if q.Private {
		q.Web, q.Read = false, false
	}
	if q.Timeout <= 0 {
		q.Timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.WithValue(ctx, automationRun{}, true), q.Timeout)
	defer cancel()
	effort := ""
	if steps := reasoning.Steps(model.Reasoning); len(steps) > 0 && model.Reasoning.Default != "" {
		effort = model.Reasoning.Default
	}
	var result chats.Message
	// The task is the user's message, so the search is planned from it; the data follows it.
	text := strings.TrimSpace(q.Prompt)
	if d := strings.TrimSpace(q.Text); d != "" {
		text += "\n\nДанные:\n" + d
	}
	t := &turn{chat: &chats.Chat{ID: q.Session, Model: q.Model}, model: model, context: ctxLen, private: true, effort: effort,
		assistant: &chats.Assistant{Instructions: taskNote + sitesNote(q.Sites)}, user: chats.Message{Role: "user", Text: text, Attachments: q.Files},
		media: q.Media, web: q.Web && r.Search != nil, result: &result}
	t.read = t.web && q.Read && r.Read != nil
	if t.web {
		t.sites = q.Sites
	}
	// OpenAI's own search cannot be kept on the sites: OmniBox searches and reads them itself.
	t.native = t.web && len(t.sites) == 0 && model.Source == "chatgpt" && r.ChatGPTSearch != nil && r.ChatGPTSearch()
	r.run(ctx, t, &stream{})
	if errors.Is(ctx.Err(), context.DeadlineExceeded) && result.Text == "" {
		result.Error = "Модель не ответила вовремя. Попробуйте ещё раз или выберите модель быстрее."
	}
	return result, nil
}
