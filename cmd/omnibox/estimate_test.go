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
