package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	omnibox "github.com/aafnx/omnibox-ai"
	"github.com/aafnx/omnibox-ai/internal/cli"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/gateway"
	"github.com/aafnx/omnibox-ai/internal/local"
	"github.com/aafnx/omnibox-ai/internal/panel"
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

// The whole service: panel, /v1 and the ai command's socket, wired as the real service is.
func TestServeEndToEnd(t *testing.T) {
	data, err := os.MkdirTemp("", "ob") // short: unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(data)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cfg := config.Default()
	cfg.Panel.Port, cfg.Panel.Hosts = port, []string{"localhost"}
	cfg.Panel.Password, _ = panel.NewPassword("пароль-панели-123")
	key, hash := gateway.NewKey()
	cfg.API = config.API{Enabled: true, RequireKey: true, KeyHash: hash, Engine: config.PrismML, QueueDepth: 4}
	if err := cfg.Save(filepath.Join(data, "config.json")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	up := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serveContext(ctx, []string{"--data", data}, io.Discard, platform.NewFakeServices(), func() { close(up) })
	}()
	select {
	case <-up:
	case err := <-done:
		t.Fatalf("service failed before announcing readiness: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("service did not announce readiness")
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	get := func(path string, header map[string]string) (int, string) {
		req, _ := http.NewRequest("GET", base+path, nil)
		req.Host = "localhost"
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	login := func(password string) int {
		req, _ := http.NewRequest("POST", base+"/api/login", strings.NewReader(`{"password":"`+password+`"}`))
		req.Host = "localhost"
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code, body := get("/", nil); code != 200 || !strings.Contains(body, "<html") {
		t.Fatalf("panel: %d", code)
	}
	if code, body := get("/v1/models", map[string]string{"Authorization": "Bearer " + key}); code != 503 || !strings.Contains(body, "No model is running") {
		t.Fatalf("/v1: %d %s", code, body)
	}
	if code, _ := get("/v1/models", nil); code != 401 {
		t.Fatalf("/v1 without key: %d", code)
	}
	if login("пароль-панели-123") != 200 {
		t.Fatal("login failed")
	}
	var out, errOut bytes.Buffer
	if code := cli.Run([]string{"status"}, local.Dial(data), &out, &errOut); code != 0 || !strings.Contains(out.String(), "OmniBox AI "+omnibox.Version) {
		t.Fatalf("ai status over the socket: %s %s", out.String(), errOut.String())
	}
	out.Reset()
	if code := cli.Run([]string{"password", "reset"}, local.Dial(data), &out, &errOut); code != 0 {
		t.Fatalf("password reset: %s", errOut.String())
	}
	m := regexp.MustCompile(`пароль панели: (\S+)`).FindStringSubmatch(out.String())
	if m == nil || login(m[1]) != 200 || login("пароль-панели-123") != 401 {
		t.Fatalf("reset password: %s", out.String())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
