package main

import (
	"context"
	"testing"
	"time"
)

// Jobs live in memory: a finished one is read for an hour, the oldest makes room when there are
// too many, and stopping one cancels its work.
func TestAutomationJobs(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	j := &autoJobs{now: func() time.Time { return now }}
	wait := func(id string) map[string]any {
		for i := 0; i < 500; i++ {
			s, err := j.state(id)
			if err != nil {
				t.Fatal(err)
			}
			if s["done"] == true {
				return s
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("job did not end")
		return nil
	}
	first, _ := j.start(func(_ context.Context, trace func(string)) (any, error) {
		trace("шаг")
		return map[string]any{"run": "ok"}, nil
	})
	if s := wait(first); s["run"] != "ok" || len(s["trace"].([]string)) != 1 {
		t.Fatalf("%+v", s)
	}
	for i := 0; i < maxJobs; i++ {
		now = now.Add(time.Second)
		id, err := j.start(func(context.Context, func(string)) (any, error) { return nil, nil })
		if err != nil {
			t.Fatal(err)
		}
		wait(id)
	}
	if _, err := j.state(first); err == nil {
		t.Fatal("the oldest finished job made no room")
	}
	stopped := make(chan error, 1)
	id, _ := j.start(func(ctx context.Context, _ func(string)) (any, error) {
		<-ctx.Done()
		stopped <- ctx.Err()
		return nil, ctx.Err()
	})
	if err := j.stop(id); err != nil || <-stopped == nil {
		t.Fatal("stop did not cancel")
	}
	if s := wait(id); s["error"] == nil {
		t.Fatalf("%+v", s)
	}
	now = now.Add(jobKeep + time.Minute)
	if _, err := j.state(id); err == nil {
		t.Fatal("a job outlived its hour")
	}
}
