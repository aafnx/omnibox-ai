package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Reserver closes admission of new generations and reports whether one still runs; the /v1
// gateway implements it. A version switch reserves first and then waits for the generation.
type Reserver interface {
	Reserve() bool
	Generating() bool
	EndControl()
}

// GenerationWait bounds how long a version switch waits for a running generation.
var GenerationWait = 120 * time.Second

// EngineInfo is one engine in the panel's engine manager.
type EngineInfo struct {
	Name      string   `json:"name"`
	Label     string   `json:"label"`
	Installed bool     `json:"installed"`
	State     string   `json:"state"`
	Version   string   `json:"version"`
	Managed   bool     `json:"managed"`
	Versions  []string `json:"versions"`
	Pending   bool     `json:"pending"`
}

// Engines lists both engines with their installed versions.
func (c *Controller) Engines() []EngineInfo {
	cfg := c.Store.Get()
	out := []EngineInfo{}
	for _, name := range config.EngineNames {
		info := EngineInfo{Name: name, Label: config.Label(name), State: "NotInstalled", Versions: []string{}}
		if e, ok := cfg.Engines[name]; ok {
			info.Installed, info.Pending = true, e.Pending
			if s, err := c.state(name); err == nil {
				info.State = s.State
			}
			info.Version = c.Manager.TagOf(name, e.RunningLaunch().Executable)
			info.Managed = info.Version != ""
		}
		for _, v := range c.Manager.Installed(name) {
			info.Versions = append(info.Versions, v.Tag)
		}
		out = append(out, info)
	}
	return out
}

// defaultLaunch is the launch of a newly installed engine: it starts once a model is chosen.
// defaultLaunch starts a new engine with the base preset's values.
func defaultLaunch(name, exe string) config.Launch {
	settings := map[string]string{"context": "32768"}
	if name == config.Ollama {
		settings["keep_alive"] = "30m"
	} else {
		for _, key := range []string{"flash_attention", "cache_key", "cache_value", "batch", "ubatch"} {
			settings[key] = config.BaseSettings(name)[key]
		}
	}
	return config.Launch{Executable: exe, Settings: settings}
}

// InstallEngine adds an engine: runtime, config entry and a service that starts manually.
func (c *Controller) InstallEngine(ctx context.Context, name, tag string) (string, error) {
	if !slices.Contains(config.EngineNames, name) {
		return "", fmt.Errorf("Неизвестный движок: %s", name)
	}
	label := config.Label(name)
	if _, ok := c.Store.Get().Engines[name]; ok {
		return "", fmt.Errorf("%s уже установлен. Для другой версии используйте обновление.", label)
	}
	// The download may take minutes and runs outside the lock; settings are read again under it.
	v, err := c.Manager.Install(ctx, name, tag)
	if err != nil {
		return "", err
	}
	var message string
	err = c.Locked(func() error {
		if _, ok := c.Store.Get().Engines[name]; ok {
			return fmt.Errorf("%s уже установлен.", label)
		}
		if s, err := c.state(name); err != nil || s.State != platform.Missing {
			return fmt.Errorf("Служба %s уже существует; установка отменена.", config.ServiceName(name))
		}
		models := filepath.Join(c.DataDir, "models", name)
		if err := os.MkdirAll(models, 0o755); err != nil {
			return err
		}
		if err := c.Store.Update(func(cfg *config.Config) error {
			cfg.Engines[name] = &config.Engine{Port: config.DefaultEnginePort(name), ModelsDirectory: models, Launch: defaultLaunch(name, v.Executable)}
			return nil
		}); err != nil {
			return err
		}
		spec := platform.ServiceSpec{Name: config.ServiceName(name), DisplayName: "OmniBox AI · " + label,
			Description: "Движок " + label + " для OmniBox AI. Запускается из панели.",
			Executable:  c.Executable, Args: []string{"engine", name, "--data", c.DataDir}}
		if err := c.Services.Install(spec); err != nil {
			cause := err
			if err := c.Store.Update(func(cfg *config.Config) error { delete(cfg.Engines, name); return nil }); err != nil {
				return fmt.Errorf("Установка %s не выполнена: %v. Запись в настройках не удалена: %v", label, cause, err)
			}
			return fmt.Errorf("Установка %s не выполнена: %v. Настройки не изменены.", label, cause)
		}
		message = fmt.Sprintf("%s %s установлен. Движок не запущен: выберите модель и запустите его в панели.", label, v.Tag)
		return nil
	})
	return message, err
}

// SetVersion installs a release and points the engine at it. A running engine restarts on it
// and returns to the previous version on failure; a stopped one checks it at the next start.
func (c *Controller) SetVersion(ctx context.Context, name, tag string) (string, error) {
	if _, err := c.engine(name); err != nil {
		return "", err
	}
	label := config.Label(name)
	v, err := c.Manager.Install(ctx, name, tag)
	if err != nil {
		return "", err
	}
	// Reserved after the download: new generations are refused, the current one may finish.
	reserved, err := c.reserve()
	if err != nil {
		return "", err
	}
	if reserved {
		defer c.Gate.EndControl()
	}
	var message string
	err = c.Locked(func() error {
		e, err := c.engine(name)
		if err != nil {
			return err
		}
		if e.Executable == v.Executable {
			message = fmt.Sprintf("%s уже использует версию %s.", label, v.Tag)
			return nil
		}
		next := e.Launch.Clone()
		next.Executable = v.Executable
		if s, err := c.state(name); err == nil && (s.State == platform.Running || s.State == platform.StartPending) {
			message = fmt.Sprintf("%s: версия %s запущена.", label, v.Tag)
			return c.Apply(name, "restart", &next)
		}
		message = fmt.Sprintf("%s: установлена версия %s. Движок остановлен; при ошибке следующего запуска вернётся прежняя версия.", label, v.Tag)
		return c.Save(name, next)
	})
	if err != nil {
		return "", err
	}
	if e, err := c.engine(name); err == nil {
		c.Manager.RemoveUnused(name, e)
	}
	return message, nil
}

