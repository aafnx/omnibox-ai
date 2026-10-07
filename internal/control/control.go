// Package control starts, stops and reconfigures engines. One operation runs at a time; a
// change that fails to start rolls back to the last working configuration, and an engine the
// owner stopped stays stopped whatever else happens.
package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/enginemgr"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Timeouts for engine services.
const (
	StartupTimeout = 180 * time.Second
	StopTimeout    = 30 * time.Second
)

// Controller runs engine operations.
type Controller struct {
	Store    *config.Store
	Services platform.Services
	// Client returns an API client for an engine (tests point it at fake engines).
	Client func(name string, e *config.Engine) *engine.Client
	// Wait is the pause between readiness probes.
	Wait time.Duration
	// StartupTimeout bounds waiting for the API after a start.
	StartupTimeout time.Duration
	// Report receives progress lines for the panel's operation log.
	Report func(string)
	// Manager installs engine versions; DataDir and Executable are what new engine services run.
	Manager    *enginemgr.Manager
	DataDir    string
	Executable string
	// Gate lets a version switch wait for the running generation.
	Gate Reserver
	// HTTP downloads models (tests point it at a fake Hugging Face).
	HTTP *http.Client
	// SelfUpdate downloads a release of the program and hands it to a temporary installer
	// service; the panel restarts during the update.
	SelfUpdate func(ctx context.Context, token, version string) (string, error)

	mu sync.Mutex
}

func (c *Controller) say(format string, args ...any) {
	if c.Report != nil {
		c.Report(fmt.Sprintf(format, args...))
	}
}

func (c *Controller) client(name string, e *config.Engine) *engine.Client {
	if c.Client != nil {
		return c.Client(name, e)
	}
	return engine.NewClient(e)
}

func (c *Controller) engine(name string) (*config.Engine, error) {
	e, ok := c.Store.Get().Engines[name]
	if !ok {
		return nil, fmt.Errorf("%s не установлен.", config.Label(name))
	}
	return e, nil
}

func (c *Controller) state(name string) (platform.ServiceInfo, error) {
	return c.Services.Query(config.ServiceName(name))
}

// Locked runs fn as the only control operation.
func (c *Controller) Locked(fn func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fn()
}

// Stop stops one engine; stopping a stopped engine is not an error.
func (c *Controller) Stop(name string) error {
	label := config.Label(name)
	info, err := c.state(name)
	if err != nil {
		return err
	}
	switch info.State {
	case platform.Missing:
		c.say("%s: служба не установлена.", label)
		return nil
	case platform.Stopped:
		c.say("%s: уже остановлен.", label)
		return nil
	}
	if err := c.Services.Stop(config.ServiceName(name), StopTimeout); err != nil {
		return fmt.Errorf("%s: не удалось остановить: %v", label, err)
	}
	c.say("%s: остановлен.", label)
	return nil
}

