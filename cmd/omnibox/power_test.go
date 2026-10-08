package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

func TestEngineStartsOncePerBoot(t *testing.T) {
	data := t.TempDir()
	cfg := config.Default()
	cfg.Engines[config.PrismML] = &config.Engine{}
	boot := time.Date(2026, 10, 8, 7, 0, 0, 0, time.UTC)
	var started []string
	start := func(e string) { started = append(started, e) }
	bootStart(data, cfg, boot, start)
	if len(started) != 0 {
		t.Fatal("started without start_engine")
	}
	cfg.Panel.StartEngine = config.PrismML
	// The same boot: an update or a crash restarts the service, and a stopped engine stays stopped.
	bootStart(data, cfg, boot.Add(30*time.Second), start)
	if len(started) != 0 {
		t.Fatalf("started again in the same boot: %v", started)
	}
	bootStart(data, cfg, boot.Add(8*time.Hour), start)
	bootStart(data, cfg, boot.Add(8*time.Hour), start)
	if len(started) != 1 || started[0] != config.PrismML {
		t.Fatalf("after a reboot: %v", started)
	}
}

// fakePanel answers the local channel like a running service.
type fakePanel struct {
	services *platform.FakeServices
	calls    []string
}

func (f *fakePanel) Do(method, path string, body any, out any) error {
	if s, _ := f.services.Query(config.PanelService); s.State != platform.Running {
		return errors.New("no service")
	}
	b, _ := json.Marshal(body)
	f.calls = append(f.calls, method+" "+path+" "+string(b))
	switch {
	case path == "/actions":
		if m, _ := body.(map[string]any); m["action"] == "start" {
			f.services.Set(config.ServiceName(m["backend"].(string)), platform.Running)
		}
		return json.Unmarshal([]byte(`{"id":"op"}`), out)
	case strings.HasPrefix(path, "/operations/"):
		return json.Unmarshal([]byte(`{"state":"done","message":"готов"}`), out)
	}
	return json.Unmarshal([]byte(`{}`), out)
}

func newPower(t *testing.T, startEngine string) (power, *platform.FakeServices, *fakePanel, *bytes.Buffer) {
	services := platform.NewFakeServices()
	services.Install(platform.ServiceSpec{Name: config.PanelService, Automatic: true})
	services.Install(platform.ServiceSpec{Name: config.ServiceName(config.PrismML)})
	panel := &fakePanel{services: services}
	out := &bytes.Buffer{}
	cfg := config.Default()
	cfg.Panel.StartEngine = startEngine
	return power{Services: services, Service: panel, Out: out, Wait: time.Millisecond, Timeout: time.Second,
		Config: func() (*config.Config, error) { return cfg, nil }}, services, panel, out
}

func TestOnStartsThePanelAndTheChosenEngineAndOffStopsAll(t *testing.T) {
	p, services, _, out := newPower(t, config.PrismML)
	if err := p.On(nil); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	for _, name := range []string{config.PanelService, config.ServiceName(config.PrismML)} {
		if s, _ := services.Query(name); s.State != platform.Running {
			t.Fatalf("%s not running", name)
		}
	}
	if err := p.Off(nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{config.PanelService, config.ServiceName(config.PrismML)} {
		if s, _ := services.Query(name); s.State == platform.Running {
			t.Fatalf("%s still running", name)
		}
	}
	// Without a chosen engine only the panel starts: the model is picked in the panel.
	p, services, _, out = newPower(t, "")
	p.On(nil)
	if s, _ := services.Query(config.ServiceName(config.PrismML)); s.State == platform.Running || !strings.Contains(out.String(), "Модель не запускалась") {
		t.Fatalf("an engine started without being chosen: %s", out)
	}
}

func TestAutostartTurnsTheServiceStartOnAndOff(t *testing.T) {
	p, services, panel, out := newPower(t, "")
	if err := p.Autostart([]string{"off"}); err != nil {
		t.Fatal(err)
	}
	if s, _ := services.Query(config.PanelService); s.StartType != "Manual" || !strings.Contains(out.String(), "выключен") {
		t.Fatalf("%+v %s", s, out)
	}
	p.Autostart([]string{"on"})
	if s, _ := services.Query(config.PanelService); s.StartType != "Automatic" {
		t.Fatalf("%+v", s)
	}
	if err := p.Autostart([]string{"model", "ollama"}); err == nil {
		t.Fatal("changed the model at start while OmniBox is off")
	}
	p.On(nil)
	if err := p.Autostart([]string{"model", "ollama"}); err != nil || !strings.Contains(panel.calls[len(panel.calls)-1], `"action":"start-engine","engine":"ollama"`) {
		t.Fatalf("%v %v", err, panel.calls)
	}
	if err := p.Autostart([]string{"maybe"}); err == nil {
		t.Fatal("a wrong argument accepted")
	}
}
