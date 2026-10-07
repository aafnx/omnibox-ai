package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// rig is a controller with fake services and fake engines that serve what the engine host would
// have started: the launch saved in config.json at the moment the service started.
type rig struct {
	t        *testing.T
	store    *config.Store
	services *platform.FakeServices
	c        *Controller
	mu       sync.Mutex
	launched map[string]map[string]string
	requests []string
	loaded   []string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	dir := t.TempDir()
	models := filepath.Join(dir, "models")
	os.MkdirAll(models, 0o755)
	for _, m := range []string{"good-Q4_K_M.gguf", "bad-Q4_K_M.gguf", "next-Q2_0.gguf"} {
		os.WriteFile(filepath.Join(models, m), make([]byte, 16), 0o644)
	}
	cfg := config.Default()
	cfg.Panel.Hosts = []string{"localhost"}
	b64 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg.Panel.Password = config.Password{Salt: b64, Hash: b64, Iterations: 210000}
	cfg.Engines[config.PrismML] = &config.Engine{Port: 8801, ModelsDirectory: models, Launch: config.Launch{Executable: "/opt/omnibox/engines/prismml/llama-server", Settings: map[string]string{"model": filepath.Join(models, "good-Q4_K_M.gguf"), "alias": "good", "context": "8192"}}}
	cfg.Engines[config.Ollama] = &config.Engine{Port: 8802, Launch: config.Launch{Executable: "/opt/omnibox/engines/ollama/ollama", Settings: map[string]string{"keep_alive": "30m"}}}
	path := filepath.Join(dir, "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, store: config.NewStore(path, cfg), services: platform.NewFakeServices(), launched: map[string]map[string]string{}}
	r.services.Set(config.ServiceName(config.PrismML), platform.Stopped)
	r.services.Set(config.ServiceName(config.Ollama), platform.Stopped)
	r.services.OnStart = func(service string) error {
		name := strings.TrimPrefix(service, "omnibox-")
		r.mu.Lock()
		r.launched[name] = r.store.Get().Engines[name].Settings
		r.mu.Unlock()
		return nil
	}
	servers := map[string]*httptest.Server{}
	for _, name := range config.EngineNames {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { r.serve(name, w, req) }))
		t.Cleanup(srv.Close)
		servers[name] = srv
	}
	r.c = &Controller{Store: r.store, Services: r.services, Wait: 5 * time.Millisecond, StartupTimeout: 300 * time.Millisecond,
		Client: func(name string, e *config.Engine) *engine.Client {
			return &engine.Client{BaseURL: servers[name].URL, HTTP: &http.Client{Timeout: 2 * time.Second}}
		}}
	return r
}