// StopAll stops every engine and reports every failure, never hiding one behind another.
func (c *Controller) StopAll() error {
	var errs []error
	for _, name := range config.EngineNames {
		if _, ok := c.Store.Get().Engines[name]; ok {
			if err := c.Stop(name); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Start starts an engine after stopping the others: one model at a time.
func (c *Controller) Start(name string) error {
	e, err := c.engine(name)
	if err != nil {
		return err
	}
	label := config.Label(name)
	info, err := c.state(name)
	if err != nil {
		return err
	}
	if info.State == platform.Missing {
		return fmt.Errorf("%s: служба не установлена.", label)
	}
	if info.StartType == "Disabled" {
		return fmt.Errorf("%s: служба отключена. Другие движки не остановлены.", label)
	}
	if name == config.PrismML && e.Settings["model"] == "" {
		return fmt.Errorf("%s: модель не выбрана. Выберите GGUF в разделе «Модели и настройки».", label)
	}
	if _, err := engine.Build(name, e, e.Launch); err != nil {
		return err
	}
	for _, other := range config.EngineNames {
		if other != name {
			if _, ok := c.Store.Get().Engines[other]; ok {
				if err := c.Stop(other); err != nil {
					return err
				}
			}
		}
	}
	if info.State != platform.Running && info.State != platform.StartPending {
		if err := c.Services.Start(config.ServiceName(name)); err != nil {
			return fmt.Errorf("%s: не удалось запустить службу: %v", label, err)
		}
	}
	c.say("%s: запуск…", label)
	if err := c.waitReady(name, e); err != nil {
		return err
	}
	if e.Pending {
		if err := c.Store.Update(func(cfg *config.Config) error { cfg.Engines[name].Confirm(); return nil }); err != nil {
			return err
		}
	}
	c.say("%s: готов.", label)
	return nil
}

func (c *Controller) waitReady(name string, e *config.Engine) error {
	label := config.Label(name)
	timeout := c.StartupTimeout
	if timeout == 0 {
		timeout = StartupTimeout
	}
	wait := c.Wait
	if wait == 0 {
		wait = 500 * time.Millisecond
	}
	client := c.client(name, e)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := c.state(name)
		if err == nil && (info.State == platform.Stopped || info.State == platform.Missing) {
			return fmt.Errorf("%s: служба завершилась при запуске. Подробности — в журнале движка.", label)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		ready := engine.Ready(ctx, name, client)
		cancel()
		if ready {
			return nil
		}
		time.Sleep(wait)
	}
	return fmt.Errorf("%s: API не ответил за %d с. Служба оставлена как есть; проверьте журнал движка.", label, int(timeout.Seconds()))
}

// Apply starts or restarts an engine, optionally with a prepared launch. A pending change that
// fails to start is rolled back; the engine runs again only if it ran before.
func (c *Controller) Apply(name, verb string, prepared *config.Launch) error {
	e, err := c.engine(name)
	if err != nil {
		return err
	}
	label := config.Label(name)
	info, err := c.state(name)
	if err != nil {
		return err
	}
	if info.State == platform.Missing {
		return fmt.Errorf("%s: служба не установлена.", label)
	}
	if info.StartType == "Disabled" {
		return fmt.Errorf("%s: служба отключена; настройки не изменены.", label)
	}
	wasRunning := info.State == platform.Running
	if verb == "start" && info.State != platform.Stopped && (prepared != nil || e.Pending) {
		return fmt.Errorf("%s: новые настройки ещё не применены. Перезапустите движок.", label)
	}
	err = func() error {
		if verb == "restart" {
			if err := c.Stop(name); err != nil {
				return err
			}
		}
		if prepared != nil {
			if err := c.Store.Update(func(cfg *config.Config) error { cfg.Engines[name].Stage(*prepared); return nil }); err != nil {
				return err
			}
		}
		return c.Start(name)
	}()
	if err == nil {
		return nil
	}
	cause := err
	e, _ = c.engine(name)
	if e == nil || !e.Pending || e.Previous == nil {
		return cause
	}
	// Stop first: never rewrite the configuration under a process that failed to stop.
	if err := c.Stop(name); err != nil {
		return fmt.Errorf("Запуск не выполнен: %v. Откат: %v", cause, err)
	}
	if err := c.Store.Update(func(cfg *config.Config) error { return cfg.Engines[name].Restore() }); err != nil {
		return fmt.Errorf("Запуск не выполнен: %v. Откат: %v", cause, err)
	}
	if wasRunning {
		if err := c.Start(name); err != nil {
			return fmt.Errorf("Запуск не выполнен: %v. Откат: %v", cause, err)
		}
		return fmt.Errorf("Запуск с новыми настройками не выполнен: %v. Прежние настройки восстановлены; движок снова запущен.", cause)
	}
	return fmt.Errorf("Запуск с новыми настройками не выполнен: %v. Прежние настройки восстановлены; движок оставлен остановленным.", cause)
}

// Save stores settings for the next start without touching a running engine.
func (c *Controller) Save(name string, prepared config.Launch) error {
	if _, err := c.engine(name); err != nil {
		return err
	}
	return c.Store.Update(func(cfg *config.Config) error { cfg.Engines[name].Stage(prepared); return nil })
}

// Restore returns to the previous configuration; a running engine restarts on it.
func (c *Controller) Restore(name string) error {
	e, err := c.engine(name)
	if err != nil {
		return err
	}
	label := config.Label(name)
	if !e.CanRestore() {
		return errors.New("Нет предыдущих настроек для восстановления.")
	}
	if e.Restored() {
		c.say("%s: предыдущие настройки уже восстановлены.", label)
		return nil
	}
	info, err := c.state(name)
	if err != nil {
		return err
	}
	wasRunning := info.State == platform.Running
	if err := c.Stop(name); err != nil {
		return err
	}
	if err := c.Store.Update(func(cfg *config.Config) error { return cfg.Engines[name].Restore() }); err != nil {
		return err
	}
	if wasRunning {
		if err := c.Start(name); err != nil {
			return err
		}
	}
	c.say("%s: предыдущие настройки восстановлены.", label)
	return nil
}
