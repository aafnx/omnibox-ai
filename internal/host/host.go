// Package host runs one engine as an operating system service: it starts the engine process from
// config.json, keeps its whole process tree together, writes its output to rotated logs with
// timestamps and records runtime state for the panel. Stopping the service stops the tree.
package host

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/status"
)

// Log rotation: each stream keeps the current file and two older ones.
const (
	keptLogs     = 2
	maxLineBytes = 64 << 10
	stopTimeout  = 15 * time.Second
)

// maxLogBytes is the size at which a log stream rotates (a variable for tests).
var maxLogBytes int64 = 4 << 20

// Automatic restart (F3), when the owner turned it on for the engine: a pause before each
// attempt and at most maxRestarts attempts in restartWindow. Variables for tests.
var (
	restartDelay  = 5 * time.Second
	restartWindow = 10 * time.Minute
	maxRestarts   = 2
)

// outOfMemory matches the engines' messages about VRAM or RAM running out: starting again with
// the same settings fails the same way, so the host stops trying.
var outOfMemory = regexp.MustCompile(`(?i)out of memory|cudaMalloc failed|failed to allocate|unable to allocate|ErrorOutOfDeviceMemory|insufficient memory`)

// recentLines is how much engine output the host keeps to judge a failure.
const recentLines = 40

// ErrExited means the engine process ended by itself, which the service reports as a failure.
var ErrExited = errors.New("процесс движка завершился")

// Logger appends timestamped lines to an engine's log streams.
type Logger struct {
	Dir string
	Now func() time.Time
	mu  sync.Mutex
}

// Line writes one line to a stream (out, err or host).
func (l *Logger) Line(stream, text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	if err := os.MkdirAll(l.Dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(l.Dir, stream+".log")
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogBytes {
		for i := keptLogs; i > 1; i-- {
			_ = os.Rename(fmt.Sprintf("%s.%d", path, i-1), fmt.Sprintf("%s.%d", path, i))
		}
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(now().UTC().Format(time.RFC3339Nano) + " " + text + "\n")
}

// Host runs one engine until ctx ends or the engine exits.
type Host struct {
	DataDir string
	Engine  string
	// Running is called once the process has started (the service then reports running).
	Running func()
	Log     *Logger

	mu      sync.Mutex
	runtime engine.Runtime
	recent  []string
}

func (h *Host) logger() *Logger {
	if h.Log == nil {
		h.Log = &Logger{Dir: status.LogDir(h.DataDir, h.Engine)}
	}
	return h.Log
}

func (h *Host) saveRuntime(change func(r *engine.Runtime)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	change(&h.runtime)
	if err := engine.WriteRuntime(h.DataDir, h.Engine, h.runtime); err != nil {
		h.logger().Line("host", "Не удалось записать состояние: "+err.Error())
	}
}

// Run starts the engine from the current config.json and supervises it. An engine that fails by
// itself is started again only when its auto_restart is on, within the limits above; a stop of
// the service ends ctx and with it any restart, including one waiting for its delay.
func (h *Host) Run(ctx context.Context) error {
	log := h.logger()
	var once sync.Once
	running := func() {
		if h.Running != nil {
			once.Do(h.Running)
		}
	}
	// The counters describe this start by the owner.
	h.saveRuntime(func(r *engine.Runtime) { r.Restarts, r.GaveUp = 0, "" })
	var attempts []time.Time
	for {
		err := h.run(ctx, running)
		if err != nil {
			log.Line("host", err.Error())
		}
		if ctx.Err() != nil || !errors.Is(err, ErrExited) || !h.autoRestart() {
			return err
		}
		now := time.Now()
		attempts = slices.DeleteFunc(attempts, func(t time.Time) bool { return now.Sub(t) > restartWindow })
		reason := ""
		switch {
		case h.outOfMemory():
			reason = "движку не хватило памяти; с теми же настройками запуск повторит ошибку"
		case len(attempts) >= maxRestarts:
			reason = fmt.Sprintf("уже %d перезапуска за %d минут", len(attempts), int(restartWindow/time.Minute))
		}
		if reason != "" {
			log.Line("host", "Автоперезапуск прекращён: "+reason+".")
			h.saveRuntime(func(r *engine.Runtime) { r.GaveUp = reason })
			return err
		}
		attempts = append(attempts, now)
		h.saveRuntime(func(r *engine.Runtime) { r.Restarts++ })
		log.Line("host", fmt.Sprintf("Автоперезапуск через %s.", restartDelay))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(restartDelay):
		}
	}
}

func (h *Host) autoRestart() bool {
	cfg, err := config.Load(filepath.Join(h.DataDir, "config.json"))
	if err != nil {
		return false
	}
	e, ok := cfg.Engines[h.Engine]
	return ok && e.AutoRestart
}

func (h *Host) outOfMemory() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.ContainsFunc(h.recent, outOfMemory.MatchString)
}

