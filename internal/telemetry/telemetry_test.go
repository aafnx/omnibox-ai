package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestParseGPUsKeepsMissingReadingsEmpty(t *testing.T) {
	gpus := parseGPUs([]byte("NVIDIA GeForce RTX 5080, 16303, 15020, 958, 97, 66, 287.5, [N/A]\nbad line\n"))
	if len(gpus) != 1 {
		t.Fatalf("%+v", gpus)
	}
	g := gpus[0]
	if g.Name != "NVIDIA GeForce RTX 5080" || g.TotalMB != 16303 || g.FreeMB != 958 || *g.Utilization != 97 || *g.PowerWatts != 287.5 || g.FanPercent != nil {
		t.Fatalf("%+v", g)
	}
}

func TestGPUsWithoutDriverIsEmpty(t *testing.T) {
	saved := RunCommand
	defer func() { RunCommand = saved }()
	RunCommand = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("not found") }
	if g := GPUs(context.Background()); g == nil || len(g) != 0 {
		t.Fatalf("%v", g)
	}
}

func TestTakeMeasuresThisMachine(t *testing.T) {
	saved := RunCommand
	defer func() { RunCommand = saved }()
	RunCommand = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("no gpu") }
	s := Take(context.Background())
	if s.CPUPercent == nil || s.RAMTotalGB == nil || *s.RAMTotalGB <= 0 || len(s.Errors) != 0 {
		t.Fatalf("%+v", s)
	}
}

func TestSamplerSharesRecentSample(t *testing.T) {
	now := time.Unix(0, 0)
	calls := 0
	s := &Sampler{Now: func() time.Time { return now }, Take: func(context.Context) Sample { calls++; return Sample{} }}
	s.Get(context.Background())
	now = now.Add(2 * time.Second)
	s.Get(context.Background())
	if calls != 1 {
		t.Fatalf("calls %d", calls)
	}
	now = now.Add(time.Second)
	s.Get(context.Background())
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
}
