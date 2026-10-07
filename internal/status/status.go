// Package status builds the panel's view of every engine: service, API, models, where the model
// lives and when it unloads. It is computed only when the panel asks; a stopped engine is never
// contacted, and status never starts anything.
package status

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	omnibox "github.com/aafnx/omnibox-ai"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/platform"
	"github.com/aafnx/omnibox-ai/internal/telemetry"
)

// Activity tells whether a generation runs and when the engine last served one.
type Activity interface {
	IsActive(engine string) bool
	LastActivity(engine string) time.Time
}

// Builder computes dashboards.
type Builder struct {
	Config   func() *config.Config
	Services platform.Services
	DataDir  string
	Activity Activity // nil until the gateway runs
	Client   func(name string, e *config.Engine) *engine.Client
}

// Engine is one engine in the dashboard.
type Engine struct {
	Name          string         `json:"name"`
	Label         string         `json:"label"`
	Type          string         `json:"type"`
	Service       string         `json:"service"`
	Startup       string         `json:"startup"`
	API           string         `json:"api"`
	ActiveContext *int           `json:"active_context"`
	Sleeping      *bool          `json:"sleeping"`
	LoadedModel   string         `json:"loaded_model"`
	SelectedModel string         `json:"selected_model"`
	Settings      map[string]any `json:"settings"`
	Models        []engine.Model `json:"models"`
	Pending       bool           `json:"pending"`
	CanRestore    bool           `json:"can_restore"`
	Errors        []string       `json:"errors"`
	Residency     Residency      `json:"residency"`
	Idle          Idle           `json:"idle"`
	Endpoint      string         `json:"endpoint"`
	// Automatic restart (F3): the setting, restarts since the owner's start, the last failure
	// and why restarting stopped.
	AutoRestart bool `json:"auto_restart"`
	// Presets are the built-in and the owner's saved launch settings.
	Presets     []config.Preset `json:"presets"`
	Restarts    int             `json:"restarts"`
	LastFailure string          `json:"last_failure"`
	FailedAt    *time.Time      `json:"failed_at"`
	GaveUp      string          `json:"gave_up"`
}

// Residency is where the loaded model's weights are.
type Residency struct {
	Mode         string `json:"mode"` // vram, ram, hybrid or unknown
	RAMBytes     *int64 `json:"ram_bytes"`
	VRAMBytes    *int64 `json:"vram_bytes"`
	GPULayers    *int   `json:"gpu_layers"`
	TotalLayers  *int   `json:"total_layers"`
	CPUMoELayers *int   `json:"cpu_moe_layers"`
}

// Idle is when the model unloads after inactivity.
type Idle struct {
	ExpiresAt *time.Time `json:"expires_at"`
	Estimated bool       `json:"estimated"`
	Busy      bool       `json:"busy"`
	State     string     `json:"state"` // stopped, loaded, unloaded or unknown
}

// Dashboard is the /api/status document.
type Dashboard struct {
	Version  string          `json:"version"`
	Computer string          `json:"computer"`
	Engine   string          `json:"default_backend"`
	Engines  []Engine        `json:"backends"`
	GPU      []telemetry.GPU `json:"gpu"`
	// Operations are the panel's background operations; the service fills them in.
	Operations any `json:"operations"`
	// LastUpdate is the result of the last update started from the panel.
	LastUpdate any `json:"last_update,omitempty"`
	// Network lists the adapters with their MAC addresses, for Wake-on-LAN.
	Network any `json:"network,omitempty"`
}

