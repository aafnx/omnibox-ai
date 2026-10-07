package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Request is a panel operation. Fields are structured data; there is no command text.
type Request struct {
	Action   string            `json:"action"`
	Engine   string            `json:"backend,omitempty"`
	Model    string            `json:"model,omitempty"`
	Settings map[string]string `json:"-"`
	URL      string            `json:"url,omitempty"`
	Filename string            `json:"filename,omitempty"`
	SHA256   string            `json:"sha256,omitempty"`
	Version  string            `json:"version,omitempty"`
	Size     string            `json:"size,omitempty"`
	EngineID string            `json:"engine,omitempty"`
	Enabled  string            `json:"enabled,omitempty"`
	// Token is the GitHub token of a self-update: kept in memory only, never listed or logged.
	Token string `json:"-"`
}

// Actions the panel may request.
var Actions = []string{"start", "stop", "restart", "stopall", "save", "apply", "restore", "load", "unload", "pull", "download", "engine-install", "engine-update", "engine-remove", "model-delete", "auto-restart", "self-update", "preset-save", "preset-delete", "preset-apply", "model-test", "sleep"}

// ParseRequest validates a panel operation body; unknown fields are refused.
func ParseRequest(body map[string]any) (Request, error) {
	var r Request
	allowed := []string{"action", "backend", "settings", "model", "url", "filename", "sha256", "engine", "version", "size", "enabled", "token"}
	for key, value := range body {
		if !slices.Contains(allowed, key) {
			return r, fmt.Errorf("Неизвестное поле: %s", key)
		}
		if key == "settings" {
			m, ok := value.(map[string]any)
			if !ok {
				return r, errors.New("Нужен объект settings.")
			}
			r.Settings = map[string]string{}
			for k, v := range m {
				s, err := scalar(v)
				if err != nil {
					return r, fmt.Errorf("Настройка %s: %v", k, err)
				}
				r.Settings[k] = s
			}
			continue
		}
		s, err := scalar(value)
		if err != nil {
			return r, fmt.Errorf("Поле %s: %v", key, err)
		}
		switch key {
		case "action":
			r.Action = s
		case "backend":
			r.Engine = s
		case "model":
			r.Model = s
		case "url":
			r.URL = s
		case "filename":
			r.Filename = s
		case "sha256":
			r.SHA256 = s
		case "engine":
			r.EngineID = s
		case "version":
			r.Version = s
		case "size":
			r.Size = s
		case "enabled":
			r.Enabled = s
		case "token":
			r.Token = strings.TrimSpace(s)
		}
	}
	if !slices.Contains(Actions, r.Action) {
		return r, errors.New("Неизвестная операция панели.")
	}
	if r.Token != "" && r.Action != "self-update" {
		return r, errors.New("token нужен только для обновления программы.")
	}
	if strings.HasPrefix(r.Action, "engine-") {
		if !slices.Contains(config.EngineNames, r.EngineID) {
			return r, errors.New("Нужен engine: prismml или ollama.")
		}
		// The operation list shows which engine the operation changes.
		r.Engine = r.EngineID
	}
	return r, nil
}

func scalar(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case json.Number:
		return t.String(), nil
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(t), nil
	case nil:
		return "", nil
	}
	return "", errors.New("нужно строковое или числовое значение")
}

