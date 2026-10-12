package control

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strconv"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// StartModel loads a local model by its key (prismml:<file> or ollama:<name>) with the settings
// saved for it; without them PrismML gets the base preset fitted to the model. The other engine
// stops first: one model at a time.
func (c *Controller) StartModel(ctx context.Context, key string) (string, error) {
	name, model, ok := config.SplitKey(key)
	if !ok || config.Remote(name) {
		return "", errors.New("Нужна локальная модель: prismml:<файл> или ollama:<имя>.")
	}
	cfg := c.Store.Get()
	e, ok := cfg.Engines[name]
	if !ok {
		return "", fmt.Errorf("%s не установлен.", config.Label(name))
	}
	var prefs map[string]string
	if p := cfg.Models[key]; p != nil {
		prefs = maps.Clone(p.Settings)
	}
	if name == config.Ollama {
		r := Request{Action: "load", Engine: name, Model: model}
		if v := prefs["context"]; v != "" && v != e.Settings["context"] {
			r.Settings = map[string]string{"context": v}
		}
		return c.ollamaLoad(ctx, name, r)
	}
	models, err := engine.PrismModels(engine.ModelDirectory(e))
	if err != nil {
		return "", err
	}
	path := ""
	for _, m := range models {
		if filepath.Base(m.ID) == model {
			path = m.ID
		}
	}
	if path == "" {
		return "", fmt.Errorf("Модель %s не найдена в каталоге моделей PrismML.", model)
	}
	fit := ""
	err = c.Locked(func() error {
		e, err := c.engine(name)
		if err != nil {
			return err
		}
		changes := modelChanges(name, e, path, prefs)
		if _, own := prefs["context"]; !own && c.Tune != nil {
			probe := *e
			probe.Settings = maps.Clone(e.Settings)
			probe.Settings["model"] = path
			changes, fit = c.Tune(name, &probe, changes)
		}
		prepared, err := c.prepare(name, e, changes)
		if err != nil {
			return err
		}
		verb := "start"
		if info, err := c.state(name); err == nil && info.State != platform.Stopped {
			verb = "restart"
		}
		if verb == "restart" && prepared.Equal(e.RunningLaunch()) && !e.Pending {
			fit = ""
			return nil
		}
		return c.Apply(name, verb, &prepared)
	})
	return "Модель запущена. API готов." + fit, err
}

// modelChanges is the settings form of a model start: the base preset, then the model's own
// settings. Options the model does not set are cleared, so nothing of the previous model stays.
func modelChanges(name string, e *config.Engine, path string, prefs map[string]string) map[string]string {
	changes := config.BaseSettings(name)
	for _, o := range config.PrismOptions {
		changes[o.Key] = ""
	}
	changes["kv_offload"] = "on"
	changes["mmproj"] = "auto"
	changes["alias"] = ""
	changes["idle_minutes"] = ""
	maps.Copy(changes, prefs)
	changes["model"] = path
	derivePlacement(changes, prefs)
	if changes["placement"] == "cpu" {
		delete(changes, "kv_offload")
	}
	return changes
}

// derivePlacement turns the owner's GPU layers into the placement the form expects: the base
// preset's "gpu" would otherwise put every layer back on the GPU.
func derivePlacement(changes, own map[string]string) {
	v, ok := own["gpu_layers"]
	if _, set := own["placement"]; !ok || set {
		return
	}
	n, _ := strconv.Atoi(v)
	switch {
	case n == 0:
		changes["placement"] = "cpu"
	case n < 99:
		changes["placement"] = "hybrid"
	default:
		changes["placement"] = "gpu"
	}
}

// SaveModelPrefs validates and stores a local model's settings; an empty map forgets them.
func SaveModelPrefs(store *config.Store, key string, settings map[string]string, advanced bool) error {
	name, _, ok := config.SplitKey(key)
	if !ok || config.Remote(name) {
		return errors.New("Нужна локальная модель.")
	}
	for k, v := range settings {
		// The projector can only be switched off for a model, never pointed at a file.
		if k == "model" || (k == "mmproj" && v != "off" && v != "") {
			return errors.New("Настройки модели не выбирают сам файл модели.")
		}
	}
	// The form is checked on top of the base preset, as the model will start: impossible values
	// are refused here, not at the start. What is stored is the normalized form of the owner's
	// own values (128k becomes 131072, a placement becomes its layers).
	clean := map[string]string{}
	for k, v := range settings {
		if v != "" {
			clean[k] = v
		}
	}
	merged := config.BaseSettings(name)
	maps.Copy(merged, clean)
	derivePlacement(merged, clean)
	next, err := config.Apply(name, map[string]string{}, merged, nil)
	if err != nil {
		return err
	}
	stored := map[string]string{}
	for k := range clean {
		if v, ok := next[k]; ok {
			stored[k] = v
		}
	}
	if _, ok := clean["placement"]; ok {
		for _, k := range []string{"gpu_layers", "kv_offload", "keep_alive"} {
			if v, ok := next[k]; ok {
				stored[k] = v
			}
		}
	}
	if _, ok := clean["idle_minutes"]; ok && name == config.Ollama {
		stored["keep_alive"] = next["keep_alive"]
	}
	settings = stored
	return store.Update(func(c *config.Config) error {
		if len(settings) == 0 && !advanced {
			delete(c.Models, key)
			return nil
		}
		if c.Models == nil {
			c.Models = map[string]*config.ModelPrefs{}
		}
		c.Models[key] = &config.ModelPrefs{Settings: maps.Clone(settings), Advanced: advanced}
		return nil
	})
}

// SetFavorite adds a model key to the favourites or removes it.
func SetFavorite(store *config.Store, key string, on bool) error {
	if _, _, ok := config.SplitKey(key); !ok {
		return errors.New("Некорректная модель.")
	}
	return store.Update(func(c *config.Config) error {
		out := c.Favorites[:0:0]
		for _, f := range c.Favorites {
			if f != key {
				out = append(out, f)
			}
		}
		if on {
			if len(out) >= config.MaxFavorites {
				return fmt.Errorf("Не больше %d избранных моделей.", config.MaxFavorites)
			}
			out = append(out, key)
		}
		c.Favorites = out
		return nil
	})
}