func (h *Host) run(ctx context.Context, running func()) error {
	log := h.logger()
	cfg, err := config.Load(filepath.Join(h.DataDir, "config.json"))
	if err != nil {
		return err
	}
	e, ok := cfg.Engines[h.Engine]
	if !ok {
		return fmt.Errorf("%s не установлен.", config.Label(h.Engine))
	}
	command, err := engine.Build(h.Engine, e, e.Launch)
	if err != nil {
		return err
	}
	tree, err := newTree()
	if err != nil {
		return fmt.Errorf("Не удалось объединить процессы движка: %v", err)
	}
	defer tree.close()
	cmd := exec.Command(command.Path, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = append(os.Environ(), command.EnvList()...)
	tree.prepare(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	log.Line("host", "Запуск: "+command.Path)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("Не удалось запустить %s: %v", command.Path, err)
	}
	if err := tree.started(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("Не удалось объединить процессы движка: %v", err)
	}
	started := time.Now().UTC()
	h.mu.Lock()
	h.recent = nil
	h.mu.Unlock()
	h.saveRuntime(func(r *engine.Runtime) {
		*r = engine.Runtime{Running: true, PID: cmd.Process.Pid, Started: started,
			Restarts: r.Restarts, LastFailure: r.LastFailure, FailedAt: r.FailedAt}
	})
	var pipes sync.WaitGroup
	pipes.Add(2)
	go func() { defer pipes.Done(); h.copyLines("out", stdout) }()
	go func() { defer pipes.Done(); h.copyLines("err", stderr) }()
	running()
	exited := make(chan error, 1)
	go func() { pipes.Wait(); exited <- cmd.Wait() }()
	var result error
	select {
	case err := <-exited:
		// Children of the engine (Ollama runners) may outlive it and hold VRAM and the port:
		// they go before anything starts again.
		tree.kill(cmd)
		code := cmd.ProcessState.ExitCode()
		result = fmt.Errorf("%w с кодом %d: %v", ErrExited, code, err)
		if err == nil {
			result = fmt.Errorf("%w с кодом %d", ErrExited, code)
		}
		failed := time.Now().UTC()
		h.saveRuntime(func(r *engine.Runtime) {
			r.Running, r.ExitCode, r.LastFailure, r.FailedAt = false, &code, result.Error(), &failed
		})
	case <-ctx.Done():
		tree.stop(cmd)
		select {
		case <-exited:
		case <-time.After(stopTimeout):
			tree.kill(cmd)
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
				log.Line("host", "Процесс движка не завершился после принудительной остановки.")
			}
		}
		h.saveRuntime(func(r *engine.Runtime) { r.Running = false })
		log.Line("host", "Процесс остановлен.")
	}
	return result
}

// copyLines logs a stream line by line; an over-long line is cut, never left blocking the pipe.
func (h *Host) copyLines(stream string, r io.Reader) {
	reader := bufio.NewReaderSize(r, maxLineBytes)
	for {
		line, prefix, err := reader.ReadLine()
		if len(line) > 0 || (err == nil && !prefix) {
			text := strings.TrimRight(string(line), "\r")
			if prefix {
				text += " …"
				for prefix && err == nil {
					_, prefix, err = reader.ReadLine()
				}
			}
			h.mu.Lock()
			if h.recent = append(h.recent, text); len(h.recent) > recentLines {
				h.recent = h.recent[len(h.recent)-recentLines:]
			}
			h.mu.Unlock()
			if gpu, total, ok := engine.ParseOffload(text); ok {
				h.saveRuntime(func(r *engine.Runtime) { r.GPULayers, r.TotalLayers = &gpu, &total })
			}
			h.logger().Line(stream, text)
		}
		if err != nil {
			return
		}
	}
}
