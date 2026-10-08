package telemetry

import (
	"context"
	"math"
	"sync"
	"time"
)

// Sample is one snapshot of the machine.
type Sample struct {
	Timestamp  time.Time `json:"timestamp"`
	CPUPercent *float64  `json:"cpu_percent"`
	RAMTotalGB *float64  `json:"ram_total_gb"`
	RAMUsedGB  *float64  `json:"ram_used_gb"`
	RAMPercent *float64  `json:"ram_percent"`
	GPU        []GPU     `json:"gpu"`
	Errors     []string  `json:"errors"`
}

// cpuWindow is how long CPU load is measured; the sample is a short average, not a counter.
const cpuWindow = 250 * time.Millisecond

func round(v float64, digits int) *float64 {
	p := math.Pow(10, float64(digits))
	r := math.Round(v*p) / p
	return &r
}

// Take measures CPU, RAM and GPUs once.
func Take(ctx context.Context) Sample {
	s := Sample{Timestamp: time.Now().UTC(), Errors: []string{}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.GPU = GPUs(ctx)
	}()
	if cpu, err := cpuPercent(ctx, cpuWindow); err == nil {
		s.CPUPercent = round(math.Max(0, math.Min(100, cpu)), 1)
	} else {
		s.Errors = append(s.Errors, "CPU: источник недоступен.")
	}
	if total, avail, err := memory(); err == nil && total > 0 {
		used := total - avail
		s.RAMTotalGB = round(float64(total)/(1<<30), 2)
		s.RAMUsedGB = round(float64(used)/(1<<30), 2)
		s.RAMPercent = round(float64(used)/float64(total)*100, 1)
	} else {
		s.Errors = append(s.Errors, "RAM: источник недоступен.")
	}
	wg.Wait()
	return s
}

// Sampler shares one recent sample between requests, so several open tabs do not multiply
// the work: a new measurement starts at most every three seconds.
type Sampler struct {
	mu   sync.Mutex
	last Sample
	at   time.Time
	Now  func() time.Time
	Take func(context.Context) Sample
}

// Get returns a sample no older than three seconds.
func (s *Sampler) Get(ctx context.Context) Sample {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	take := Take
	if s.Take != nil {
		take = s.Take
	}
	if s.at.IsZero() || now().Sub(s.at) >= 3*time.Second {
		s.last = take(ctx)
		s.at = now()
	}
	return s.last
}

// RAMTotal is the computer's memory in bytes, 0 when unknown.
func RAMTotal() int64 {
	total, _, err := memory()
	if err != nil {
		return 0
	}
	return int64(total)
}
