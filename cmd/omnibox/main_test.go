package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	omnibox "github.com/aafnx/omnibox-ai"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/platform"
	"github.com/aafnx/omnibox-ai/internal/telemetry"
)

func TestVersionMatchesVersionFile(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), omnibox.Version) || omnibox.Version == "" {
		t.Fatalf("output %q, version %q", out.String(), omnibox.Version)
	}
}

func TestServeReportsMissingConfigAndUnknownCommands(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"serve", "--data", t.TempDir()}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "Нет настроек") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if code := run([]string{"unknown"}, &out, &errOut); code != 2 {
		t.Fatalf("unknown command exit %d", code)
	}
}

func TestTelemetryPollsOnlyWhileAnEngineWorks(t *testing.T) {
	cfg := config.Default()
	cfg.Engines[config.PrismML] = &config.Engine{Port: 8801, Launch: config.Launch{Settings: map[string]string{}}}
	services := platform.NewFakeServices()
	services.Set(config.ServiceName(config.PrismML), platform.Stopped)
	taken := 0
	sampler := &telemetry.Sampler{Take: func(context.Context) telemetry.Sample { taken++; return telemetry.Sample{} }}
	if v := telemetryView(context.Background(), cfg, services, sampler, false); v["enabled"] != false || taken != 0 {
		t.Fatalf("stopped engines must not be polled: %v, %d samples", v, taken)
	}
	if v := telemetryView(context.Background(), cfg, services, sampler, true); v["enabled"] != true || taken != 1 {
		t.Fatalf("a single sample on request: %v", v)
	}
	services.Set(config.ServiceName(config.PrismML), platform.Running)
	if v := telemetryView(context.Background(), cfg, services, &telemetry.Sampler{Take: func(context.Context) telemetry.Sample { taken++; return telemetry.Sample{} }}, false); v["enabled"] != true || taken != 2 {
		t.Fatalf("a working engine is polled: %v", v)
	}
}