// Do runs a panel operation and returns the message shown to the owner.
func (c *Controller) Do(ctx context.Context, r Request) (string, error) {
	if r.Action == "sleep" {
		if c.Sleep == nil {
			return "", errors.New("Сон недоступен в этом режиме.")
		}
		// A moment later, so the panel gets the answer before the network goes down.
		go func() {
			time.Sleep(SleepDelay)
			if err := c.Sleep(); err != nil {
				c.say("Не удалось перевести ПК в сон: %v", err)
			}
		}()
		return "ПК засыпает. Разбудить его можно кнопкой в Home Assistant (Wake-on-LAN) или кнопкой питания.", nil
	}
	// The model test goes through the /v1 queue like a client request.
	if r.Action == "model-test" {
		if c.Test == nil {
			return "", errors.New("Тест модели недоступен в этом режиме.")
		}
		return c.Test(ctx)
	}
	// The program update does not touch the engines' settings.
	if r.Action == "self-update" {
		if c.SelfUpdate == nil {
			return "", errors.New("Обновление из панели недоступно в этом режиме.")
		}
		return c.SelfUpdate(ctx, r.Token, r.Version)
	}
	cfg := c.Store.Get()
	name := r.Engine
	if name == "" {
		name = cfg.API.Engine
	}
	switch r.Action {
	case "stopall":
		return "Все движки остановлены.", c.Locked(c.StopAll)
	case "engine-install":
		return c.InstallEngine(ctx, r.EngineID, r.Version)
	case "engine-update":
		return c.SetVersion(ctx, r.EngineID, r.Version)
	case "engine-remove":
		return c.RemoveEngine(r.EngineID)
	}
	if _, ok := cfg.Engines[name]; !ok {
		return "", fmt.Errorf("Неизвестный движок: %s", name)
	}
	label := config.Label(name)
	switch r.Action {
	case "start", "restart":
		return label + ": готов.", c.Locked(func() error { return c.Apply(name, r.Action, nil) })
	case "stop":
		return label + ": остановлен.", c.Locked(func() error { return c.Stop(name) })
	case "restore":
		return label + ": предыдущие настройки восстановлены.", c.Locked(func() error { return c.Restore(name) })
	case "save", "apply":
		if r.Settings == nil {
			return "", errors.New("Нужен объект settings.")
		}
		var message string
		err := c.Locked(func() error {
			// Read the engine again under the lock: an earlier operation may have changed it.
			e, err := c.engine(name)
			if err != nil {
				return err
			}
			prepared, err := c.prepare(name, e, r.Settings)
			if err != nil {
				return err
			}
			if r.Action == "save" {
				message = "Настройки сохранены для следующего запуска; работающая модель не перезапущена."
				return c.Save(name, prepared)
			}
			message = "Настройки применены. Движок готов."
			return c.Apply(name, "restart", &prepared)
		})
		return message, err
	case "load", "unload":
		if name == config.PrismML {
			return c.prismLoad(name, r)
		}
		return c.ollamaLoad(ctx, name, r)
	case "pull":
		return c.pull(ctx, name, r.Model)
	case "download":
		return c.downloadGGUF(ctx, name, r)
	case "model-delete":
		return c.DeleteModel(ctx, name, r.Model)
	case "preset-save", "preset-delete":
		err := c.Store.Update(func(cfg *config.Config) error {
			e, ok := cfg.Engines[name]
			if !ok {
				return fmt.Errorf("Неизвестный движок: %s", name)
			}
			if r.Action == "preset-save" {
				return e.SavePreset(name, r.Model)
			}
			return e.DeletePreset(name, r.Model)
		})
		if r.Action == "preset-save" {
			return fmt.Sprintf("%s: текущие настройки сохранены как пресет «%s».", label, strings.TrimSpace(r.Model)), err
		}
		return fmt.Sprintf("%s: пресет «%s» удалён.", label, r.Model), err
	case "preset-apply":
		// A preset is a settings form filled in advance: applied to a running engine with the
		// usual rollback, saved for the next start of a stopped one.
		var message string
		err := c.Locked(func() error {
			e, err := c.engine(name)
			if err != nil {
				return err
			}
			p, ok := e.FindPreset(name, r.Model)
			if !ok {
				return fmt.Errorf("Пресета «%s» нет.", r.Model)
			}
			prepared, err := c.prepare(name, e, p.Settings)
			if err != nil {
				return err
			}
			if s, err := c.Services.Query(config.ServiceName(name)); err != nil || s.State != platform.Running {
				message = fmt.Sprintf("%s: пресет «%s» сохранён для следующего запуска.", label, p.Name)
				return c.Save(name, prepared)
			}
			message = fmt.Sprintf("%s: пресет «%s» применён. Движок готов.", label, p.Name)
			return c.Apply(name, "restart", &prepared)
		})
		return message, err
	case "auto-restart":
		on, err := strconv.ParseBool(r.Enabled)
		if err != nil {
			return "", errors.New("Нужно enabled: true или false.")
		}
		err = c.Store.Update(func(cfg *config.Config) error {
			e, ok := cfg.Engines[name]
			if !ok {
				return fmt.Errorf("Неизвестный движок: %s", name)
			}
			e.AutoRestart = on
			return nil
		})
		if on {
			return label + ": после сбоя перезапускается сам, до 2 раз за 10 минут. Остановка из панели по-прежнему окончательная.", err
		}
		return label + ": после сбоя остаётся остановленным.", err
	}
	return "", fmt.Errorf("Операция %s ещё не перенесена в Go-версию.", r.Action)
}