func (r *rig) serve(name string, w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if info, _ := r.services.Query(config.ServiceName(name)); info.State != platform.Running {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	var body map[string]any
	json.NewDecoder(req.Body).Decode(&body)
	r.requests = append(r.requests, name+" "+req.URL.Path)
	switch req.URL.Path {
	case "/health":
		if strings.Contains(r.launched[name]["model"], "bad") {
			http.Error(w, "loading", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"status":"ok"}`))
	case "/api/version":
		w.Write([]byte(`{"version":"0.12.0"}`))
	case "/api/tags":
		w.Write([]byte(`{"models":[{"name":"qwen3:8b","size":5000},{"name":"gemma3:4b","size":3000}]}`))
	case "/api/ps":
		out := []map[string]any{}
		for _, m := range r.loaded {
			out = append(out, map[string]any{"name": m, "size": 10, "size_vram": 10})
		}
		json.NewEncoder(w).Encode(map[string]any{"models": out})
	case "/api/generate":
		model, _ := body["model"].(string)
		r.loaded = slices.DeleteFunc(r.loaded, func(m string) bool { return m == model })
		if keep, ok := body["keep_alive"].(float64); !ok || keep != 0 {
			r.loaded = append(r.loaded, model)
		}
		w.Write([]byte(`{"done":true}`))
	case "/api/pull":
		if body["stream"] != true {
			http.Error(w, "stream expected", http.StatusBadRequest)
			return
		}
		if body["model"] == "broken:1b" {
			w.Write([]byte(`{"status":"pulling manifest"}` + "\n" + `{"error":"file does not exist"}` + "\n"))
			return
		}
		w.Write([]byte(`{"status":"pulling manifest"}` + "\n" + `{"status":"pulling abc","total":100,"completed":40}` + "\n" + `{"status":"pulling abc","total":100,"completed":100}` + "\n" + `{"status":"success"}` + "\n"))
	default:
		http.NotFound(w, req)
	}
}

func (r *rig) model(file string) string {
	return filepath.Join(r.store.Get().Engines[config.PrismML].ModelsDirectory, file)
}

func (r *rig) state(name string) string {
	info, _ := r.services.Query(config.ServiceName(name))
	return info.State
}

func (r *rig) do(req Request) (string, error) {
	return r.c.Do(context.Background(), req)
}

func TestStartStopsTheOtherEngineAndWaitsForTheAPI(t *testing.T) {
	r := newRig(t)
	r.services.Set(config.ServiceName(config.Ollama), platform.Running)
	if _, err := r.do(Request{Action: "start", Engine: config.PrismML}); err != nil {
		t.Fatal(err)
	}
	if r.state(config.PrismML) != platform.Running || r.state(config.Ollama) != platform.Stopped {
		t.Fatalf("prism %s, ollama %s", r.state(config.PrismML), r.state(config.Ollama))
	}
	stop := slices.Index(r.services.Calls, "stop:omnibox-ollama")
	start := slices.Index(r.services.Calls, "start:omnibox-prismml")
	if stop < 0 || start < stop {
		t.Fatalf("the other engine must stop first: %v", r.services.Calls)
	}
	if !slices.Contains(r.requests, "prismml /health") {
		t.Fatalf("start did not wait for the API: %v", r.requests)
	}
}

func TestStartRefusesWithoutModelOrDisabledService(t *testing.T) {
	r := newRig(t)
	r.store.Update(func(c *config.Config) error { delete(c.Engines[config.PrismML].Settings, "model"); return nil })
	if _, err := r.do(Request{Action: "start", Engine: config.PrismML}); err == nil || !strings.Contains(err.Error(), "модель не выбрана") {
		t.Fatalf("%v", err)
	}
	if len(r.services.Calls) != 0 {
		t.Fatalf("a refused start touched services: %v", r.services.Calls)
	}
}

func TestFailedChangeOnStoppedEngineRollsBackAndStaysStopped(t *testing.T) {
	r := newRig(t)
	_, err := r.do(Request{Action: "apply", Engine: config.PrismML, Settings: map[string]string{"model": r.model("bad-Q4_K_M.gguf")}})
	if err == nil || !strings.Contains(err.Error(), "оставлен остановленным") {
		t.Fatalf("%v", err)
	}
	e := r.store.Get().Engines[config.PrismML]
	if r.state(config.PrismML) != platform.Stopped || e.Pending || !strings.Contains(e.Settings["model"], "good") {
		t.Fatalf("state %s, pending %v, model %s", r.state(config.PrismML), e.Pending, e.Settings["model"])
	}
	saved, err := config.Load(filepath.Join(filepath.Dir(e.ModelsDirectory), "config.json"))
	if err != nil || !strings.Contains(saved.Engines[config.PrismML].Settings["model"], "good") {
		t.Fatalf("rollback was not saved: %v", err)
	}
}

func TestFailedChangeOnRunningEngineRestartsPreviousSettings(t *testing.T) {
	r := newRig(t)
	if _, err := r.do(Request{Action: "start", Engine: config.PrismML}); err != nil {
		t.Fatal(err)
	}
	_, err := r.do(Request{Action: "apply", Engine: config.PrismML, Settings: map[string]string{"model": r.model("bad-Q4_K_M.gguf")}})
	if err == nil || !strings.Contains(err.Error(), "снова запущен") {
		t.Fatalf("%v", err)
	}
	if r.state(config.PrismML) != platform.Running || !strings.Contains(r.launched[config.PrismML]["model"], "good") {
		t.Fatalf("state %s, launched %v", r.state(config.PrismML), r.launched[config.PrismML])
	}
}

func TestSaveKeepsRunningEngineAndStartRefusesPendingChange(t *testing.T) {
	r := newRig(t)
	if _, err := r.do(Request{Action: "start", Engine: config.PrismML}); err != nil {
		t.Fatal(err)
	}
	calls := len(r.services.Calls)
	if _, err := r.do(Request{Action: "save", Engine: config.PrismML, Settings: map[string]string{"model": r.model("next-Q2_0.gguf")}}); err != nil {
		t.Fatal(err)
	}
	if len(r.services.Calls) != calls || !r.store.Get().Engines[config.PrismML].Pending {
		t.Fatalf("save restarted the engine or did not stage: %v", r.services.Calls)
	}
	if _, err := r.do(Request{Action: "start", Engine: config.PrismML}); err == nil || !strings.Contains(err.Error(), "Перезапустите") {
		t.Fatalf("%v", err)
	}
	if _, err := r.do(Request{Action: "restart", Engine: config.PrismML}); err != nil {
		t.Fatal(err)
	}
	e := r.store.Get().Engines[config.PrismML]
	if e.Pending || !strings.Contains(r.launched[config.PrismML]["model"], "next") || !e.CanRestore() {
		t.Fatalf("pending %v, launched %v", e.Pending, r.launched[config.PrismML])
	}
	if _, err := r.do(Request{Action: "restore", Engine: config.PrismML}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.launched[config.PrismML]["model"], "good") || r.state(config.PrismML) != platform.Running {
		t.Fatalf("restore did not restart on the previous model: %v", r.launched[config.PrismML])
	}
}

func TestStoppedEngineStaysStoppedAfterRestore(t *testing.T) {
	r := newRig(t)
	if _, err := r.do(Request{Action: "save", Engine: config.PrismML, Settings: map[string]string{"model": r.model("next-Q2_0.gguf")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.do(Request{Action: "restore", Engine: config.PrismML}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(r.services.Calls, "start:omnibox-prismml") {
		t.Fatalf("restore started a stopped engine: %v", r.services.Calls)
	}
}

func TestOllamaLoadKeepsOneModelAndPullNeedsRunningServer(t *testing.T) {
	r := newRig(t)
	if _, err := r.do(Request{Action: "pull", Engine: config.Ollama, Model: "qwen3:8b"}); err == nil || !strings.Contains(err.Error(), "Сначала запустите") {
		t.Fatalf("%v", err)
	}
	if slices.Contains(r.services.Calls, "start:omnibox-ollama") {
		t.Fatal("pull started the server")
	}
	if _, err := r.do(Request{Action: "load", Engine: config.Ollama, Model: "qwen3:8b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.do(Request{Action: "load", Engine: config.Ollama, Model: "gemma3:4b"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.loaded, []string{"gemma3:4b"}) || r.store.Get().Engines[config.Ollama].Model != "gemma3:4b" {
		t.Fatalf("loaded %v", r.loaded)
	}
	if _, err := r.do(Request{Action: "load", Engine: config.Ollama, Model: "missing:1b"}); err == nil || !strings.Contains(err.Error(), "не установлена") {
		t.Fatalf("%v", err)
	}
	if _, err := r.do(Request{Action: "pull", Engine: config.Ollama, Model: "qwen3:8b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.do(Request{Action: "unload", Engine: config.Ollama}); err != nil {
		t.Fatal(err)
	}
	if len(r.loaded) != 0 || r.state(config.Ollama) != platform.Running {
		t.Fatalf("unload: loaded %v, state %s", r.loaded, r.state(config.Ollama))
	}
}

func TestAutoRestartIsAnExplicitSetting(t *testing.T) {
	r := newRig(t)
	if r.store.Get().Engines[config.PrismML].AutoRestart {
		t.Fatal("auto-restart must be off by default")
	}
	req, err := ParseRequest(map[string]any{"action": "auto-restart", "backend": "prismml", "enabled": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.do(req); err != nil || !r.store.Get().Engines[config.PrismML].AutoRestart {
		t.Fatalf("not turned on: %v", err)
	}
	if r.state(config.ServiceName(config.PrismML)) == platform.Running {
		t.Fatal("turning auto-restart on started the engine")
	}
	if _, err := r.do(Request{Action: "auto-restart", Engine: config.PrismML, Enabled: "maybe"}); err == nil {
		t.Fatal("a wrong value accepted")
	}
	if _, err := r.do(Request{Action: "auto-restart", Engine: config.PrismML, Enabled: "false"}); err != nil || r.store.Get().Engines[config.PrismML].AutoRestart {
		t.Fatalf("not turned off: %v", err)
	}
}

func TestParseRequestRejectsUnknownFieldsAndActions(t *testing.T) {
	if _, err := ParseRequest(map[string]any{"action": "start", "command": "calc.exe"}); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := ParseRequest(map[string]any{"action": "exec"}); err == nil {
		t.Fatal("unknown action accepted")
	}
	if _, err := ParseRequest(map[string]any{"action": "apply", "settings": map[string]any{"x": []any{1}}}); err == nil {
		t.Fatal("nested setting accepted")
	}
	r, err := ParseRequest(map[string]any{"action": "apply", "backend": "prismml", "settings": map[string]any{"context": float64(32768), "flash_attn": true}})
	if err != nil || r.Settings["context"] != "32768" || r.Settings["flash_attn"] != "true" {
		t.Fatalf("%+v %v", r, err)
	}
}

type gate struct {
	mu            sync.Mutex
	busy          bool
	began, cancel []bool
	ended         int
}

func (g *gate) BeginControl(cancel bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cancel = append(g.cancel, cancel)
	return !g.busy || cancel
}

func (g *gate) EndControl() { g.mu.Lock(); g.ended++; g.mu.Unlock() }

func TestOperationsRunOnePerCategoryAndReleaseTheGate(t *testing.T) {
	release := make(chan struct{})
	g := &gate{busy: true}
	ops := &Operations{Gate: g, Run: func(ctx context.Context, r Request) (string, error) {
		if r.Action == "pull" {
			<-release
		}
		return "ok " + r.Action, nil
	}}
	if _, err := ops.Start(Request{Action: "start", Engine: "prismml"}); err != ErrGenerating {
		t.Fatalf("start during a generation: %v", err)
	}
	if _, err := ops.Start(Request{Action: "pull", Engine: "ollama"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.Start(Request{Action: "download"}); err != ErrBusy {
		t.Fatalf("second download: %v", err)
	}
	if _, err := ops.Start(Request{Action: "stop", Engine: "prismml"}); err != nil {
		t.Fatalf("stop cancels generations: %v", err)
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for ops.Running() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	list := ops.List()
	if len(list) != 2 || list[0].State != "done" || list[1].State != "done" {
		t.Fatalf("%+v", list)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ended != 1 || !slices.Equal(g.cancel, []bool{false, true}) {
		t.Fatalf("gate: cancel %v, ended %d", g.cancel, g.ended)
	}
}

func TestOperationsKeepRecentFinishedAndReportFailures(t *testing.T) {
	ops := &Operations{Run: func(ctx context.Context, r Request) (string, error) {
		if r.Engine == "boom" {
			panic("boom")
		}
		return "ok", nil
	}}
	base := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for i := range 25 {
		ops.Now = func() time.Time { return base.Add(time.Duration(i) * time.Second) }
		if _, err := ops.Start(Request{Action: "stop", Engine: map[bool]string{true: "boom", false: "prismml"}[i == 24]}); err != nil {
			t.Fatal(err)
		}
		for ops.Running() {
			time.Sleep(time.Millisecond)
		}
	}
	list := ops.List()
	if len(list) != keepFinished || list[0].State != "failed" || !strings.Contains(list[0].Message, "прервана") {
		t.Fatalf("%d %+v", len(list), list[0])
	}
}
