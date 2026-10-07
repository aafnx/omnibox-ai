package install

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/enginemgr"
	"github.com/aafnx/omnibox-ai/internal/panel"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

type rig struct {
	*Installer
	out      *bytes.Buffer
	services *platform.FakeServices
	rules    map[string]int
	health   []string
	path     []string
}

func newRig(t *testing.T, input string) *rig {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "download", "omnibox")
	os.MkdirAll(filepath.Dir(source), 0o755)
	os.WriteFile(source, []byte("binary v1"), 0o755)
	// GitHub is unreachable in tests: engines fail to download, which must not break the install.
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", 503) }))
	t.Cleanup(gh.Close)
	r := &rig{out: &bytes.Buffer{}, services: platform.NewFakeServices(), rules: map[string]int{}}
	data := filepath.Join(root, "data")
	r.Installer = &Installer{ProgramDir: filepath.Join(root, "program"), DataDir: data, Version: "0.18.0", Source: source,
		Services: r.services, Manager: &enginemgr.Manager{DataDir: data, API: gh.URL}, In: strings.NewReader(input), Out: r.out,
		AllowPort:  func(rule string, port int) error { r.rules[rule] = port; return nil },
		RemoveRule: func(rule string) error { delete(r.rules, rule); return nil },
		Health:     func(_ context.Context, port int, host string) error { r.health = append(r.health, host); return nil },
		Addresses:  func() []string { return []string{"192.168.1.77", "omnibox", "localhost"} },
		Restrict:   func(string) error { return nil }, ShimDir: filepath.Join(root, "shim"),
		AddToPath: func(dir string) (func() error, error) {
			r.path = append(r.path, dir)
			return func() error { r.path = r.path[:len(r.path)-1]; return nil }, nil
		}}
	return r
}

var shown = regexp.MustCompile(`Пароль панели: (\S+)`)

func TestEnterThroughInstallUsesDefaults(t *testing.T) {
	r := newRig(t, strings.Repeat("\n", 20))
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	cfg, err := config.Load(r.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Panel.Port != 8800 || !slices.Equal(cfg.Panel.Hosts, []string{"192.168.1.77", "omnibox", "localhost"}) || cfg.API.PublicURL != "http://192.168.1.77:8800/v1" || !cfg.API.RequireKey || cfg.Panel.PublicOrigin != "" {
		t.Fatalf("%+v", cfg)
	}
	m := shown.FindStringSubmatch(r.out.String())
	if m == nil || !panel.VerifyPassword(m[1], cfg.Panel.Password) {
		t.Fatalf("password not shown or wrong:\n%s", r.out)
	}
	saved, _ := os.ReadFile(r.configPath())
	if strings.Contains(string(saved), m[1]) || !regexp.MustCompile(`API key: obai-[0-9a-f]{64}`).MatchString(r.out.String()) {
		t.Fatal("secret stored or key not shown")
	}
	spec, ok := r.services.Spec(config.PanelService)
	if !ok || !spec.Automatic || spec.Executable != r.binary() || !slices.Equal(spec.Args, []string{"serve", "--data", r.DataDir}) {
		t.Fatalf("panel service %+v", spec)
	}
	if b, _ := os.ReadFile(r.binary()); string(b) != "binary v1" {
		t.Fatal("binary not installed")
	}
	if r.rules[FirewallRule] != 8800 || len(r.health) != 1 || r.health[0] != "192.168.1.77" {
		t.Fatalf("firewall %v health %v", r.rules, r.health)
	}
	if !strings.Contains(r.out.String(), "не установлен") {
		t.Fatalf("engine failure not reported:\n%s", r.out)
	}
}

func TestAnswersAreUsed(t *testing.T) {
	answers := strings.Join([]string{"9000", "pc.lan, 10.0.0.5", "", "https://llm.fnx.red", "192.168.1.10", "n", "/srv/models", ""}, "\n") + "\n"
	r := newRig(t, answers)
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("%v\n%s", err, r.out)
	}
	cfg, _ := config.Load(r.configPath())
	if cfg.Panel.Port != 9000 || !slices.Equal(cfg.Panel.Hosts, []string{"pc.lan", "10.0.0.5"}) || cfg.API.PublicURL != "http://10.0.0.5:9000/v1" ||
		cfg.Panel.PublicOrigin != "https://llm.fnx.red" || !slices.Equal(cfg.Panel.TrustedProxies, []string{"192.168.1.10"}) {
		t.Fatalf("%+v", cfg.Panel)
	}
	if strings.Contains(r.out.String(), "скачивание последней версии") {
		t.Fatal("engines installed although declined")
	}
}

