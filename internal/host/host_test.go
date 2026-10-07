package host

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/status"
)

// The test binary doubles as a fake engine, so the tests run the same on Windows and Linux.
func TestMain(m *testing.M) {
	switch os.Getenv("OMNIBOX_FAKE_ENGINE") {
	case "":
		os.Exit(m.Run())
	case "serve":
		fmt.Println("hello from engine")
		fmt.Fprintln(os.Stderr, "load_tensors: offloaded 41/49 layers to GPU\r")
		fmt.Println(strings.Repeat("x", maxLineBytes*2))
		fmt.Println("after long line")
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "OMNIBOX_FAKE_ENGINE=child")
		if err := child.Start(); err != nil {
			os.Exit(5)
		}
		time.Sleep(time.Minute)
	case "child":
		// A grandchild like Ollama's runner: it must not outlive the engine service.
		for range 3000 {
			os.WriteFile(os.Getenv("OMNIBOX_HEARTBEAT"), []byte(time.Now().String()), 0o644)
			time.Sleep(20 * time.Millisecond)
		}
	case "fail":
		fmt.Fprintln(os.Stderr, "error: model not found")
		os.Exit(3)
	}
	os.Exit(0)
}

func setup(t *testing.T, mode string) (string, string) {
	t.Helper()
	data := t.TempDir()
	heartbeat := filepath.Join(data, "heartbeat")
	cfg := config.Default()
	cfg.Panel.Hosts = []string{"localhost"}
	b64 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg.Panel.Password = config.Password{Salt: b64, Hash: b64, Iterations: 210000}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Engines[config.PrismML] = &config.Engine{Port: 8801, Launch: config.Launch{Executable: exe,
		Settings:    map[string]string{"model": filepath.Join(data, "m-Q4_K_M.gguf")},
		Environment: map[string]string{"OMNIBOX_FAKE_ENGINE": mode, "OMNIBOX_HEARTBEAT": heartbeat}}}
	if err := cfg.Save(filepath.Join(data, "config.json")); err != nil {
		t.Fatal(err)
	}
	return data, heartbeat
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHostRunsEngineLogsOutputAndStopsWholeTree(t *testing.T) {
	data, heartbeat := setup(t, "serve")
	ctx, cancel := context.WithCancel(context.Background())
	running := make(chan struct{})
	h := &Host{DataDir: data, Engine: config.PrismML, Running: func() { close(running) }}
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	<-running
	waitFor(t, "the grandchild", func() bool { _, err := os.Stat(heartbeat); return err == nil })
	waitFor(t, "offload layers", func() bool {
		rt, err := engine.ReadRuntime(data, config.PrismML)
		return err == nil && rt.GPULayers != nil && *rt.GPULayers == 41 && *rt.TotalLayers == 49 && rt.Running && rt.PID > 0
	})
	logs := status.LogDir(data, config.PrismML)
	waitFor(t, "output after a long line", func() bool {
		b, _ := os.ReadFile(filepath.Join(logs, "out.log"))
		return bytes.Contains(b, []byte("after long line"))
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("a requested stop is not a failure: %v", err)
	}
	if rt, _ := engine.ReadRuntime(data, config.PrismML); rt.Running {
		t.Fatal("runtime still says running")
	}
	before, _ := os.ReadFile(heartbeat)
	time.Sleep(300 * time.Millisecond)
	if after, _ := os.ReadFile(heartbeat); !bytes.Equal(before, after) {
		t.Fatal("a grandchild survived the engine service")
	}
	out, _ := os.ReadFile(filepath.Join(logs, "out.log"))
	stamped := regexp.MustCompile(`(?m)^\d{4}-\d{2}-\d{2}T\S+ hello from engine$`)
	if !stamped.Match(out) || bytes.Contains(out, []byte(strings.Repeat("x", maxLineBytes+1))) || !bytes.Contains(out, []byte("x …")) {
		t.Fatalf("out.log: %.200q", out)
	}
	errLog, _ := os.ReadFile(filepath.Join(logs, "err.log"))
	if bytes.Contains(errLog, []byte("\r")) || !bytes.Contains(errLog, []byte("offloaded 41/49")) {
		t.Fatalf("err.log: %q", errLog)
	}
	cfg, _ := config.Load(filepath.Join(data, "config.json"))
	merged := status.Logs(data, cfg, config.PrismML)[0]["text"]
	if !strings.Contains(merged, "[prismml/out] hello from engine") || !strings.Contains(merged, "[prismml/host] Процесс остановлен.") {
		t.Fatalf("panel log view: %s", merged)
	}
}

func TestEngineThatExitsIsAFailureWithExitCode(t *testing.T) {
	data, _ := setup(t, "fail")
	h := &Host{DataDir: data, Engine: config.PrismML}
	err := h.Run(context.Background())
	if !errors.Is(err, ErrExited) || !strings.Contains(err.Error(), "кодом 3") {
		t.Fatalf("%v", err)
	}
	rt, _ := engine.ReadRuntime(data, config.PrismML)
	if rt.Running || rt.ExitCode == nil || *rt.ExitCode != 3 {
		t.Fatalf("%+v", rt)
	}
	b, _ := os.ReadFile(filepath.Join(status.LogDir(data, config.PrismML), "host.log"))
	if !bytes.Contains(b, []byte("кодом 3")) {
		t.Fatalf("host.log: %s", b)
	}
}

func TestMissingEngineOrExecutableIsReported(t *testing.T) {
	data, _ := setup(t, "serve")
	if err := (&Host{DataDir: data, Engine: config.Ollama}).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "не установлен") {
		t.Fatalf("%v", err)
	}
}

func TestLogRotationKeepsTwoOlderFiles(t *testing.T) {
	old := maxLogBytes
	maxLogBytes = 100
	defer func() { maxLogBytes = old }()
	dir := t.TempDir()
	l := &Logger{Dir: dir}
	for i := range 40 {
		l.Line("out", fmt.Sprintf("line %02d %s", i, strings.Repeat("y", 20)))
	}
	files, _ := filepath.Glob(filepath.Join(dir, "out.log*"))
	if len(files) != 3 {
		t.Fatalf("%v", files)
	}
	current, _ := os.ReadFile(filepath.Join(dir, "out.log"))
	if !bytes.Contains(current, []byte("line 39")) {
		t.Fatalf("%s", current)
	}
}