// reserve closes admission and waits for the running generation to end.
func (c *Controller) reserve() (bool, error) {
	if c.Gate == nil {
		return false, nil
	}
	deadline := time.Now().Add(GenerationWait)
	for !c.Gate.Reserve() {
		if time.Now().After(deadline) {
			return false, errors.New("Выполняется другая операция управления. Новая версия скачана; повторите обновление после её завершения.")
		}
		time.Sleep(200 * time.Millisecond)
	}
	for c.Gate.Generating() {
		if time.Now().After(deadline) {
			c.Gate.EndControl()
			return false, errors.New("Модель занята генерацией. Новая версия скачана; повторите обновление после завершения запроса.")
		}
		time.Sleep(200 * time.Millisecond)
	}
	return true, nil
}

// RemoveEngine stops and removes the engine's service and settings. Models stay on disk, so a
// reinstall finds them again.
func (c *Controller) RemoveEngine(name string) (string, error) {
	label := config.Label(name)
	var e *config.Engine
	err := c.Locked(func() error {
		cfg := c.Store.Get()
		var ok bool
		if e, ok = cfg.Engines[name]; !ok {
			return fmt.Errorf("%s не установлен.", label)
		}
		if len(cfg.Engines) < 2 {
			return errors.New("Нельзя удалить единственный движок: панели нужен хотя бы один.")
		}
		if err := c.Stop(name); err != nil {
			return err
		}
		// The service goes first: an engine without a service can be installed again, a service
		// without settings would start nothing.
		if err := c.Services.Remove(config.ServiceName(name), StopTimeout); err != nil {
			return fmt.Errorf("Удаление %s не выполнено: %v. Настройки не изменены, движок остановлен.", label, err)
		}
		return c.Store.Update(func(cfg *config.Config) error {
			delete(cfg.Engines, name)
			if cfg.API.Engine == name {
				for _, other := range config.EngineNames {
					if _, ok := cfg.Engines[other]; ok {
						cfg.API.Engine = other
					}
				}
			}
			return nil
		})
	})
	if err != nil {
		return "", err
	}
	removed := c.Manager.RemoveUnused(name, nil)
	text := label + " удалён: служба и запись в настройках."
	if len(removed) > 0 {
		text += " Удалены версии: " + strings.Join(removed, ", ") + "."
	}
	if c.Manager.TagOf(name, e.Executable) == "" {
		text += " Файлы движка вне OmniBox оставлены: " + filepath.Dir(e.Executable) + "."
	}
	return text + " Модели остались на диске.", nil
}

// DeleteModel removes a GGUF (all parts of a split model) or an Ollama model. A model the
// current or rollback settings use is refused.
func (c *Controller) DeleteModel(ctx context.Context, name, model string) (string, error) {
	if name == config.Ollama {
		return c.deleteOllamaModel(ctx, model)
	}
	var message string
	err := c.Locked(func() error {
		e, err := c.engine(name)
		if err != nil {
			return err
		}
		models, err := engine.PrismModels(engine.ModelDirectory(e))
		if err != nil {
			return err
		}
		i := slices.IndexFunc(models, func(m engine.Model) bool { return m.ID == model })
		if i < 0 {
			return errors.New("Модель не найдена в каталоге моделей.")
		}
		parts := engine.GGUFParts(models[i].ID)
		inUse := []string{e.Settings["model"]}
		if e.Previous != nil {
			inUse = append(inUse, e.Previous.Settings["model"])
		}
		for _, used := range inUse {
			if used == "" {
				continue
			}
			for _, p := range engine.GGUFParts(used) {
				if slices.ContainsFunc(parts, func(x string) bool { return samePath(x, p) }) {
					return errors.New("Эта модель выбрана в настройках или нужна для отката. Выберите другую модель и сохраните настройки.")
				}
			}
		}
		for _, p := range parts {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
		message = fmt.Sprintf("Модель %s удалена, освобождено %.1f ГБ.", models[i].Name, float64(models[i].Bytes)/(1<<30))
		if len(parts) > 1 {
			message = fmt.Sprintf("Модель %s удалена (частей: %d), освобождено %.1f ГБ.", models[i].Name, len(parts), float64(models[i].Bytes)/(1<<30))
		}
		return nil
	})
	return message, err
}

func samePath(a, b string) bool {
	x, err1 := filepath.Abs(a)
	y, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && strings.EqualFold(x, y)
}

func (c *Controller) deleteOllamaModel(ctx context.Context, model string) (string, error) {
	if !config.ValidOllamaModel(model) {
		return "", errors.New("Некорректное имя модели Ollama.")
	}
	if !c.running(config.Ollama) {
		return "", errors.New("Запустите Ollama: модели удаляются через её API.")
	}
	e, err := c.engine(config.Ollama)
	if err != nil {
		return "", err
	}
	if err := c.client(config.Ollama, e).Delete(ctx, "/api/delete", map[string]any{"model": model}); err != nil {
		return "", err
	}
	err = c.Locked(func() error {
		return c.Store.Update(func(cfg *config.Config) error {
			if x := cfg.Engines[config.Ollama]; x != nil && x.Model == model {
				x.Model = ""
			}
			return nil
		})
	})
	return "Модель " + model + " удалена из Ollama.", err
}
