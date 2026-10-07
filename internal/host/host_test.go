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
	case "crash", "oom":
		// Counts its starts, leaves a runner behind and fails, as a crashing engine would.
		f, _ := os.OpenFile(os.Getenv("OMNIBOX_STARTS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		f.WriteString("start\n")
		f.Close()
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "OMNIBOX_FAKE_ENGINE=child")
		child.Start()
		time.Sleep(200 * time.Millisecond)
		if os.Getenv("OMNIBOX_FAKE_ENGINE") == "oom" {
			fmt.Fprintln(os.Stderr, "CUDA error: out of memory")
		}
		os.Exit(3)
	}
	os.Exit(0)
}

func setup(t *testing.T, mode string) (string, string) {
	t.Helper()
	return setupRestart(t, mode, false)
}

func setupRestart(t *testing.T, mode string, autoRestart bool) (string, string) {
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
	cfg.Engines[config.PrismML] = &config.Engine{Port: 8801, AutoRestart: autoRestart, Launch: config.Launch{Executable: exe,
		Settings: map[string]string{"model": filepath.Join(data, "m-Q4_K_M.gguf")},
		Environment: map[string]string{"OMNIBOX_FAKE_ENGINE": mode, "OMNIBOX_HEARTBEAT": heartbeat,
			"OMNIBOX_STARTS": filepath.Join(data, "starts")}}}
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

func starts(data string) int {
	b, _ := os.ReadFile(filepath.Join(data, "starts"))
	return bytes.Count(b, []byte("start"))
}

func fastRestarts(t *testing.T) {
	old := restartDelay
	restartDelay = 50 * time.Millisecond
	t.Cleanup(func() { restartDelay = old })
}

func TestFailureWithoutAutoRestartStopsAndLeavesNoRunner(t *testing.T) {
	data, heartbeat := setupRestart(t, "crash", false)
	if err := (&Host{DataDir: data, Engine: config.PrismML}).Run(context.Background()); !errors.Is(err, ErrExited) {
		t.Fatalf("%v", err)
	}
	if n := starts(data); n != 1 {
		t.Fatalf("restarted with auto_restart off: %d starts", n)
	}
	before, _ := os.ReadFile(heartbeat)
	time.Sleep(300 * time.Millisecond)
	if after, _ := os.ReadFile(heartbeat); !bytes.Equal(before, after) {
		t.Fatal("the runner of a failed engine survived it")
	}
	if rt, _ := engine.ReadRuntime(data, config.PrismML); !strings.Contains(rt.LastFailure, "кодом 3") || rt.FailedAt == nil {
		t.Fatalf("%+v", rt)
	}
}

func TestAutoRestartIsLimited(t *testing.T) {
	fastRestarts(t)
	data, _ := setupRestart(t, "crash", true)
	if err := (&Host{DataDir: data, Engine: config.PrismML}).Run(context.Background()); !errors.Is(err, ErrExited) {
		t.Fatalf("%v", err)
	}
	if n := starts(data); n != 1+maxRestarts {
		t.Fatalf("%d starts", n)
	}
	rt, _ := engine.ReadRuntime(data, config.PrismML)
	if rt.Restarts != maxRestarts || !strings.Contains(rt.GaveUp, "перезапуска") || rt.Running {
		t.Fatalf("%+v", rt)
	}
}

func TestOutOfMemoryIsNotRestarted(t *testing.T) {
	fastRestarts(t)
	data, _ := setupRestart(t, "oom", true)
	(&Host{DataDir: data, Engine: config.PrismML}).Run(context.Background())
	if n := starts(data); n != 1 {
		t.Fatalf("restarted after out of memory: %d starts", n)
	}
	if rt, _ := engine.ReadRuntime(data, config.PrismML); !strings.Contains(rt.GaveUp, "памяти") {
		t.Fatalf("%+v", rt)
	}
}

func TestStopDuringRestartDelayStaysStopped(t *testing.T) {
	old := restartDelay
	restartDelay = time.Minute
	defer func() { restartDelay = old }()
	data, _ := setupRestart(t, "crash", true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (&Host{DataDir: data, Engine: config.PrismML}).Run(ctx) }()
	waitFor(t, "the first failure", func() bool {
		rt, err := engine.ReadRuntime(data, config.PrismML)
		return err == nil && rt.Restarts == 1
	})
	// The owner stops the service while the host waits to restart the engine.
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("a stop is not a failure: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := starts(data); n != 1 {
		t.Fatalf("started again after the stop: %d starts", n)
	}
}
