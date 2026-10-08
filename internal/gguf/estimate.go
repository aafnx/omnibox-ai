package gguf

import "fmt"

// Estimate is where a model fits with given settings. All sizes are bytes; it is an estimate:
// the engine's buffers vary with the build and batch size.
type Estimate struct {
	Weights    int64  `json:"weights_bytes"`
	KVCache    int64  `json:"kv_cache_bytes"`
	Overhead   int64  `json:"overhead_bytes"`
	Layers     int    `json:"layers"`
	GPULayers  int    `json:"gpu_layers"` // recommended; Layers means everything in VRAM
	VRAMNeeded int64  `json:"vram_bytes"` // with the recommended layers
	RAMNeeded  int64  `json:"ram_bytes"`  // what stays in RAM with them
	VRAM       int64  `json:"vram_total"` // budget used for the estimate
	RAM        int64  `json:"ram_total"`
	Placement  string `json:"placement"` // gpu, hybrid, cpu or none
	Context    int    `json:"context"`
	MaxContext int    `json:"max_context"` // the model's trained context
	Text       string `json:"text"`
	MoE        bool   `json:"moe"`
}

// Overheads that do not depend on the model much: the CUDA context and compute buffers, and RAM
// the system and other programs keep.
const (
	gpuOverhead = 1 << 30
	ramReserve  = 6 << 30
	gib         = float64(1 << 30)
)

// Place estimates the model's memory with the context and cache types, and how many layers fit
// into vram (0 means no GPU). The weights are the file size; each layer gets an equal share.
func Place(m Meta, fileBytes int64, context int, cacheKey, cacheValue string, vram, ram int64) Estimate {
	if context <= 0 {
		context = m.Context
	}
	e := Estimate{Weights: fileBytes, Layers: m.Blocks, VRAM: vram, RAM: ram, Context: context, MaxContext: m.Context, MoE: m.Experts > 1}
	perToken := float64(m.HeadsKV) * (float64(m.KeyLength)*cacheBytes(cacheKey) + float64(m.ValueLength)*cacheBytes(cacheValue))
	kvLayers := m.KVLayers
	if kvLayers <= 0 || kvLayers > m.Blocks {
		kvLayers = m.Blocks
	}
	e.KVCache = int64(perToken * float64(context) * float64(kvLayers))
	layer := float64(fileBytes) / float64(m.Blocks)
	kvLayer := float64(e.KVCache) / float64(m.Blocks)
	if vram > 0 {
		e.Overhead = gpuOverhead
		budget := float64(vram) - gpuOverhead
		e.GPULayers = max(0, min(m.Blocks, int(budget/(layer+kvLayer))))
	}
	gpuShare := float64(e.GPULayers) / float64(m.Blocks)
	if e.GPULayers > 0 {
		e.VRAMNeeded = int64(gpuShare*float64(fileBytes+e.KVCache)) + e.Overhead
	}
	e.RAMNeeded = int64((1 - gpuShare) * float64(fileBytes+e.KVCache))
	ramFits := ram == 0 || e.RAMNeeded <= ram-ramReserve
	switch {
	case e.GPULayers == m.Blocks:
		e.Placement = "gpu"
	case e.GPULayers > 0 && ramFits:
		e.Placement = "hybrid"
	case e.GPULayers == 0 && ramFits:
		e.Placement = "cpu"
	default:
		e.Placement = "none"
	}
	size := func(b int64) string { return fmt.Sprintf("%.1f ГБ", float64(b)/gib) }
	switch e.Placement {
	case "gpu":
		e.Text = fmt.Sprintf("Помещается в VRAM: веса %s + KV-кеш %s + буферы ≈ %s из %s.", size(fileBytes), size(e.KVCache), size(e.VRAMNeeded), size(vram))
	case "hybrid":
		e.Text = fmt.Sprintf("Целиком в VRAM не помещается: веса %s + KV-кеш %s. Рекомендация — RAM + VRAM, %d из %d слоёв на GPU (≈ %s VRAM, %s RAM).",
			size(fileBytes), size(e.KVCache), e.GPULayers, m.Blocks, size(e.VRAMNeeded), size(e.RAMNeeded))
		if e.MoE {
			e.Text += " Для MoE лучше выбрать VRAM и вынести экспертов в RAM («Слои MoE в RAM»)."
		}
	case "cpu":
		e.Text = fmt.Sprintf("Только RAM: веса %s + KV-кеш %s ≈ %s.", size(fileBytes), size(e.KVCache), size(e.RAMNeeded))
	default:
		e.Text = fmt.Sprintf("Не помещается: веса %s + KV-кеш %s больше, чем VRAM и свободная RAM. Уменьшите контекст или выберите квантизацию меньше.", size(fileBytes), size(e.KVCache))
	}
	if m.Context > 0 && context > m.Context {
		e.Text += fmt.Sprintf(" Контекст больше обученного (%d).", m.Context)
	}
	return e
}