func TestFailureAtEveryStepUndoesEverything(t *testing.T) {
	for _, stepName := range []string{"files", "data", "config", "services", "firewall", "command", "path", "panel", "panel-started"} {
		r := newRig(t, "")
		r.Yes = true
		r.FailAt = stepName
		err := r.Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "отменены") {
			t.Fatalf("%s: %v", stepName, err)
		}
		if _, err := os.Stat(r.configPath()); err == nil {
			t.Fatalf("%s: config left", stepName)
		}
		if s, _ := r.services.Query(config.PanelService); s.State != platform.Missing {
			t.Fatalf("%s: panel service left", stepName)
		}
		if len(r.rules) != 0 {
			t.Fatalf("%s: firewall rule left", stepName)
		}
		if _, err := os.Stat(filepath.Dir(r.binary())); err == nil {
			t.Fatalf("%s: binary left", stepName)
		}
		if shim, _ := r.shim(); fileExists(shim) || len(r.path) != 0 {
			t.Fatalf("%s: ai command or PATH left", stepName)
		}
	}
}

func TestUpdateSwitchesServicesAndRollsBack(t *testing.T) {
	r := newRig(t, "")
	r.Yes = true
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	old := r.binary()
	// An engine installed from the panel runs the same program.
	r.services.Install(engineSpec(config.Ollama, old, r.DataDir))
	store, _ := config.Open(r.configPath())
	store.Update(func(c *config.Config) error {
		c.Engines[config.Ollama] = &config.Engine{Port: 8802, Launch: config.Launch{Executable: "/x/ollama", Settings: map[string]string{}}}
		return nil
	})
	before, _ := os.ReadFile(r.configPath())

	os.WriteFile(r.Source, []byte("binary v2"), 0o755)
	r.Version = "0.18.1"
	r.Health = func(context.Context, int, string) error { return errors.New("panel down") }
	if err := r.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "отменены") {
		t.Fatalf("failed update: %v", err)
	}
	for _, name := range []string{config.PanelService, config.ServiceName(config.Ollama)} {
		if exe, _ := r.services.Executable(name); exe != old {
			t.Fatalf("%s points at %s after rollback", name, exe)
		}
	}
	if _, err := os.Stat(r.binary()); err == nil {
		t.Fatal("new binary left after rollback")
	}

	r.Health = func(context.Context, int, string) error { return nil }
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{config.PanelService, config.ServiceName(config.Ollama)} {
		if exe, _ := r.services.Executable(name); exe != r.binary() {
			t.Fatalf("%s not switched", name)
		}
	}
	after, _ := os.ReadFile(r.configPath())
	if !bytes.Equal(before, after) {
		t.Fatal("an update changed the settings")
	}
	r.Version = "0.18.2"
	os.WriteFile(r.Source, []byte("binary v3"), 0o755)
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(r.ProgramDir, "bin"))
	if len(entries) != 2 {
		t.Fatalf("versions kept: %v", entries)
	}
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestAiCommandFollowsTheInstalledVersion(t *testing.T) {
	r := newRig(t, "")
	r.Yes = true
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	shim, _ := r.shim()
	first, _ := os.ReadFile(shim)
	if !strings.Contains(string(first), r.binary()) || !strings.Contains(string(first), " ai ") || len(r.path) != 1 || r.path[0] != r.ShimDir {
		t.Fatalf("%q %v", first, r.path)
	}
	r.Version = "0.18.1"
	r.Health = func(context.Context, int, string) error { return errors.New("down") }
	r.Run(context.Background())
	if after, _ := os.ReadFile(shim); !bytes.Equal(after, first) {
		t.Fatal("a failed update left the ai command on the new version")
	}
	r.Health = func(context.Context, int, string) error { return nil }
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(shim); !strings.Contains(string(after), filepath.Join("bin", "0.18.1")) {
		t.Fatalf("%q", after)
	}
}
