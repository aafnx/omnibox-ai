package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"time"
)

// Admission errors carry the HTTP status the client gets.
type admissionError struct {
	status  int
	message string
}

func (e *admissionError) Error() string { return e.message }

var (
	errBusy    = &admissionError{409, "The model is busy or a control operation is running. Retry after completion."}
	errFull    = &admissionError{429, "The request queue is full. Retry later."}
	errChanged = &admissionError{503, "The model was stopped or changed in OmniBox AI. The request was not run."}
	errTimeout = &admissionError{503, "Timed out waiting in the request queue."}
)

// waiter is a request in the queue. It is bound to the generation it was admitted in, so a
// stop, unload or settings change never lets it run on another model.
type waiter struct {
	gen     uint64
	record  *Record
	ready   chan error
	granted bool
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (g *Gateway) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// admit makes the request the one running generation, waiting in the FIFO queue if another
// one runs. cancel aborts the request when a control operation stops the engine.
func (g *Gateway) admit(ctx context.Context, gen uint64, t Target, route, sessionHeader string, cancel context.CancelFunc, depth int, timeout time.Duration) (*Record, error) {
	g.mu.Lock()
	if g.control || gen != g.gen {
		g.mu.Unlock()
		return nil, errChanged
	}
	r := &Record{ID: newID(), SessionID: g.sessionID(t.Engine, t.Model, sessionHeader), Engine: t.Engine, Model: t.Model, Route: route,
		Started: g.now().UTC(), State: "preparing", ContextLimit: t.Context, UsageSource: "unavailable"}
	if g.current == nil && len(g.queue) == 0 {
		g.current, g.cancel = r, cancel
		g.mu.Unlock()
		return r, nil
	}
	if depth == 0 {
		g.mu.Unlock()
		return nil, errBusy
	}
	if len(g.queue) >= depth {
		g.mu.Unlock()
		return nil, errFull
	}
	r.State = "queued"
	w := &waiter{gen: gen, record: r, ready: make(chan error, 1)}
	g.queue = append(g.queue, w)
	g.cancels[r.ID] = cancel
	g.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var err error
	select {
	case err = <-w.ready:
		if err == nil {
			return r, nil
		}
		return nil, err
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = errTimeout
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.cancels, r.ID)
	if w.granted {
		// Granted at the same moment the client left or the wait ran out: pass the turn on.
		g.current, g.cancel = nil, nil
		g.next()
		return nil, err
	}
	g.queue = slices.DeleteFunc(g.queue, func(x *waiter) bool { return x == w })
	return nil, err
}

// next hands the turn to the oldest waiter. The caller holds the lock.
func (g *Gateway) next() {
	for len(g.queue) > 0 && g.current == nil {
		w := g.queue[0]
		g.queue = g.queue[1:]
		if w.gen != g.gen || g.control {
			w.ready <- errChanged
			continue
		}
		w.granted = true
		queued := g.now().UTC().Sub(w.record.Started).Seconds()
		w.record.Started, w.record.State, w.record.QueuedSeconds = g.now().UTC(), "preparing", float64(int64(queued*1000))/1000
		g.current, g.cancel = w.record, g.cancels[w.record.ID]
		delete(g.cancels, w.record.ID)
		w.ready <- nil
	}
}

// bump starts a new generation: every queued request fails at once. The caller holds the lock.
func (g *Gateway) bump() {
	g.gen++
	for _, w := range g.queue {
		delete(g.cancels, w.record.ID)
		w.ready <- errChanged
	}
	g.queue = nil
}

// BeginControl reserves the engines for a panel operation. A running generation blocks it,
// unless cancel is set (stop, unload, remove), which aborts the generation.
func (g *Gateway) BeginControl(cancel bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.control || (g.current != nil && !cancel) {
		return false
	}
	g.control = true
	g.bump()
	clear(g.modelSessions)
	if cancel && g.cancel != nil {
		g.cancel()
	}
	return true
}

// Reserve closes admission for a panel operation that must wait for the running generation
// (a version switch); queued requests fail at once.
func (g *Gateway) Reserve() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.control {
		return false
	}
	g.control = true
	g.bump()
	return true
}

// Generating tells whether a generation still runs.
func (g *Gateway) Generating() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.current != nil
}

// EndControl reopens admission after a panel operation.
func (g *Gateway) EndControl() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.control = false
	g.gen++
}

// Invalidate starts a new generation without blocking admission, e.g. after settings change.
func (g *Gateway) Invalidate() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.bump()
}

func (g *Gateway) generation() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gen
}

// finish records the outcome, writes the journals and passes the turn to the next request.
func (g *Gateway) finish(r *Record, status int, completed bool, c *capture) {
	if r == nil {
		return
	}
	g.mu.Lock()
	ended := g.now().UTC()
	r.Ended, r.Status = &ended, status
	r.State = "failed"
	if completed && status < 400 {
		r.State = "completed"
	}
	r.Duration = float64(ended.Sub(r.Started).Milliseconds()) / 1000
	g.add(r)
	line, _ := jsonLine(r)
	var content []byte
	if c != nil && g.contentEnabled() {
		content, _ = c.line(r)
	}
	if g.current == r {
		g.current, g.cancel = nil, nil
	}
	g.next()
	g.mu.Unlock()

	g.journal.Lock()
	defer g.journal.Unlock()
	var failure string
	if err := appendLine(g.path("requests.jsonl"), line, journalLimit); err != nil {
		failure = "Журнал запросов не записан."
	}
	if content != nil {
		if err := appendLine(g.path("content.jsonl"), content, contentLimit); err != nil {
			failure = "Содержимое запроса не записано."
		}
	}
	if failure != "" {
		g.mu.Lock()
		g.journalErr = failure
		g.mu.Unlock()
	}
}
