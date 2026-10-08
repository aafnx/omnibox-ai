package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"strconv"
	"time"

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
	vram, measured := s.vram(ctx)
	est := gguf.Place(meta, model.Bytes, context, cache("cache_key"), cache("cache_value"), vram, s.RAM())
	if !measured {
		est.Text += " VRAM не определена (nvidia-smi не ответил); расчёт для 16 ГБ."
	}
	return est, nil
}

func (s estimator) vram(ctx context.Context) (int64, bool) {
	if gpus := s.GPUs(ctx); len(gpus) > 0 && gpus[0].TotalMB > 0 {
		return int64(gpus[0].TotalMB) << 20, true
	}
	return defaultVRAM, false
}

// desktopVRAM is what Windows, the browser and other programs keep in VRAM: a model planned to
// the last megabyte spills into shared memory, and the driver makes generation many times slower.
const desktopVRAM = int64(768) << 20

// baseContexts are the contexts the base preset tries, longest first.
var baseContexts = []int{131072, 65536, 32768, 16384, 8192}

// Tune fits the base preset to the selected GGUF and this GPU: the longest context whose weights
// and q8_0 KV cache stay in VRAM; a model larger than VRAM gets RAM + VRAM, or its MoE experts
// in RAM. Without a readable model the preset's own values stay.
func (s estimator) Tune(name string, e *config.Engine, base map[string]string) (map[string]string, string) {
	out := maps.Clone(base)
	path := e.Settings["model"]
	if name != config.PrismML || path == "" {
		return out, ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return out, " Модель не найдена: контекст 32k."
	}
	meta, err := gguf.Read(path)
	if err != nil {
		return out, " Заголовок GGUF не прочитан: контекст 32k."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	vram, _ := s.vram(ctx)
	budget, ram := vram-desktopVRAM, s.RAM()
	gb := func(b int64) string { return fmt.Sprintf("%.1f ГБ", float64(b)/(1<<30)) }
	tried := false
	for _, n := range baseContexts {
		if meta.Context > 0 && n > meta.Context {
			continue
		}
		tried = true
		if est := gguf.Place(meta, info.Size(), n, "q8_0", "q8_0", budget, ram); est.Placement == "gpu" {
			out["context"] = strconv.Itoa(n)
			return out, fmt.Sprintf(" Контекст %d: веса и KV-кеш в VRAM (≈ %s из %s).", n, gb(est.VRAMNeeded), gb(vram))
		}
	}
	n := 32768
	if !tried || (meta.Context > 0 && meta.Context < n) {
		n = meta.Context
	}
	if n > 0 {
		out["context"] = strconv.Itoa(n)
	}
	est := gguf.Place(meta, info.Size(), n, "q8_0", "q8_0", budget, ram)
	switch {
	case est.MoE:
		// Experts are most of a MoE layer: moving a layer's experts frees a little less than the
		// whole layer, so a few more layers go.
		moe := min(meta.Blocks, (meta.Blocks-est.GPULayers)*10/9+1)
		out["n_cpu_moe"] = strconv.Itoa(moe)
		return out, fmt.Sprintf(" Модель больше VRAM: эксперты MoE %d из %d слоёв — в RAM, остальное в VRAM.", moe, meta.Blocks)
	case est.GPULayers >= 1 && est.GPULayers <= 98:
		out["placement"], out["gpu_layers"] = "hybrid", strconv.Itoa(est.GPULayers)
		return out, fmt.Sprintf(" Модель больше VRAM: %d из %d слоёв на GPU, остальные в RAM.", est.GPULayers, meta.Blocks)
	default:
		out["placement"] = "cpu"
		return out, " Модель не помещается в VRAM: только RAM."
	}
}
