package status

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

type fakeActivity struct{ last time.Time }

func (f fakeActivity) IsActive(string) bool          { return true }
func (f fakeActivity) LastActivity(string) time.Time { return f.last }

func port(t *testing.T, url string) int {
	t.Helper()
	p, err := strconv.Atoi(url[strings.LastIndex(url, ":")+1:])
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStoppedEnginesAreNeverContacted(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a-Q4_K_M.gguf"), make([]byte, 10), 0o644)
	cfg := config.Default()
	cfg.Engines[config.PrismML] = &config.Engine{Port: port(t, srv.URL), ModelsDirectory: dir, Launch: config.Launch{Settings: map[string]string{}}}
	cfg.Engines[config.Ollama] = &config.Engine{Port: port(t, srv.URL) + 1, Launch: config.Launch{Settings: map[string]string{}}}
	services := platform.NewFakeServices()
	services.Set(config.ServiceName(config.PrismML), platform.Stopped)
	b := &Builder{Config: func() *config.Config { return cfg }, Services: services, DataDir: t.TempDir()}
	d := b.Build(context.Background(), false)
	if hits.Load() != 0 {
		t.Fatalf("stopped engines were contacted %d times", hits.Load())
	}
	if len(d.Engines) != 2 || d.Engines[0].Name != "prismml" || d.Engines[0].Service != "Stopped" || d.Engines[1].Service != "Missing" {
		t.Fatalf("%+v", d.Engines)
	}
	if len(d.Engines[0].Models) != 1 || d.Engines[0].Idle.State != "stopped" || d.Engines[0].Endpoint != "http://127.0.0.1:"+strconv.Itoa(port(t, srv.URL))+"/v1" {
		t.Fatalf("%+v", d.Engines[0])
	}
}

func TestRunningPrismShowsPropsResidencyAndIdle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"is_sleeping":false,"model_path":"D:\\models\\ornith-Q4_K_M.gguf","default_generation_settings":{"n_ctx":32768}}`))
	}))
	defer srv.Close()
	data := t.TempDir()
	started := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	gpu, total := 49, 49
	if err := engine.WriteRuntime(data, config.PrismML, engine.Runtime{Running: true, Started: started, GPULayers: &gpu, TotalLayers: &total}); err != nil {
		t.Fatal(err)
	}
	e := &config.Engine{Port: port(t, srv.URL), Launch: config.Launch{Settings: map[string]string{"alias": "new", "idle_minutes": "30", "n_cpu_moe": "24"}}}
	e.Previous = &config.Launch{Settings: map[string]string{"alias": "old"}}
	e.Pending = true
	cfg := config.Default()
	cfg.Engines[config.PrismML] = e
	services := platform.NewFakeServices()
	services.Set(config.ServiceName(config.PrismML), platform.Running)
	last := started.Add(10 * time.Minute)
	b := &Builder{Config: func() *config.Config { return cfg }, Services: services, DataDir: data, Activity: fakeActivity{last: last}}
	row := b.Build(context.Background(), false).Engines[0]
	if row.API != "Ready" || *row.ActiveContext != 32768 || row.LoadedModel != "ornith-Q4_K_M.gguf" || *row.Sleeping {
		t.Fatalf("%+v", row)
	}
	if row.SelectedModel != "old" {
		t.Fatalf("a pending change still serves the previous model, got %q", row.SelectedModel)
	}
	// The pending launch has no MoE offload yet; the running one decides.
	if row.Residency.Mode != "vram" || row.Residency.CPUMoELayers != nil {
		t.Fatalf("residency %+v", row.Residency)
	}
	e.Pending, e.Previous = false, nil
	row = b.Build(context.Background(), false).Engines[0]
	if row.Residency.Mode != "hybrid" || *row.Residency.CPUMoELayers != 24 || !row.Idle.Busy {
		t.Fatalf("residency %+v idle %+v", row.Residency, row.Idle)
	}
	if want := last.Add(30 * time.Minute); row.Idle.ExpiresAt == nil || !row.Idle.ExpiresAt.Equal(want) || !row.Idle.Estimated {
		t.Fatalf("idle %+v", row.Idle)
	}
}

func TestRunningOllamaReportsLoadedModelAndPlacement(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			w.Write([]byte(`{"version":"0.12.0"}`))
		case "/api/ps":
			w.Write([]byte(`{"models":[{"name":"qwen3:8b","size":1000,"size_vram":600,"expires_at":"2026-10-07T11:00:00Z","context_length":8192}]}`))
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"qwen3:8b","size":5000}]}`))
		}
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.Engines[config.Ollama] = &config.Engine{Port: port(t, srv.URL), Model: "qwen3:8b", Launch: config.Launch{Settings: map[string]string{"keep_alive": "30m"}}}
	services := platform.NewFakeServices()
	services.Set(config.ServiceName(config.Ollama), platform.Running)
	b := &Builder{Config: func() *config.Config { return cfg }, Services: services, DataDir: t.TempDir()}
	row := b.Build(context.Background(), false).Engines[0]
	if row.API != "Ready" || row.LoadedModel != "qwen3:8b" || *row.ActiveContext != 8192 || row.SelectedModel != "qwen3:8b" || len(row.Models) != 1 {
		t.Fatalf("%+v", row)
	}
	if row.Residency.Mode != "hybrid" || *row.Residency.RAMBytes != 400 || row.Idle.State != "loaded" || row.Idle.ExpiresAt == nil {
		t.Fatalf("%+v %+v", row.Residency, row.Idle)
	}
	if row.Settings["idle_minutes"] != 30 {
		t.Fatalf("settings %v", row.Settings)
	}
}

func TestUnavailableApiIsReportedNotHidden(t *testing.T) {
	cfg := config.Default()
	cfg.Engines[config.PrismML] = &config.Engine{Port: 1, Launch: config.Launch{Settings: map[string]string{}}}
	services := platform.NewFakeServices()
	services.Set(config.ServiceName(config.PrismML), platform.Running)
	b := &Builder{Config: func() *config.Config { return cfg }, Services: services, DataDir: t.TempDir()}
	if row := b.Build(context.Background(), false).Engines[0]; row.API != "Unavailable" || row.Idle.State != "unknown" {
		t.Fatalf("%+v", row)
	}
}
