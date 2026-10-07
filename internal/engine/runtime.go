package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Runtime is what the engine host knows about the process it runs; the panel reads it to show
// where the model lives (VRAM, RAM or both) and when it was started.
type Runtime struct {
	Running     bool      `json:"running"`
	PID         int       `json:"pid,omitempty"`
	Started     time.Time `json:"started"`
	GPULayers   *int      `json:"gpu_layers,omitempty"`
	TotalLayers *int      `json:"total_layers,omitempty"`
	ExitCode    *int      `json:"exit_code,omitempty"`
	// Automatic restarts since the owner started the engine, the last failure and why the host
	// gave up restarting.
	Restarts    int        `json:"restarts,omitempty"`
	LastFailure string     `json:"last_failure,omitempty"`
	FailedAt    *time.Time `json:"failed_at,omitempty"`
	GaveUp      string     `json:"gave_up,omitempty"`
}

// RuntimePath is the engine's runtime file in the data directory.
func RuntimePath(dataDir, engine string) string {
	return filepath.Join(dataDir, "run", engine+".json")
}

// ReadRuntime returns the engine's runtime state; a missing file means it never ran.
func ReadRuntime(dataDir, engine string) (Runtime, error) {
	var r Runtime
	data, err := platform.ReadShared(RuntimePath(dataDir, engine))
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(data, &r)
	return r, err
}

// WriteRuntime stores the engine's runtime state atomically.
func WriteRuntime(dataDir, engine string, r Runtime) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	path := RuntimePath(dataDir, engine)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return platform.WriteAtomic(path, data, 0o644)
}

// llama.cpp logs "offloaded 41/49 layers to GPU" once the model is loaded.
var offloadLine = regexp.MustCompile(`offloaded (\d+)/(\d+) layers to GPU`)

// ParseOffload extracts GPU and total layers from a llama-server log line.
func ParseOffload(line string) (gpu, total int, ok bool) {
	m := offloadLine.FindStringSubmatch(line)
	if m == nil {
		return 0, 0, false
	}
	gpu, _ = strconv.Atoi(m[1])
	total, _ = strconv.Atoi(m[2])
	return gpu, total, true
}