// Build computes the dashboard; engines are checked in parallel with short probes.
func (b *Builder) Build(ctx context.Context, includeGPU bool) Dashboard {
	cfg := b.Config()
	host, _ := os.Hostname()
	d := Dashboard{Version: omnibox.Version, Computer: host, Engine: cfg.API.Engine, Engines: []Engine{}, GPU: []telemetry.GPU{}, Operations: []any{}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var gpus []telemetry.GPU
	if includeGPU {
		wg.Add(1)
		go func() { defer wg.Done(); g := telemetry.GPUs(ctx); mu.Lock(); gpus = g; mu.Unlock() }()
	}
	rows := make([]Engine, len(config.EngineNames))
	present := make([]bool, len(config.EngineNames))
	for i, name := range config.EngineNames {
		e, ok := cfg.Engines[name]
		if !ok {
			continue
		}
		present[i] = true
		wg.Add(1)
		go func(i int, name string, e *config.Engine) {
			defer wg.Done()
			rows[i] = b.engine(ctx, name, e)
		}(i, name, e)
	}
	wg.Wait()
	for i, ok := range present {
		if ok {
			d.Engines = append(d.Engines, rows[i])
		}
	}
	if gpus != nil {
		d.GPU = gpus
	}
	return d
}

func (b *Builder) client(name string, e *config.Engine) *engine.Client {
	if b.Client != nil {
		return b.Client(name, e)
	}
	return engine.NewClient(e)
}

func (b *Builder) engine(ctx context.Context, name string, e *config.Engine) Engine {
	row := Engine{Name: name, Label: config.Label(name), Type: name, Service: platform.Missing, Startup: "-", API: "-", LoadedModel: "-",
		Models: []engine.Model{}, Errors: []string{}, Pending: e.Pending, CanRestore: e.CanRestore(),
		Residency: Residency{Mode: "unknown"}, Idle: Idle{State: "stopped"}, Endpoint: e.BaseURL() + "/v1"}
	row.Settings = config.View(name, e.Settings)
	row.AutoRestart = e.AutoRestart
	row.Presets = e.PresetList(name)
	if rt, err := engine.ReadRuntime(b.DataDir, name); err == nil {
		row.Restarts, row.LastFailure, row.FailedAt, row.GaveUp = rt.Restarts, rt.LastFailure, rt.FailedAt, rt.GaveUp
	}
	if info, err := b.Services.Query(config.ServiceName(name)); err != nil {
		row.Errors = append(row.Errors, "Служба: "+err.Error())
	} else {
		row.Service = info.State
		if info.State != platform.Missing {
			row.Startup = info.StartType
		}
	}
	running := row.Service == platform.Running
	if running {
		row.Idle.State = "unknown"
	}
	client := b.client(name, e)
	switch name {
	case config.PrismML:
		row.SelectedModel = row.Settings["alias"].(string)
		if running && e.Pending && e.Previous != nil {
			// A pending change has not started yet: the engine still serves the previous model.
			row.SelectedModel = config.View(name, e.Previous.Settings)["alias"].(string)
		}
		models, err := engine.PrismModels(engine.ModelDirectory(e))
		if err != nil {
			row.Errors = append(row.Errors, "Каталог моделей: "+err.Error())
		} else if models != nil {
			row.Models = models
		}
		if running {
			var props engine.PrismProps
			if err := client.Get(ctx, "/props", &props); err != nil {
				row.API = "Unavailable"
			} else {
				row.API = "Ready"
				row.Sleeping = &props.IsSleeping
				if props.DefaultGenerationSettings.NCtx > 0 {
					n := props.DefaultGenerationSettings.NCtx
					row.ActiveContext = &n
				}
				if props.ModelPath != "" {
					row.LoadedModel = baseName(props.ModelPath)
				}
			}
		}
	case config.Ollama:
		row.SelectedModel = e.Model
		if running {
			var v struct {
				Version string `json:"version"`
			}
			ps, err := client.OllamaPS(ctx)
			if err != nil || client.Get(ctx, "/api/version", &v) != nil {
				row.API = "Unavailable"
			} else {
				row.API = "Ready"
				names := make([]string, 0, len(ps))
				for _, m := range ps {
					names = append(names, m.Name)
				}
				row.LoadedModel = "(none)"
				if len(names) > 0 {
					row.LoadedModel = strings.Join(names, ", ")
				}
				if len(ps) == 1 && ps[0].ContextLength > 0 {
					n := ps[0].ContextLength
					row.ActiveContext = &n
				}
				b.ollamaResidency(&row, ps)
			}
			if tags, err := client.OllamaTags(ctx); err == nil {
				row.Models = tags
			} else {
				row.Errors = append(row.Errors, "Список моделей Ollama недоступен.")
			}
		}
	}
	if running {
		if row.Sleeping != nil && *row.Sleeping {
			row.Idle.State = "unloaded"
		}
		if b.Activity != nil {
			row.Idle.Busy = b.Activity.IsActive(name)
		}
		if name == config.PrismML && (row.Sleeping == nil || !*row.Sleeping) {
			b.prismResidency(&row, e)
		}
	}
	return row
}

func (b *Builder) ollamaResidency(row *Engine, ps []engine.OllamaLoaded) {
	if len(ps) != 1 {
		row.Idle.State = "unloaded"
		return
	}
	m := ps[0]
	row.Idle.State = "loaded"
	if t, err := time.Parse(time.RFC3339Nano, m.ExpiresAt); err == nil {
		row.Idle.ExpiresAt = &t
	}
	vram, ram := m.SizeVRAM, max(0, m.Size-m.SizeVRAM)
	row.Residency.VRAMBytes, row.Residency.RAMBytes = &vram, &ram
	switch {
	case vram == 0:
		row.Residency.Mode = "ram"
	case ram == 0:
		row.Residency.Mode = "vram"
	default:
		row.Residency.Mode = "hybrid"
	}
}

func (b *Builder) prismResidency(row *Engine, e *config.Engine) {
	rt, err := engine.ReadRuntime(b.DataDir, config.PrismML)
	if err != nil || !rt.Running {
		return
	}
	settings := e.RunningLaunch().Settings
	if settings["gpu_layers"] == "0" {
		row.Residency.Mode = "ram"
	}
	if rt.GPULayers != nil && rt.TotalLayers != nil {
		row.Residency.GPULayers, row.Residency.TotalLayers = rt.GPULayers, rt.TotalLayers
		switch {
		case *rt.GPULayers == 0:
			row.Residency.Mode = "ram"
		case *rt.GPULayers >= *rt.TotalLayers:
			row.Residency.Mode = "vram"
		default:
			row.Residency.Mode = "hybrid"
		}
	}
	// Offload logs count expert layers as GPU layers even when --n-cpu-moe keeps their weights in RAM.
	if moe := settings["n_cpu_moe"]; moe != "" && row.Residency.Mode != "ram" {
		n := atoi(moe)
		row.Residency.CPUMoELayers = &n
		row.Residency.Mode = "hybrid"
	}
	anchor := rt.Started
	if b.Activity != nil {
		if last := b.Activity.LastActivity(config.PrismML); last.After(anchor) {
			anchor = last
		}
	}
	if idle := atoi(settings["idle_minutes"]); idle > 0 {
		t := anchor.Add(time.Duration(idle) * time.Minute)
		row.Idle.ExpiresAt, row.Idle.Estimated, row.Idle.State = &t, true, "loaded"
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// baseName returns the file name for either path separator.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `\/`); i >= 0 {
		return p[i+1:]
	}
	return filepath.Base(p)
}
