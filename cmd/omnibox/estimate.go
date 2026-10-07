package main

import (
	"context"
	"errors"
	"net/url"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/gguf"
	"github.com/aafnx/omnibox-ai/internal/telemetry"
)

// defaultVRAM is the owner's RTX 5080, used when nvidia-smi does not answer.
const defaultVRAM = int64(16) << 30

// estimator answers the panel's "where does this model fit" for PrismML: only a GGUF from the
// model directory is read, and only its header.
type estimator struct {
	Config func() *config.Config
	GPUs   func(ctx context.Context) []telemetry.GPU
	RAM    func() int64
}

func (s estimator) Estimate(ctx context.Context, q url.Values) (any, error) {
	e, ok := s.Config().Engines[config.PrismML]
	if !ok {
		return nil, errors.New("PrismML не установлен.")
	}
	models, err := engine.PrismModels(engine.ModelDirectory(e))
	if err != nil {
		return nil, err
	}
	id := q.Get("model")
	if id == "" {
		id = e.Settings["model"]
	}
	var model *engine.Model
	for i := range models {
		if models[i].ID == id {
			model = &models[i]
		}
	}
	if model == nil {
		return nil, errors.New("Выберите GGUF из каталога моделей.")
	}
	meta, err := gguf.Read(model.ID)
	if err != nil {
		return nil, errors.New("Заголовок GGUF не прочитан: " + err.Error())
	}
	context := 0
	if v := q.Get("context"); v != "" {
		if context, err = config.ParseContext(v); err != nil {
			return nil, err
		}
	} else if v := e.Settings["context"]; v != "" {
		context, _ = config.ParseContext(v)
	}
	cache := func(key string) string {
		if v := q.Get(key); v != "" {
			return v
		}
		if v := e.Settings[key]; v != "" {
			return v
		}
		return "f16"
	}
	vram, measured := defaultVRAM, false
	if gpus := s.GPUs(ctx); len(gpus) > 0 && gpus[0].TotalMB > 0 {
		vram, measured = int64(gpus[0].TotalMB)<<20, true
	}
	est := gguf.Place(meta, model.Bytes, context, cache("cache_key"), cache("cache_value"), vram, s.RAM())
	if !measured {
		est.Text += " VRAM не определена (nvidia-smi не ответил); расчёт для 16 ГБ."
	}
	return est, nil
}
