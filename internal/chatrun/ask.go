package chatrun

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aafnx/omnibox-ai/internal/chats"
	"github.com/aafnx/omnibox-ai/internal/openrouter"
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
