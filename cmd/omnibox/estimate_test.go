package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/gguf"
	"github.com/aafnx/omnibox-ai/internal/telemetry"
)

func ggufFile(t *testing.T, path string, padding int) {
	t.Helper()
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { w(uint64(len(s))); b.WriteString(s) }
	b.WriteString("GGUF")
	w(uint32(3))
	w(uint64(0))
	w(uint64(4))
	str("general.architecture")
	w(uint32(8))
	str("qwen3")
	for _, kv := range [][2]any{{"qwen3.block_count", uint32(40)}, {"qwen3.attention.head_count", uint32(32)}, {"qwen3.attention.head_count_kv", uint32(8)}} {
		str(kv[0].(string))
		w(uint32(4))
		w(kv[1])
	}
	b.Write(make([]byte, padding))
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEstimateReadsOnlyModelsFromTheModelDirectory(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "qwen3-Q4_K_M.gguf")
	ggufFile(t, model, 1000)
	outside := filepath.Join(t.TempDir(), "other-Q4_K_M.gguf")
	ggufFile(t, outside, 10)
	cfg := config.Default()
	cfg.Engines[config.PrismML] = &config.Engine{ModelsDirectory: dir, Launch: config.Launch{Settings: map[string]string{"model": model, "context": "8192"}}}
	s := estimator{Config: func() *config.Config { return cfg }, RAM: func() int64 { return 32 << 30 },
		GPUs: func(context.Context) []telemetry.GPU { return []telemetry.GPU{{TotalMB: 16303}} }}
	got, err := s.Estimate(context.Background(), url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	e := got.(gguf.Estimate)
	if e.Layers != 40 || e.Placement != "gpu" || e.Context != 8192 || e.VRAM != 16303<<20 {
		t.Fatalf("%+v", e)
	}
	if _, err := s.Estimate(context.Background(), url.Values{"model": {outside}}); err == nil {
		t.Fatal("a file outside the model directory was read")
	}
	if _, err := s.Estimate(context.Background(), url.Values{"context": {"5"}}); err == nil {
		t.Fatal("a wrong context accepted")
	}
	s.GPUs = func(context.Context) []telemetry.GPU { return nil }
	got, _ = s.Estimate(context.Background(), url.Values{"context": {"32k"}, "cache_key": {"q8_0"}})
	if e := got.(gguf.Estimate); e.VRAM != defaultVRAM || !strings.Contains(e.Text, "16 ГБ") || e.Context != 32768 {
		t.Fatalf("%+v", e)
	}
}

// tunedModel writes a small GGUF whose KV cache is what decides the fit.
func tunedModel(t *testing.T, headsKV, keyLength, experts uint32) string {
	t.Helper()
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { w(uint64(len(s))); b.WriteString(s) }
	keys := [][2]any{{"llama.block_count", uint32(40)}, {"llama.context_length", uint32(262144)}, {"llama.attention.head_count", uint32(64)},
		{"llama.attention.head_count_kv", headsKV}, {"llama.attention.key_length", keyLength}, {"llama.expert_count", experts}}
	b.WriteString("GGUF")
	w(uint32(3))
	w(uint64(0))
	w(uint64(len(keys) + 1))
	str("general.architecture")
	w(uint32(8))
	str("llama")
	for _, kv := range keys {
		str(kv[0].(string))
		w(uint32(4))
		w(kv[1])
	}
	path := filepath.Join(t.TempDir(), "model-Q4_K_M.gguf")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBasePresetFitsTheContextToVRAM(t *testing.T) {
	tune := func(model string, vramMB int) (map[string]string, string) {
		s := estimator{RAM: func() int64 { return 32 << 30 }, GPUs: func(context.Context) []telemetry.GPU { return []telemetry.GPU{{TotalMB: vramMB}} }}
		e := &config.Engine{Launch: config.Launch{Settings: map[string]string{"model": model}}}
		return s.Tune(config.PrismML, e, config.BaseSettings(config.PrismML))
	}
	// 2 KiB of q8_0 cache per token and layer: 128k needs about 10.6 GiB, 64k about 5.3 GiB.
	dense := tunedModel(t, 8, 128, 0)
	if s, note := tune(dense, 16303); s["context"] != "131072" || s["placement"] != "gpu" || s["cache_key"] != "q8_0" || s["flash_attention"] != "on" || !strings.Contains(note, "в VRAM") {
		t.Fatalf("16 GB: %v %s", s, note)
	}
	if s, _ := tune(dense, 8192); s["context"] != "65536" {
		t.Fatalf("8 GB: %v", s)
	}
	// A cache too large even for 8k on the GPU: a dense model splits its layers, MoE moves experts.
	huge := tunedModel(t, 64, 512, 0)
	if s, note := tune(huge, 16303); s["placement"] != "hybrid" || s["gpu_layers"] == "" || s["context"] != "32768" || !strings.Contains(note, "слоёв на GPU") {
		t.Fatalf("dense: %v %s", s, note)
	}
	moe := tunedModel(t, 64, 512, 8)
	if s, note := tune(moe, 16303); s["placement"] != "gpu" || s["n_cpu_moe"] == "0" || s["n_cpu_moe"] == "" || !strings.Contains(note, "MoE") {
		t.Fatalf("moe: %v %s", s, note)
	}
	// An unreadable model keeps the preset's values.
	broken := filepath.Join(t.TempDir(), "broken.gguf")
	os.WriteFile(broken, []byte("not a model"), 0o644)
	if s, note := tune(broken, 16303); s["context"] != "32768" || !strings.Contains(note, "32k") {
		t.Fatalf("broken: %v %s", s, note)
	}
	// Each result is a valid settings form.
	for _, model := range []string{dense, huge, moe} {
		s, _ := tune(model, 16303)
		if _, err := config.Apply(config.PrismML, map[string]string{"model": model}, s, []string{model}); err != nil {
			t.Fatalf("%v: %v", s, err)
		}
	}
}
