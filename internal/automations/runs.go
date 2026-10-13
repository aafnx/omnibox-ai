package automations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aafnx/omnibox-ai/internal/chats"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Run is one result kept in an automation's history: only for an automation whose history is on,
// never for a private one. The data given to the run is kept as text (without files), so the
// owner sees what was asked.
type Run struct {
	ID         string         `json:"id"`
	Automation string         `json:"automation"`
	Created    time.Time      `json:"created"`
	Trigger    string         `json:"trigger"` // "manual" or "schedule"
	Model      string         `json:"model"`
	Input      string         `json:"input,omitempty"`
	Files      []string       `json:"files,omitempty"`
	Text       string         `json:"text,omitempty"`
	Values     []Value        `json:"values,omitempty"`
	Sources    []chats.Source `json:"sources,omitempty"`
	Error      string         `json:"error,omitempty"`
	CostUSD    float64        `json:"cost_usd,omitempty"`
	// Steps are the results of every step when there are several; Text and Values are the last's.
	Steps []StepResult `json:"steps,omitempty"`
	// Seen is false for a scheduled result the owner has not opened.
	Seen bool `json:"seen"`
}

// StepResult is what one step gave.
type StepResult struct {
	Name   string  `json:"name"`
	Kind   string  `json:"kind"`
	Model  string  `json:"model"`
	Text   string  `json:"text,omitempty"`
	Values []Value `json:"values,omitempty"`
	Error  string  `json:"error,omitempty"`
}

const (
	maxRunsKept  = 200
	maxRunInput  = 20000
	maxRunResult = 200000
)

func (s *Store) runsDir(id string) string { return filepath.Join(s.Dir, "runs", id) }

// SaveRun keeps a run; the oldest go when an automation has too many.
func (s *Store) SaveRun(r Run) (Run, error) {
	if !validID(r.Automation) {
		return Run{}, errors.New("Автоматизация не найдена.")
	}
	if r.ID == "" {
		// Nanoseconds keep two runs of one second in order: the file names sort as the runs do.
		r.ID = r.Created.UTC().Format("20060102T150405.000000000") + "-" + newID()[:6]
	}
	r.Input = clip(r.Input, maxRunInput)
	r.Text = clip(r.Text, maxRunResult)
	for i := range r.Steps {
		r.Steps[i].Text = clip(r.Steps[i].Text, maxRunResult)
	}
	dir := s.runsDir(r.Automation)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Run{}, err
	}
	data, _ := json.Marshal(r)
	if err := platform.WriteAtomic(filepath.Join(dir, r.ID+".json"), data, 0o600); err != nil {
		return Run{}, fmt.Errorf("Результат не сохранён: %v", err)
	}
	if names, _ := runFiles(dir); len(names) > maxRunsKept {
		for _, n := range names[:len(names)-maxRunsKept] {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
	return r, nil
}

// runFiles are a history's files, the oldest first: their names start with the time.
func runFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out, nil
}

// Runs returns an automation's history, the newest first; runs older than keep are deleted
// first, as chats are.
func (s *Store) Runs(id string, keep time.Duration, now time.Time) ([]Run, error) {
	if !validID(id) {
		return nil, errors.New("Автоматизация не найдена.")
	}
	dir := s.runsDir(id)
	names, err := runFiles(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Run{}, nil
	}
	if err != nil {
		return nil, errors.New("История не прочитана.")
	}
	out := []Run{}
	for i := len(names) - 1; i >= 0; i-- {
		data, err := platform.ReadShared(filepath.Join(dir, names[i]))
		var r Run
		if err != nil || json.Unmarshal(data, &r) != nil {
			continue
		}
		if keep > 0 && now.Sub(r.Created) > keep {
			_ = os.Remove(filepath.Join(dir, names[i]))
			continue
		}
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b Run) int { return b.Created.Compare(a.Created) })
	return out, nil
}

// MarkSeen marks an automation's runs as opened.
func (s *Store) MarkSeen(id string) error {
	runs, err := s.Runs(id, 0, time.Now())
	if err != nil {
		return err
	}
	for _, r := range runs {
		if !r.Seen {
			r.Seen = true
			if _, err := s.SaveRun(r); err != nil {
				return err
			}
		}
	}
	return nil
}

// DeleteRuns deletes an automation's history at once, without a copy.
func (s *Store) DeleteRuns(id string) error {
	if !validID(id) {
		return errors.New("Автоматизация не найдена.")
	}
	if err := os.RemoveAll(s.runsDir(id)); err != nil {
		return fmt.Errorf("История не удалена: %v", err)
	}
	return nil
}

// Scheduler runs the scheduled tasks. It keeps one timer, set to the nearest run; it is set again
// when an automation changes and after each run — nothing polls in between.
type Scheduler struct {
	Store *Store
	// Run executes a due automation and keeps its result.
	Run  func(ctx context.Context, a Automation)
	Now  func() time.Time
	Logf func(format string, args ...any)

	mu      sync.Mutex
	ctx     context.Context
	timer   *time.Timer
	running map[string]bool
	next    time.Time
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Start sets the first timer; ctx ends it with the service.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		if s.timer != nil {
			s.timer.Stop()
		}
		s.mu.Unlock()
	}()
	s.Reset()
}

// Next is the nearest scheduled run, zero when none.
func (s *Scheduler) Next() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next
}

// Reset sets the timer to the nearest run of the saved automations.
func (s *Scheduler) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil || s.ctx.Err() != nil {
		return
	}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.next = time.Time{}
	list, err := s.Store.List()
	if err != nil {
		return
	}
	now := s.now()
	for _, a := range list {
		if t, ok := a.Schedule.Next(now); ok && (s.next.IsZero() || t.Before(s.next)) {
			s.next = t
		}
	}
	if s.next.IsZero() {
		return
	}
	s.timer = time.AfterFunc(s.next.Sub(now), s.fire)
}

// fire runs every automation due now, each in its own goroutine (the model queue orders them),
// and sets the next timer.
func (s *Scheduler) fire() {
	s.mu.Lock()
	ctx, due := s.ctx, s.next
	if s.running == nil {
		s.running = map[string]bool{}
	}
	s.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return
	}
	list, _ := s.Store.List()
	for _, a := range list {
		// A run is due when its next time from just before the timer's moment is that moment.
		if t, ok := a.Schedule.Next(due.Add(-time.Second)); !ok || t.After(due) {
			continue
		}
		s.mu.Lock()
		busy := s.running[a.ID]
		s.running[a.ID] = true
		s.mu.Unlock()
		if busy {
			continue
		}
		go func(a Automation) {
			defer func() {
				s.mu.Lock()
				delete(s.running, a.ID)
				s.mu.Unlock()
			}()
			s.Run(ctx, a)
		}(a)
	}
	s.Reset()
}
