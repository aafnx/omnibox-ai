package control

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/enginemgr"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// withManager gives the rig an engine manager backed by a fake GitHub with versions b1 and b2.
func (r *rig) withManager(t *testing.T) {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("bin/engine")
	f.Write([]byte("engine"))
	w.Close()
	archive := buf.Bytes()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/repos/") {
			var out []map[string]any
			for _, tag := range []string{"b2", "b1"} {
				out = append(out, map[string]any{"tag_name": tag, "published_at": "2026-10-01T00:00:00Z",
					"assets": []any{map[string]any{"name": "engine-" + tag + ".zip", "browser_download_url": srv.URL + "/dl/" + tag, "size": len(archive)}}})
			}
			json.NewEncoder(w).Encode(out)
			return
		}
		w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	kind := enginemgr.Kind{Repo: "test/engine", Exe: "engine", AssetSets: [][]*regexp.Regexp{{regexp.MustCompile(`^engine-.+\.zip$`)}}}
	r.c.DataDir = filepath.Dir(r.store.Get().Engines[config.PrismML].ModelsDirectory)
	r.c.Executable = "/opt/omnibox/omnibox"
	r.c.Manager = &enginemgr.Manager{DataDir: r.c.DataDir, API: srv.URL, Download: func(string) string { return srv.URL + "/dl/" },
		Kinds: map[string]enginemgr.Kind{config.PrismML: kind, config.Ollama: kind}, Check: func(context.Context, string) error { return nil }}
}

func TestInstallEngineRegistersServiceAndSettings(t *testing.T) {
	r := newRig(t)
	r.withManager(t)
	if _, err := r.do(Request{Action: "engine-install", EngineID: config.PrismML}); err == nil || !strings.Contains(err.Error(), "уже установлен") {
		t.Fatalf("installed engine: %v", err)
	}
	r.store.Update(func(c *config.Config) error { delete(c.Engines, config.Ollama); return nil })
	r.services.Remove(config.ServiceName(config.Ollama), 0)
	message, err := r.do(Request{Action: "engine-install", EngineID: config.Ollama})
	if err != nil {
		t.Fatal(err)
	}
	e := r.store.Get().Engines[config.Ollama]
	spec, ok := r.services.Spec(config.ServiceName(config.Ollama))
	if !ok || e == nil || !strings.Contains(message, "b2") || e.Port != 8802 || r.c.Manager.TagOf(config.Ollama, e.Executable) != "b2" || e.Settings["keep_alive"] != "30m" {
		t.Fatalf("%s: %+v %+v", message, e, spec)
	}
	if spec.Executable != "/opt/omnibox/omnibox" || !slices.Equal(spec.Args, []string{"engine", "ollama", "--data", r.c.DataDir}) || spec.Automatic {
		t.Fatalf("service %+v", spec)
	}
	if info, _ := r.services.Query(config.ServiceName(config.Ollama)); info.State != platform.Stopped {
		t.Fatalf("a new engine started: %s", info.State)
	}
	if _, err := os.Stat(e.ModelsDirectory); err != nil {
		t.Fatal("models directory not created")
	}
}

func TestVersionSwitchOfStoppedEngineWaitsForNextStart(t *testing.T) {
	r := newRig(t)
	r.withManager(t)
	message, err := r.do(Request{Action: "engine-update", EngineID: config.PrismML, Version: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	e := r.store.Get().Engines[config.PrismML]
	if !e.Pending || r.c.Manager.TagOf(config.PrismML, e.Executable) != "b1" || !strings.Contains(message, "остановлен") || slices.Contains(r.services.Calls, "start:omnibox-prismml") {
		t.Fatalf("%s %+v %v", message, e, r.services.Calls)
	}
	if e.Previous == nil || e.Previous.Executable != "/opt/omnibox/engines/prismml/llama-server" {
		t.Fatal("rollback point lost")
	}
}

type fakeReserver struct {
	mu         sync.Mutex
	generating int // polls until the generation ends
	reserved   bool
	ended      int
}

func (f *fakeReserver) Reserve() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reserved = true
	return true
}
func (f *fakeReserver) Generating() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generating--
	return f.generating >= 0
}
func (f *fakeReserver) EndControl() { f.mu.Lock(); f.ended++; f.mu.Unlock() }

