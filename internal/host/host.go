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

// Run starts the engine from the current config.json and supervises it.
func (h *Host) Run(ctx context.Context) error {
	log := h.logger()
	err := h.run(ctx)
	if err != nil {
		log.Line("host", err.Error())
	}
	return err
}

func (h *Host) run(ctx context.Context) error {
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
	h.saveRuntime(func(r *engine.Runtime) { *r = engine.Runtime{Running: true, PID: cmd.Process.Pid, Started: started} })
	var pipes sync.WaitGroup
	pipes.Add(2)
	go func() { defer pipes.Done(); h.copyLines("out", stdout) }()
	go func() { defer pipes.Done(); h.copyLines("err", stderr) }()
	if h.Running != nil {
		h.Running()
	}
	exited := make(chan error, 1)
	go func() { pipes.Wait(); exited <- cmd.Wait() }()
	var result error
	select {
	case err := <-exited:
		code := cmd.ProcessState.ExitCode()
		h.saveRuntime(func(r *engine.Runtime) { r.Running, r.ExitCode = false, &code })
		result = fmt.Errorf("%w с кодом %d: %v", ErrExited, code, err)
		if err == nil {
			result = fmt.Errorf("%w с кодом %d", ErrExited, code)
		}
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
