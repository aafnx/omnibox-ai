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
	// A model download can bring its vision projector (mmproj) in the same operation; ForModel
	// names the installed model a projector is downloaded for.
	ProjectorURL      string `json:"-"`
	ProjectorFilename string `json:"-"`
	ProjectorSHA256   string `json:"-"`
	ProjectorSize     string `json:"-"`
	ForModel          string `json:"-"`
	// Token is the GitHub token of a self-update: kept in memory only, never listed or logged.
	Token string `json:"-"`
}

// Actions the panel may request.
var Actions = []string{"start", "stop", "restart", "stopall", "save", "apply", "restore", "load", "unload", "pull", "download", "engine-install", "engine-update", "engine-remove", "model-delete", "auto-restart", "self-update", "preset-save", "preset-delete", "preset-apply", "model-test", "sleep", "autostart", "start-engine", "shutdown", "model-start", "poweroff", "start-model"}

// ParseRequest validates a panel operation body; unknown fields are refused.
func ParseRequest(body map[string]any) (Request, error) {
	var r Request
	allowed := []string{"action", "backend", "settings", "model", "url", "filename", "sha256", "engine", "version", "size", "enabled", "token",
		"mmproj_url", "mmproj_filename", "mmproj_sha256", "mmproj_size", "for_model"}
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
		case "mmproj_url":
			r.ProjectorURL = s
		case "mmproj_filename":
			r.ProjectorFilename = s
		case "mmproj_sha256":
			r.ProjectorSHA256 = s
		case "mmproj_size":
			r.ProjectorSize = s
		case "for_model":
			r.ForModel = s
		}
	}
	if !slices.Contains(Actions, r.Action) {
		return r, errors.New("Неизвестная операция панели.")
	}
	if r.Token != "" && r.Action != "self-update" {
		return r, errors.New("token нужен только для обновления программы.")
	}
	if r.Action == "model-start" {
		name, _, ok := config.SplitKey(r.Model)
		if !ok || config.Remote(name) {
			return r, errors.New("Нужна локальная модель: prismml:<файл> или ollama:<имя>.")
		}
		r.Engine = name
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
	switch r.Action {
	case "autostart":
		on, err := strconv.ParseBool(r.Enabled)
		if err != nil {
			return "", errors.New("Нужно enabled: true или false.")
		}
		if err := c.Services.SetAutomatic(config.PanelService, on); err != nil {
			return "", err
		}
		if on {
			return "OmniBox будет запускаться вместе с системой.", nil
		}
		return "OmniBox больше не запускается вместе с системой; включить его — ai on.", nil
	case "start-engine":
		name := r.EngineID
		if name != "" && !slices.Contains(config.EngineNames, name) {
			return "", errors.New("Нужен engine: prismml, ollama или пусто.")
		}
		err := c.Store.Update(func(cfg *config.Config) error {
			if _, ok := cfg.Engines[name]; name != "" && !ok {
				return fmt.Errorf("%s не установлен.", config.Label(name))
			}
			cfg.Panel.StartEngine = name
			return nil
		})
		if name == "" {
			return "Модель при старте не запускается.", err
		}
		return config.Label(name) + " с выбранной моделью будет запускаться при старте системы и командой ai on.", err
	case "shutdown":
		// Engines first, under the lock like any stop; the panel itself goes a moment later, so
		// the owner still gets the answer.
		if err := c.Locked(c.StopAll); err != nil {
			return "", err
		}
		go func() {
			time.Sleep(SleepDelay)
			if err := c.Services.Stop(config.PanelService, StopTimeout); err != nil {
				c.say("Не удалось остановить службу OmniBox: %v", err)
			}
		}()
		return "OmniBox выключается: движки остановлены, панель и API остановятся через несколько секунд. Включить снова — ai on (или перезагрузка, если включён автозапуск).", nil
	}
	switch r.Action {
	case "model-start":
		return c.StartModel(ctx, r.Model)
	case "start-model":
		// The model loaded once after the computer boots; the service itself starts by autostart.
		if r.Model != "" {
			if name, _, ok := config.SplitKey(r.Model); !ok || config.Remote(name) {
				return "", errors.New("Нужна локальная модель или пусто.")
			}
		}
		err := c.Store.Update(func(cfg *config.Config) error {
			cfg.Panel.StartModel = r.Model
			if r.Model != "" {
				cfg.Panel.StartEngine = ""
			}
			return nil
		})
		if r.Model == "" {
			return "После включения ПК модели не загружаются.", err
		}
		return "Эта модель будет загружаться один раз после включения ПК.", err
	case "poweroff":
		if c.PowerOff == nil {
			return "", errors.New("Выключение ПК недоступно в этом режиме.")
		}
		go func() {
			time.Sleep(SleepDelay)
			if err := c.PowerOff(); err != nil {
				c.say("Не удалось выключить ПК: %v", err)
			}
		}()
		return "ПК выключается. Включить его можно через Wake-on-LAN (Home Assistant) или кнопкой питания.", nil
	}
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
			fit := ""
			if p.Auto && c.Tune != nil {
				p.Settings, fit = c.Tune(name, e, p.Settings)
			}
			prepared, err := c.prepare(name, e, p.Settings)
			if err != nil {
				return err
			}
			if s, err := c.Services.Query(config.ServiceName(name)); err != nil || s.State != platform.Running {
				message = fmt.Sprintf("%s: пресет «%s» сохранён для следующего запуска.%s", label, p.Name, fit)
				return c.Save(name, prepared)
			}
			message = fmt.Sprintf("%s: пресет «%s» применён. Движок готов.%s", label, p.Name, fit)
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
		for _, p := range engine.Projectors(engine.ModelDirectory(e)) {
			known = append(known, p.ID)
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
