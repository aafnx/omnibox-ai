package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/aafnx/omnibox-ai/internal/cli"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/control"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// bootStart starts the owner's chosen engine once per boot of the computer. A restart of the
// service in the same boot (an update, a crash) starts nothing: a stopped engine stays stopped.
func bootStart(data string, cfg *config.Config, boot time.Time, start func(engine string)) {
	path := filepath.Join(data, "run", "boot.json")
	var last struct {
		Boot time.Time `json:"boot"`
	}
	if b, err := platform.ReadShared(path); err == nil {
		_ = json.Unmarshal(b, &last)
	}
	sameBoot := !boot.IsZero() && !last.Boot.IsZero() && last.Boot.Sub(boot).Abs() < 2*time.Minute
	if !boot.IsZero() && !sameBoot {
		last.Boot = boot.UTC()
		if b, err := json.Marshal(last); err == nil {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			_ = platform.WriteAtomic(path, b, 0o644)
		}
	}
	if sameBoot {
		return
	}
	// A chosen model names its engine; the older setting starts an engine with its last model.
	if name, _, ok := config.SplitKey(cfg.Panel.StartModel); ok {
		if _, ok := cfg.Engines[name]; ok {
			start(name)
		}
		return
	}
	if cfg.Panel.StartEngine == "" {
		return
	}
	if _, ok := cfg.Engines[cfg.Panel.StartEngine]; ok {
		start(cfg.Panel.StartEngine)
	}
}

// power is what "ai on", "ai off" and "ai autostart" need; tests replace each part.
type power struct {
	Services platform.Services
	Service  cli.Service // the local channel to the running service
	Config   func() (*config.Config, error)
	Out      io.Writer
	Wait     time.Duration // pause between checks while the service starts
	Timeout  time.Duration
}

func (p power) running(name string) bool {
	s, err := p.Services.Query(name)
	return err == nil && s.State == platform.Running
}

// On starts OmniBox: the panel service and, if chosen, an engine (the argument, or the one set
// to start with the system).
func (p power) On(args []string) error {
	if len(args) > 1 {
		return errors.New("Используйте: ai on [prismml|ollama]")
	}
	if !p.running(config.PanelService) {
		if err := p.Services.Start(config.PanelService); err != nil {
			return fmt.Errorf("Служба OmniBox не запустилась: %v", err)
		}
	}
	// The panel answers on its local channel once it is ready.
	for deadline := time.Now().Add(p.Timeout); ; time.Sleep(p.Wait) {
		if p.Service.Do("GET", "/status", nil, &json.RawMessage{}) == nil {
			break
		}
		if time.Now().After(deadline) {
			return errors.New("Служба OmniBox запущена, но не отвечает. Журнал: logs/panel.log в каталоге данных.")
		}
	}
	fmt.Fprintln(p.Out, "OmniBox запущен: панель и API работают.")
	engine := ""
	if len(args) == 1 {
		engine = args[0]
	} else if cfg, err := p.Config(); err == nil {
		engine = cfg.Panel.StartEngine
	}
	if engine == "" || p.running(config.ServiceName(engine)) {
		if engine == "" {
			fmt.Fprintln(p.Out, "Модель не запускалась: выберите её в панели или ai start <движок>.")
		}
		return nil
	}
	if cli.Run([]string{"start", engine}, p.Service, p.Out, p.Out) != 0 {
		return errors.New("Движок не запустился; панель работает.")
	}
	return nil
}

// Off stops everything: the engines, then the panel and the API.
func (p power) Off(args []string) error {
	if len(args) != 0 {
		return errors.New("Используйте: ai off")
	}
	for _, name := range config.EngineNames {
		if p.running(config.ServiceName(name)) {
			if err := p.Services.Stop(config.ServiceName(name), control.StopTimeout); err != nil {
				return fmt.Errorf("%s не остановился: %v", config.Label(name), err)
			}
		}
	}
	if p.running(config.PanelService) {
		if err := p.Services.Stop(config.PanelService, control.StopTimeout); err != nil {
			return fmt.Errorf("Служба OmniBox не остановилась: %v", err)
		}
	}
	fmt.Fprintln(p.Out, "OmniBox остановлен: движки, панель и API выключены. Включить — ai on.")
	return nil
}

// Autostart turns starting with the system on or off, or shows it; "model" picks the engine
// started with the system.
func (p power) Autostart(args []string) error {
	if len(args) == 2 && args[0] == "model" {
		engine := args[1]
		if engine == "off" {
			engine = ""
		}
		if !p.running(config.PanelService) {
			return errors.New("Сначала включите OmniBox: ai on")
		}
		var started struct{ ID string }
		if err := p.Service.Do("POST", "/actions", map[string]any{"action": "start-engine", "engine": engine}, &started); err != nil {
			return err
		}
		fmt.Fprintln(p.Out, map[bool]string{true: "Модель при старте не запускается.", false: config.Label(engine) + " будет запускаться при старте системы."}[engine == ""])
		return nil
	}
	if len(args) > 1 {
		return errors.New("Используйте: ai autostart [on|off|model <prismml|ollama|off>]")
	}
	if len(args) == 1 && (args[0] == "on" || args[0] == "off") {
		if err := p.Services.SetAutomatic(config.PanelService, args[0] == "on"); err != nil {
			return err
		}
	} else if len(args) == 1 {
		return errors.New("Используйте: ai autostart [on|off|model <prismml|ollama|off>]")
	}
	s, err := p.Services.Query(config.PanelService)
	if err != nil {
		return err
	}
	line := "Автозапуск OmniBox вместе с системой: " + map[bool]string{true: "включён", false: "выключен"}[s.StartType == "Automatic"]
	if cfg, err := p.Config(); err == nil && cfg.Panel.StartEngine != "" {
		line += "; модель при старте: " + config.Label(cfg.Panel.StartEngine)
	}
	fmt.Fprintln(p.Out, line+".")
	return nil
}
