package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/install"
	"github.com/aafnx/omnibox-ai/internal/platform"
	"github.com/aafnx/omnibox-ai/internal/selfupdate"
)

func TestPanelUpdateStartsTheTemporaryInstallerService(t *testing.T) {
	srv := releaseServer(t, "v0.20.0")
	services := platform.NewFakeServices()
	data := t.TempDir()
	p := &panelUpdate{Services: services, Data: data, Program: filepath.Join(t.TempDir(), "program"), Current: "0.19.0",
		NewUpdater: func(token string) *selfupdate.Updater {
			return &selfupdate.Updater{Token: token, API: srv.URL, GOOS: "windows", HTTP: srv.Client()}
		}}
	if _, err := p.Start(context.Background(), "", ""); err == nil {
		t.Fatal("started without a token")
	}
	msg, err := p.Start(context.Background(), "token", "")
	if err != nil || !strings.Contains(msg, "0.20.0") {
		t.Fatalf("%q %v", msg, err)
	}
	spec, ok := services.Spec(updateService)
	if !ok || !strings.HasPrefix(spec.Executable, updatesDir(data)) || spec.Args[0] != "update-service" || spec.Automatic {
		t.Fatalf("%+v", spec)
	}
	if b, _ := os.ReadFile(spec.Executable); string(b) != "new" || !slices.Contains(services.Calls, "start:"+updateService) {
		t.Fatalf("binary %q, calls %v", b, services.Calls)
	}
	if _, err := p.Start(context.Background(), "token", ""); err == nil || !strings.Contains(err.Error(), "уже выполняется") {
		t.Fatalf("a second update while one runs: %v", err)
	}
	// After the update the panel removes the stopped temporary service and the download.
	services.Set(updateService, platform.Stopped)
	cleanupUpdate(services, data)
	waitUntil(t, func() bool { s, _ := services.Query(updateService); return s.State == platform.Missing })
	waitUntil(t, func() bool { _, err := os.Stat(updatesDir(data)); return err != nil })
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	for range 500 {
		if ok() {
			return
		}
		sleepBriefly()
	}
	t.Fatal("timed out")
}

func TestPerformUpdateRecordsTheResult(t *testing.T) {
	root := t.TempDir()
	data, program := filepath.Join(root, "data"), filepath.Join(root, "program")
	cfg := config.Default()
	cfg.Panel.Hosts = []string{"127.0.0.1"}
	b64 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg.Panel.Password = config.Password{Salt: b64, Hash: b64, Iterations: 210000}
	os.MkdirAll(data, 0o755)
	if err := cfg.Save(filepath.Join(data, "config.json")); err != nil {
		t.Fatal(err)
	}
	services := platform.NewFakeServices()
	old := filepath.Join(program, "bin", "0.19.0", "omnibox")
	services.Install(platform.ServiceSpec{Name: config.PanelService, Executable: old, Automatic: true})
	source := filepath.Join(root, "omnibox")
	os.WriteFile(source, []byte("v2"), 0o755)
	installer := func(health error) *install.Installer {
		return &install.Installer{ProgramDir: program, DataDir: data, Version: "0.20.0", Source: source, Services: services, Yes: true,
			AllowPort: func(string, int) error { return nil }, RemoveRule: func(string) error { return nil },
			Health:   func(context.Context, int, string) error { return health },
			Restrict: func(string) error { return nil }, ShimDir: filepath.Join(root, "shim"),
			AddToPath: func(string) (func() error, error) { return func() error { return nil }, nil }}
	}
	if err := performUpdate(installer(errors.New("panel down"))); err != nil {
		t.Fatal(err)
	}
	if r := lastUpdate(data); r == nil || r.OK || r.From != "0.19.0" || !strings.Contains(r.Message, "отменены") {
		t.Fatalf("failed update: %+v", r)
	}
	if exe, _ := services.Executable(config.PanelService); exe != old {
		t.Fatalf("a failed update left the panel on %s", exe)
	}
	if err := performUpdate(installer(nil)); err != nil {
		t.Fatal(err)
	}
	if r := lastUpdate(data); r == nil || !r.OK {
		t.Fatalf("update: %+v", r)
	}
	if log, _ := os.ReadFile(filepath.Join(data, "logs", "update.log")); !strings.Contains(string(log), "обновление") {
		t.Fatalf("update.log: %s", log)
	}
}

func sleepBriefly() { time.Sleep(10 * time.Millisecond) }
