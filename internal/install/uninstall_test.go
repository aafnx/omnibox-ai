package install

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// installed returns a rig with the program installed and an engine service and model in place.
func installed(t *testing.T) *rig {
	t.Helper()
	r := newRig(t, "")
	r.Yes = true
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.services.Install(engineSpec(config.Ollama, r.binary(), r.DataDir))
	os.MkdirAll(filepath.Join(r.DataDir, "models", "ollama"), 0o755)
	os.WriteFile(filepath.Join(r.DataDir, "models", "m-Q4_K_M.gguf"), []byte("model"), 0o644)
	r.Yes = false
	return r
}

func (r *rig) answer(lines ...string) {
	r.In = strings.NewReader(strings.Join(lines, "\n") + "\n")
	r.in = bufio.NewReader(r.In)
}

func TestUninstallKeepsDataAndModelsByDefault(t *testing.T) {
	r := installed(t)
	var path []string
	r.RemoveFromPath = func(dir string) error { path = append(path, dir); return nil }
	r.answer("y", "", "")
	if err := r.Uninstall(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	for _, name := range []string{config.PanelService, config.ServiceName(config.Ollama)} {
		if s, _ := r.services.Query(name); s.State != platform.Missing {
			t.Fatalf("%s left", name)
		}
	}
	if shim, _ := r.shim(); fileExists(shim) || fileExists(r.ProgramDir) || len(r.rules) != 0 {
		t.Fatal("program, ai command or firewall rule left")
	}
	if !fileExists(r.configPath()) || !fileExists(filepath.Join(r.DataDir, "models", "m-Q4_K_M.gguf")) {
		t.Fatal("data or models removed without asking")
	}
}

func TestUninstallCanRemoveDataButKeepModels(t *testing.T) {
	r := installed(t)
	r.answer("y", "y", "n")
	if err := r.Uninstall(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if fileExists(r.configPath()) || fileExists(filepath.Join(r.DataDir, "logs")) {
		t.Fatal("data left")
	}
	if !fileExists(filepath.Join(r.DataDir, "models", "m-Q4_K_M.gguf")) {
		t.Fatal("models removed although kept")
	}
	if !strings.Contains(r.out.String(), "модели оставлены") {
		t.Fatalf("%s", r.out)
	}
}

func TestUninstallAnsweredNoChangesNothing(t *testing.T) {
	r := installed(t)
	r.answer("")
	if err := r.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s, _ := r.services.Query(config.PanelService); s.State == platform.Missing || !fileExists(r.binary()) {
		t.Fatal("a declined uninstall removed something")
	}
}

func TestUninstallEverything(t *testing.T) {
	r := installed(t)
	r.answer("y", "y", "y")
	if err := r.Uninstall(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if fileExists(r.DataDir) {
		t.Fatal("data directory left")
	}
}

func TestUninstallAllRemovesEverythingAfterOneQuestion(t *testing.T) {
	r := installed(t)
	// A models directory outside the data directory, and a service left by an interrupted update.
	outside := filepath.Join(filepath.Dir(r.DataDir), "models-elsewhere")
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "big.gguf"), []byte("model"), 0o644)
	store, err := config.Open(r.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(c *config.Config) error {
		c.Engines[config.PrismML] = &config.Engine{Port: 8801, ModelsDirectory: outside, Launch: config.Launch{Settings: map[string]string{}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.services.Install(platform.ServiceSpec{Name: config.UpdateService, Executable: r.binary()})
	r.All = true
	r.answer("y")
	if err := r.Uninstall(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if fileExists(r.DataDir) || fileExists(outside) || fileExists(r.ProgramDir) {
		t.Fatalf("something left:\n%s", r.out)
	}
	for _, name := range []string{config.PanelService, config.ServiceName(config.Ollama), config.UpdateService} {
		if s, _ := r.services.Query(name); s.State != platform.Missing {
			t.Fatalf("%s left", name)
		}
	}
	if !strings.Contains(r.out.String(), outside) || !strings.Contains(r.out.String(), "Данные и модели удалены") {
		t.Fatalf("the model directories are not named before the question:\n%s", r.out)
	}
}

func TestUninstallAllDeclinedChangesNothing(t *testing.T) {
	r := installed(t)
	r.All = true
	r.answer("")
	if err := r.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fileExists(r.configPath()) || !fileExists(r.binary()) {
		t.Fatal("a declined full uninstall removed something")
	}
}

func TestUninstallAllWithYesAsksNothing(t *testing.T) {
	r := installed(t)
	r.All, r.Yes = true, true
	r.In = strings.NewReader("")
	if err := r.Uninstall(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	if fileExists(r.DataDir) || fileExists(r.ProgramDir) {
		t.Fatal("--all --yes left data or the program")
	}
}
