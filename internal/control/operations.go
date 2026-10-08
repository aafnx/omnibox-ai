package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sort"
	"sync"
	"time"
)

// Gate keeps engine control and generations apart; the /v1 gateway implements it.
type Gate interface {
	// BeginControl reserves the engines for a control operation; cancel aborts running generations.
	BeginControl(cancel bool) bool
	EndControl()
}

// Operation is one panel operation as the panel shows it.
type Operation struct {
	ID      string    `json:"id"`
	Action  string    `json:"action"`
	Engine  string    `json:"backend"`
	State   string    `json:"state"` // running, done or failed
	Message string    `json:"message"`
	Started time.Time `json:"started"`
	Elapsed float64   `json:"elapsed_seconds"`
	// Progress of a download: bytes done, total (0 if unknown) and the current speed.
	Done     int64   `json:"bytes_done,omitempty"`
	Total    int64   `json:"bytes_total,omitempty"`
	Speed    float64 `json:"bytes_per_second,omitempty"`
	category string
	cancel   context.CancelFunc
	mark     time.Time
	markDone int64
}

type progressKey struct{}

// Progress reports download progress for the operation running in ctx.
func Progress(ctx context.Context, done, total int64) {
	if f, ok := ctx.Value(progressKey{}).(func(int64, int64)); ok {
		f(done, total)
	}
}

// Operations runs panel operations in the background, one per category at a time.
type Operations struct {
	Run  func(ctx context.Context, r Request) (string, error)
	Gate Gate
	Now  func() time.Time

	mu   sync.Mutex
	list map[string]*Operation
}

// keepFinished is how many finished operations the panel still lists.
const keepFinished = 20

// ErrBusy means an operation of the same kind still runs.
var ErrBusy = errors.New("Операция этого типа ещё выполняется. Дождитесь результата.")

// ErrGenerating means the engines are busy with a generation.
var ErrGenerating = errors.New("Модель занята генерацией или другой операцией. Дождитесь завершения либо остановите сервер.")

func category(action string) string {
	switch {
	case action == "pull" || action == "download":
		return "download"
	case len(action) > 7 && action[:7] == "engine-":
		return "engine"
	case action == "self-update":
		return "update"
	case action == "model-test":
		// It waits in the /v1 queue, so it must not hold the engines like a control operation.
		return "test"
	}
	return "control"
}

func (o *Operations) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Start begins an operation and returns its id at once.
func (o *Operations) Start(r Request) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.list == nil {
		o.list = map[string]*Operation{}
	}
	cat := category(r.Action)
	for _, op := range o.list {
		if op.category == cat && op.State == "running" {
			return "", ErrBusy
		}
	}
	// engine-update is not guarded: its download may take minutes, and it takes the gate itself
	// right before a running engine restarts.
	guarded := o.Gate != nil && slices.Contains([]string{"start", "stop", "restart", "stopall", "apply", "restore", "load", "unload", "engine-remove", "preset-apply"}, r.Action)
	if guarded && !o.Gate.BeginControl(slices.Contains([]string{"stop", "stopall", "unload", "engine-remove"}, r.Action)) {
		return "", ErrGenerating
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	op := &Operation{ID: hex.EncodeToString(b[:]), Action: r.Action, Engine: r.Engine, State: "running", Message: "Выполняется…", Started: o.now().UTC(), category: cat}
	ctx, cancel := context.WithCancel(context.Background())
	op.cancel = cancel
	ctx = context.WithValue(ctx, progressKey{}, func(done, total int64) { o.progress(op, done, total) })
	o.list[op.ID] = op
	go func() {
		defer cancel()
		var message string
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = errors.New("Операция прервана внутренней ошибкой; проверьте состояние и журнал.")
				}
			}()
			if guarded {
				defer o.Gate.EndControl()
			}
			message, err = o.Run(ctx, r)
		}()
		o.mu.Lock()
		defer o.mu.Unlock()
		op.State, op.Message = "done", message
		if err != nil {
			op.State, op.Message = "failed", err.Error()
		}
		if len(op.Message) > 12000 {
			op.Message = op.Message[:12000]
		}
		o.prune()
	}()
	return op.ID, nil
}

func (o *Operations) progress(op *Operation, done, total int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	if op.mark.IsZero() {
		op.mark, op.markDone = now, done
	} else if dt := now.Sub(op.mark).Seconds(); dt >= 2 {
		// Speed over the last couple of seconds, not since the start, so a resumed download reads right.
		op.Speed = float64(done-op.markDone) / dt
		op.mark, op.markDone = now, done
	}
	op.Done, op.Total = done, total
}

// ErrNotCancelable means the operation cannot be canceled safely.
var ErrNotCancelable = errors.New("Отменить можно только скачивание.")

// Cancel stops a running download; engine and settings operations run to the end.
func (o *Operations) Cancel(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	op, ok := o.list[id]
	if !ok || op.State != "running" {
		return errors.New("Операция не выполняется.")
	}
	if op.category != "download" {
		return ErrNotCancelable
	}
	op.cancel()
	return nil
}

func (o *Operations) prune() {
	var finished []*Operation
	for _, op := range o.list {
		if op.State != "running" {
			finished = append(finished, op)
		}
	}
	sort.Slice(finished, func(i, j int) bool { return finished[i].Started.After(finished[j].Started) })
	for _, op := range finished[min(len(finished), keepFinished):] {
		delete(o.list, op.ID)
	}
}

// List returns the operations, newest first.
func (o *Operations) List() []Operation {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	out := make([]Operation, 0, len(o.list))
	for _, op := range o.list {
		v := *op
		v.Elapsed = float64(now.Sub(op.Started).Milliseconds()/100) / 10
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	return out
}

// Running tells whether any operation still runs.
func (o *Operations) Running() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, op := range o.list {
		if op.State == "running" {
			return true
		}
	}
	return false
}

// Get returns one operation by id.
func (o *Operations) Get(id string) (Operation, bool) {
	for _, op := range o.List() {
		if op.ID == id {
			return op, true
		}
	}
	return Operation{}, false
}
