package install

import (
	"bufio"
	"context"
	"fmt"
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

// gone fails the test if any path still exists.
func gone(t *testing.T, out fmt.Stringer, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if fileExists(p) {
			t.Fatalf("%s left\n%s", p, out)
		}
	}
}

func TestUninstallLeavesNoTrace(t *testing.T) {
	r := installed(t)
	root := filepath.Dir(r.DataDir)
	// Models chosen outside the data directory: a shared folder with someone else's file in it,
	// and an Ollama store inside it; a service and a download left by interrupted updates.
	shared := filepath.Join(root, "Models")
	ollama := filepath.Join(shared, "ollama")
	os.MkdirAll(filepath.Join(ollama, "blobs"), 0o755)
	os.MkdirAll(filepath.Join(ollama, "manifests"), 0o755)
	os.WriteFile(filepath.Join(shared, "big-Q4_K_M.gguf"), []byte("model"), 0o644)
	os.WriteFile(filepath.Join(shared, ".download-0123456789abcdef0123456789abcdef.part"), []byte("mo"), 0o644)
	os.WriteFile(filepath.Join(shared, "notes.txt"), []byte("mine"), 0o644)
	store, err := config.Open(r.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(c *config.Config) error {
		c.Engines[config.PrismML] = &config.Engine{Port: 8801, ModelsDirectory: shared, Launch: config.Launch{Settings: map[string]string{}}}
		c.Engines[config.Ollama] = &config.Engine{Port: 8802, ModelsDirectory: ollama, Launch: config.Launch{Settings: map[string]string{}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.services.Install(platform.ServiceSpec{Name: config.UpdateService, Executable: r.binary()})
	r.TempDir = filepath.Join(root, "tmp")
	os.MkdirAll(filepath.Join(r.TempDir, "omnibox-update-123", "x"), 0o755)
	os.MkdirAll(filepath.Join(r.TempDir, "other"), 0o755)
	var path []string
	r.RemoveFromPath = func(dir string) error { path = append(path, dir); return nil }

	r.answer("y")
	if err := r.Uninstall(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	for _, name := range []string{config.PanelService, config.ServiceName(config.Ollama), config.UpdateService} {
		if s, _ := r.services.Query(name); s.State != platform.Missing {
			t.Fatalf("%s left", name)
		}
	}
	shim, _ := r.shim()
	gone(t, r.out, r.DataDir, r.ProgramDir, shim, ollama, filepath.Join(r.TempDir, "omnibox-update-123"),
		filepath.Join(shared, "big-Q4_K_M.gguf"), filepath.Join(shared, ".download-0123456789abcdef0123456789abcdef.part"))
	if len(r.rules) != 0 {
		t.Fatal("firewall rule left")
	}
	if !fileExists(filepath.Join(shared, "notes.txt")) || !fileExists(filepath.Join(r.TempDir, "other")) {
		t.Fatal("removed a file OmniBox did not create")
	}
	if out := r.out.String(); !strings.Contains(out, shared) || !strings.Contains(out, filepath.Join(shared, "notes.txt")) {
		t.Fatalf("the shared folder or the file left in it is not named:\n%s", out)
	}
}

func TestUninstallRemovesAnEmptiedModelsFolder(t *testing.T) {
	r := installed(t)
	own := filepath.Join(filepath.Dir(r.DataDir), "Models")
	os.MkdirAll(own, 0o755)
	os.WriteFile(filepath.Join(own, "m.GGUF"), []byte("model"), 0o644)
	store, _ := config.Open(r.configPath())
	if err := store.Update(func(c *config.Config) error {
		c.Engines[config.PrismML] = &config.Engine{Port: 8801, ModelsDirectory: own, Launch: config.Launch{Settings: map[string]string{}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r.Yes = true
	r.In = strings.NewReader("")
	if err := r.Uninstall(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	gone(t, r.out, own, r.DataDir, r.ProgramDir)
}

func TestUninstallAnsweredNoChangesNothing(t *testing.T) {
	r := installed(t)
	r.answer("")
	if err := r.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s, _ := r.services.Query(config.PanelService); s.State == platform.Missing || !fileExists(r.binary()) || !fileExists(r.configPath()) ||
		!fileExists(filepath.Join(r.DataDir, "models", "m-Q4_K_M.gguf")) {
		t.Fatal("a declined uninstall removed something")
	}
}
