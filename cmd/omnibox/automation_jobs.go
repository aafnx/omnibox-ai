package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// A run started from the panel goes on in the service: a step that reads a site for minutes
// would outlast the browser's request and the NAS proxy in front of it. The panel follows the
// run by asking for it while its window is open; closing the window does not stop it, and its
// result goes to the history when the automation keeps one. Nothing of a job is written to disk.

// jobKeep is how long a finished job waits to be read; maxJobs bounds those kept at once.
const (
	jobKeep = time.Hour
	maxJobs = 20
)

type autoJob struct {
	mu       sync.Mutex
	trace    []string
	done     bool
	out      any
	err      string
	cancel   context.CancelFunc
	finished time.Time
}

type autoJobs struct {
	mu   sync.Mutex
	jobs map[string]*autoJob
	now  func() time.Time
}

func (j *autoJobs) clock() time.Time {
	if j.now != nil {
		return j.now()
	}
	return time.Now()
}

// purge drops jobs read long enough ago; it runs when one is added or asked for, not on a timer.
func (j *autoJobs) purge() {
	now := j.clock()
	for id, x := range j.jobs {
		x.mu.Lock()
		old := x.done && now.Sub(x.finished) > jobKeep
		x.mu.Unlock()
		if old {
			delete(j.jobs, id)
		}
	}
}

// start runs work in the background and returns the job's id.
func (j *autoJobs) start(work func(ctx context.Context, trace func(string)) (any, error)) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.jobs == nil {
		j.jobs = map[string]*autoJob{}
	}
	j.purge()
	running := 0
	for _, x := range j.jobs {
		x.mu.Lock()
		if !x.done {
			running++
		}
		x.mu.Unlock()
	}
	if running >= maxJobs/2 {
		return "", errors.New("Слишком много запусков сразу: дождитесь, пока закончатся начатые.")
	}
	// Full: the finished job read longest ago makes room.
	for len(j.jobs) >= maxJobs {
		oldest := ""
		var at time.Time
		for id, x := range j.jobs {
			x.mu.Lock()
			if x.done && (oldest == "" || x.finished.Before(at)) {
				oldest, at = id, x.finished
			}
			x.mu.Unlock()
		}
		delete(j.jobs, oldest)
	}
	ctx, cancel := context.WithCancel(context.Background())
	x := &autoJob{cancel: cancel}
	id := jobID()
	j.jobs[id] = x
	go func() {
		defer cancel()
		out, err := work(ctx, func(s string) {
			x.mu.Lock()
			x.trace = append(x.trace, s)
			x.mu.Unlock()
		})
		x.mu.Lock()
		x.done, x.out, x.finished = true, out, j.clock()
		if err != nil {
			x.err = err.Error()
		}
		x.mu.Unlock()
	}()
	return id, nil
}

// state is what the panel sees of a job: the trace so far, then the result or the error.
func (j *autoJobs) state(id string) (map[string]any, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.purge()
	x, ok := j.jobs[id]
	if !ok {
		return nil, errors.New("Запуск не найден: он закончился больше часа назад. Результат — в истории, если она включена.")
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	out := map[string]any{"done": x.done, "trace": append([]string{}, x.trace...)}
	if x.done {
		if x.err != "" {
			out["error"] = x.err
		} else if m, ok := x.out.(map[string]any); ok {
			for k, v := range m {
				out[k] = v
			}
		}
	}
	return out, nil
}

// stop cancels a running job; its steps end with what they did so far.
func (j *autoJobs) stop(id string) error {
	j.mu.Lock()
	x, ok := j.jobs[id]
	j.mu.Unlock()
	if !ok {
		return errors.New("Запуск не найден.")
	}
	x.cancel()
	return nil
}

func jobID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
