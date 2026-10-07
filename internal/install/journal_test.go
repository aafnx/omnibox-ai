package install

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// A power loss during a new installation leaves nothing behind once the installer runs again.
func TestCutOffNewInstallationIsUndoneByTheNextRun(t *testing.T) {
	for _, stepName := range []string{"files", "config", "services", "firewall", "command", "path", "panel", "panel-started"} {
		r := newRig(t, "")
		r.Yes = true
		r.CrashAt = stepName
		if err := r.Run(context.Background()); !errors.Is(err, errCrash) {
			t.Fatalf("%s: %v", stepName, err)
		}
		r.CrashAt = ""
		if err := r.recover(); err != nil {
			t.Fatalf("%s: %v", stepName, err)
		}
		if fileExists(r.configPath()) || fileExists(r.journalPath()) || fileExists(r.ProgramDir) || len(r.rules) != 0 || len(r.path) != 0 {
			t.Fatalf("%s: left config %v, journal %v, program %v, rules %v, path %v", stepName,
				fileExists(r.configPath()), fileExists(r.journalPath()), fileExists(r.ProgramDir), r.rules, r.path)
		}
		if s, _ := r.services.Query(config.PanelService); s.State != platform.Missing {
			t.Fatalf("%s: panel service left", stepName)
		}
		// The next installation then goes through as usual.
		if err := r.Run(context.Background()); err != nil || !fileExists(r.configPath()) || fileExists(r.journalPath()) {
			t.Fatalf("%s: install after recovery: %v", stepName, err)
		}
	}
}

// A power loss during an update brings the previous version back on the next run.
func TestCutOffUpdateReturnsThePreviousVersion(t *testing.T) {
	for _, stepName := range []string{"files", "services", "command", "panel", "panel-started"} {
		r := newRig(t, "")
		r.Yes = true
		if err := r.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		old := r.binary()
		shim, _ := r.shim()
		oldShim, _ := os.ReadFile(shim)
		r.Version, r.CrashAt = "0.19.1", stepName
		os.WriteFile(r.Source, []byte("binary v2"), 0o755)
		if err := r.Run(context.Background()); !errors.Is(err, errCrash) {
			t.Fatalf("%s: %v", stepName, err)
		}
		r.CrashAt = ""
		r.out.Reset()
		if err := r.recover(); err != nil {
			t.Fatalf("%s: %v", stepName, err)
		}
		if exe, _ := r.services.Executable(config.PanelService); exe != old {
			t.Fatalf("%s: panel runs %s", stepName, exe)
		}
		if now, _ := os.ReadFile(shim); string(now) != string(oldShim) {
			t.Fatalf("%s: ai command not restored", stepName)
		}
		if fileExists(r.binary()) || fileExists(r.journalPath()) || !fileExists(old) {
			t.Fatalf("%s: new binary or journal left, or old one gone", stepName)
		}
		if s, _ := r.services.Query(config.PanelService); s.State != platform.Running {
			t.Fatalf("%s: panel not running after recovery", stepName)
		}
		if !strings.Contains(r.out.String(), "Прежняя версия восстановлена") {
			t.Fatalf("%s: %s", stepName, r.out)
		}
	}
}

func TestDamagedJournalStopsTheInstaller(t *testing.T) {
	r := newRig(t, "")
	r.Yes = true
	os.MkdirAll(r.ProgramDir, 0o755)
	os.WriteFile(r.journalPath(), []byte("{"), 0o644)
	if err := r.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "не прочитан") {
		t.Fatalf("%v", err)
	}
}