// prepare validates the settings form against the current launch.
func (c *Controller) prepare(name string, e *config.Engine, changes map[string]string) (config.Launch, error) {
	var known []string
	if name == config.PrismML {
		models, err := engine.PrismModels(engine.ModelDirectory(e))
		if err != nil {
			return config.Launch{}, err
		}
		for _, m := range models {
			known = append(known, m.ID)
		}
	}
	settings, err := config.Apply(name, e.Settings, changes, known)
	if err != nil {
		return config.Launch{}, err
	}
	next := e.Launch.Clone()
	next.Settings = settings
	return next, nil
}

func (c *Controller) prismLoad(name string, r Request) (string, error) {
	if r.Action == "unload" {
		return "Модель выгружена, движок остановлен. Новый запрос сам его не запустит.", c.Locked(func() error { return c.Stop(name) })
	}
	return "Модель загружена. API готов.", c.Locked(func() error {
		e, err := c.engine(name)
		if err != nil {
			return err
		}
		var prepared *config.Launch
		if r.Settings != nil {
			p, err := c.prepare(name, e, r.Settings)
			if err != nil {
				return err
			}
			prepared = &p
		}
		verb := "start"
		if info, err := c.state(name); err == nil && info.State != platform.Stopped {
			verb = "restart"
		}
		return c.Apply(name, verb, prepared)
	})
}

func (c *Controller) running(name string) bool {
	info, err := c.state(name)
	return err == nil && info.State == platform.Running
}

func (c *Controller) ollamaLoad(ctx context.Context, name string, r Request) (string, error) {
	var message string
	err := c.Locked(func() error {
		e, err := c.engine(name)
		if err != nil {
			return err
		}
		client := c.client(name, e)
		if r.Action == "unload" {
			if !c.running(name) {
				message = "Ollama уже остановлена; загруженных моделей нет."
				return nil
			}
			ps, err := client.OllamaPS(ctx)
			if err != nil {
				return err
			}
			for _, m := range ps {
				if err := c.ollamaGenerate(ctx, client, map[string]any{"model": m.Name, "prompt": "", "stream": false, "keep_alive": 0}); err != nil {
					return err
				}
			}
			message = "Все модели Ollama выгружены; сервер продолжает слушать API."
			return nil
		}
		if !config.ValidOllamaModel(r.Model) {
			return errors.New("Некорректное имя модели Ollama.")
		}
		var prepared *config.Launch
		if r.Settings != nil {
			p, err := c.prepare(name, e, r.Settings)
			if err != nil {
				return err
			}
			prepared = &p
		}
		if prepared != nil || e.Pending {
			verb := "start"
			if info, err := c.state(name); err == nil && info.State != platform.Stopped {
				verb = "restart"
			}
			if err := c.Apply(name, verb, prepared); err != nil {
				return err
			}
		} else if err := c.Start(name); err != nil {
			return err
		}
		e, _ = c.engine(name)
		client = c.client(name, e)
		tags, err := client.OllamaTags(ctx)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(tags, func(m engine.Model) bool { return m.ID == r.Model }) {
			return errors.New("Модель не установлена. Сначала скачайте её.")
		}
		// One model at a time: unload the others first.
		ps, err := client.OllamaPS(ctx)
		if err != nil {
			return err
		}
		for _, m := range ps {
			if m.Name != r.Model {
				if err := c.ollamaGenerate(ctx, client, map[string]any{"model": m.Name, "prompt": "", "stream": false, "keep_alive": 0}); err != nil {
					return err
				}
			}
		}
		keep := e.Settings["keep_alive"]
		if keep == "" {
			keep = "5m"
		}
		body := map[string]any{"model": r.Model, "prompt": "", "stream": false, "keep_alive": keep}
		options := map[string]any{}
		if n, err := strconv.Atoi(e.Settings["context"]); err == nil {
			options["num_ctx"] = n
		}
		if e.Settings["placement"] == "cpu" {
			options["num_gpu"] = 0
		}
		if len(options) > 0 {
			body["options"] = options
		}
		if err := c.ollamaGenerate(ctx, client, body); err != nil {
			return err
		}
		message = "Модель загружена в память."
		return c.Store.Update(func(cfg *config.Config) error { cfg.Engines[name].Model = r.Model; return nil })
	})
	return message, err
}

func (c *Controller) ollamaGenerate(ctx context.Context, client *engine.Client, body map[string]any) error {
	timeout := c.StartupTimeout
	if timeout == 0 {
		timeout = StartupTimeout
	}
	return client.Post(ctx, "/api/generate", body, nil, timeout)
}