func TestVersionSwitchOfRunningEngineWaitsForGenerationAndRestarts(t *testing.T) {
	r := newRig(t)
	r.withManager(t)
	if _, err := r.do(Request{Action: "start", Engine: config.PrismML}); err != nil {
		t.Fatal(err)
	}
	gate := &fakeReserver{generating: 2}
	r.c.Gate = gate
	if _, err := r.do(Request{Action: "engine-update", EngineID: config.PrismML, Version: "b2"}); err != nil {
		t.Fatal(err)
	}
	e := r.store.Get().Engines[config.PrismML]
	if e.Pending || r.c.Manager.TagOf(config.PrismML, e.Executable) != "b2" || r.state(config.PrismML) != platform.Running {
		t.Fatalf("%+v", e)
	}
	if !gate.reserved || gate.ended != 1 || gate.generating > -1 {
		t.Fatalf("gate %+v", gate)
	}
	old := GenerationWait
	GenerationWait = 50 * time.Millisecond
	defer func() { GenerationWait = old }()
	r.c.Gate = &fakeReserver{generating: 1 << 30}
	if _, err := r.do(Request{Action: "engine-update", EngineID: config.PrismML, Version: "b1"}); err == nil || !strings.Contains(err.Error(), "занята генерацией") {
		t.Fatalf("busy model: %v", err)
	}
}

func TestRemoveEngineKeepsModelsAndRefusesTheLastOne(t *testing.T) {
	r := newRig(t)
	r.withManager(t)
	r.services.Set(config.ServiceName(config.Ollama), platform.Running)
	message, err := r.do(Request{Action: "engine-remove", EngineID: config.Ollama})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.store.Get().Engines[config.Ollama]; ok || !slices.Contains(r.services.Calls, "remove:omnibox-ollama") || !strings.Contains(message, "вне OmniBox") {
		t.Fatalf("%s %v", message, r.services.Calls)
	}
	if _, err := r.do(Request{Action: "engine-remove", EngineID: config.PrismML}); err == nil || !strings.Contains(err.Error(), "единственный") {
		t.Fatalf("last engine: %v", err)
	}
}

func TestDeleteModelRefusesModelsInUse(t *testing.T) {
	r := newRig(t)
	dir := r.store.Get().Engines[config.PrismML].ModelsDirectory
	if _, err := r.do(Request{Action: "model-delete", Engine: config.PrismML, Model: filepath.Join(dir, "good-Q4_K_M.gguf")}); err == nil || !strings.Contains(err.Error(), "выбрана в настройках") {
		t.Fatalf("selected model: %v", err)
	}
	if _, err := r.do(Request{Action: "model-delete", Engine: config.PrismML, Model: filepath.Join(dir, "..", "config.json")}); err == nil {
		t.Fatal("a path outside the model list was accepted")
	}
	message, err := r.do(Request{Action: "model-delete", Engine: config.PrismML, Model: filepath.Join(dir, "bad-Q4_K_M.gguf")})
	if err != nil || !strings.Contains(message, "удалена") {
		t.Fatalf("%s %v", message, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bad-Q4_K_M.gguf")); err == nil {
		t.Fatal("model kept")
	}
	if _, err := r.do(Request{Action: "model-delete", Engine: config.Ollama, Model: "qwen3:8b"}); err == nil || !strings.Contains(err.Error(), "Запустите Ollama") {
		t.Fatalf("stopped Ollama: %v", err)
	}
}

func TestEngineActionsNeedAKnownEngine(t *testing.T) {
	if _, err := ParseRequest(map[string]any{"action": "engine-install", "engine": "vllm"}); err == nil {
		t.Fatal("unknown engine accepted")
	}
	req, err := ParseRequest(map[string]any{"action": "engine-update", "engine": "ollama", "version": "v0.12.0"})
	if err != nil || req.Engine != config.Ollama || req.Version != "v0.12.0" {
		t.Fatalf("%+v %v", req, err)
	}
}
