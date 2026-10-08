package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
)

func TestPrismCommandIsBuiltFromSettings(t *testing.T) {
	e := &config.Engine{Port: 8801}
	l := config.Launch{Executable: "/opt/omnibox/engines/prismml/b1/llama-server", Settings: map[string]string{
		"model": "/models/a.gguf", "alias": "ornith", "context": "32768", "gpu_layers": "999", "n_cpu_moe": "24",
		"idle_minutes": "30", "temperature": "0.7", "kv_offload": "off",
	}, ExtraArguments: []string{"--jinja"}}
	cmd, err := Build(config.PrismML, e, l)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(cmd.Args, " ")
	for _, want := range []string{"--host 127.0.0.1 --port 8801", "-m /models/a.gguf", "--alias ornith", "-c 32768", "-ngl 999", "--n-cpu-moe 24", "--sleep-idle-seconds 1800", "--temp 0.7", "--no-kv-offload", "-np 1"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if cmd.Args[len(cmd.Args)-1] != "--jinja" || strings.Contains(got, "-ctk") {
		t.Fatalf("extra arguments go last and unset settings are omitted: %q", got)
	}
	if _, err := Build(config.PrismML, e, config.Launch{}); err == nil {
		t.Fatal("an engine without an executable must not start")
	}
}

func TestOllamaCommandUsesEnvironment(t *testing.T) {
	e := &config.Engine{Port: 8802, ModelsDirectory: "/models/ollama"}
	l := config.Launch{Executable: "/opt/omnibox/engines/ollama/v1/ollama", Settings: map[string]string{"context": "32768", "keep_alive": "30m", "placement": "cpu"}, Environment: map[string]string{"OLLAMA_FLASH_ATTENTION": "1"}}
	cmd, err := Build(config.Ollama, e, l)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cmd.Args, []string{"serve"}) {
		t.Fatalf("args %v", cmd.Args)
	}
	want := map[string]string{"OLLAMA_HOST": "127.0.0.1:8802", "OLLAMA_CONTEXT_LENGTH": "32768", "OLLAMA_KEEP_ALIVE": "30m", "OLLAMA_MODELS": "/models/ollama",
		"OLLAMA_NUM_PARALLEL": "1", "OLLAMA_MAX_LOADED_MODELS": "1", "CUDA_VISIBLE_DEVICES": "-1", "OLLAMA_FLASH_ATTENTION": "1"}
	for k, v := range want {
		if cmd.Env[k] != v {
			t.Errorf("%s=%q, want %q", k, cmd.Env[k], v)
		}
	}
	if l.Environment["OLLAMA_HOST"] != "" {
		t.Fatal("building a command must not change the stored launch")
	}
	auto, _ := Build(config.Ollama, e, config.Launch{Executable: l.Executable, Settings: map[string]string{}})
	if _, masked := auto.Env["CUDA_VISIBLE_DEVICES"]; masked {
		t.Fatal("automatic placement leaves the GPU visible")
	}
}

func TestQuant(t *testing.T) {
	cases := map[string]string{
		"ornith-1.5-35b-a3b-Q4_K_M.gguf":     "Q4_K_M",
		"bonsai-2-27b-pq2_0.gguf":            "PQ2_0",
		"big-UD-Q2_K_XL-00001-of-00003.gguf": "UD-Q2_K_XL",
		"model-BF16.gguf":                    "BF16",
		"model.gguf":                         "",
	}
	for name, want := range cases {
		if got := Quant(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

func TestPrismModelsGroupSplitFilesAndSkipProjectors(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int) { os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644) }
	write("a-Q4_K_M.gguf", 10)
	write("big-Q2_K-00001-of-00002.gguf", 30)
	write("big-Q2_K-00002-of-00002.gguf", 70)
	write("mmproj-a.gguf", 5)
	write("notes.txt", 1)
	models, err := PrismModels(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].Name != "a-Q4_K_M.gguf" || models[1].Parts != 2 || models[1].Bytes != 100 {
		t.Fatalf("%+v", models)
	}
	if parts := GGUFParts(models[1].ID); len(parts) != 2 {
		t.Fatalf("parts %v", parts)
	}
	if m, err := PrismModels(filepath.Join(dir, "missing")); err != nil || m != nil {
		t.Fatalf("missing dir: %v %v", m, err)
	}
	e := &config.Engine{ModelsDirectory: dir}
	if ModelDirectory(e) != dir {
		t.Fatal("models directory without a selected model")
	}
	e.Settings = map[string]string{"model": models[0].ID}
	if ModelDirectory(e) != dir {
		t.Fatal("directory of the selected model")
	}
}

func TestProbes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer upstream" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/health":
			w.Write([]byte(`{"status":"ok"}`))
		case "/api/version":
			w.Write([]byte(`{"version":"0.12.0"}`))
		case "/api/ps":
			w.Write([]byte(`{"models":[{"name":"qwen3:8b","size":100,"size_vram":60,"context_length":32768}]}`))
		case "/api/tags":
			w.Write([]byte(`{"models":[{"name":"qwen3:8b","size":5000}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	port, _ := strconv.Atoi(srv.URL[strings.LastIndex(srv.URL, ":")+1:])
	c := NewClient(&config.Engine{Port: port})
	c.UpstreamKey = "upstream"
	ctx := context.Background()
	if !Ready(ctx, config.PrismML, c) || !Ready(ctx, config.Ollama, c) {
		t.Fatal("ready probes")
	}
	ps, err := c.OllamaPS(ctx)
	if err != nil || len(ps) != 1 || ps[0].SizeVRAM != 60 || ps[0].ContextLength != 32768 {
		t.Fatalf("ps %+v %v", ps, err)
	}
	tags, err := c.OllamaTags(ctx)
	if err != nil || tags[0].ID != "qwen3:8b" || tags[0].Bytes != 5000 {
		t.Fatalf("tags %+v %v", tags, err)
	}
	c.UpstreamKey = ""
	if Ready(ctx, config.PrismML, c) {
		t.Fatal("an engine answering 401 is not ready")
	}
}

func TestModelFamilyProjectorAndOllamaSource(t *testing.T) {
	if f := family("unsloth-gemma-3-27b-it-GGUF-gemma-3-27b-it-Q4_K_M.gguf"); f != "unsloth-gemma-3-27b-it-GGUF-gemma-3-27b-it" {
		t.Fatal(f)
	}
	projectors := []string{"unsloth-gemma-3-27b-it-GGUF-mmproj-F16.gguf"}
	if !hasProjector("unsloth-gemma-3-27b-it-GGUF-gemma-3-27b-it-Q4_K_M.gguf", projectors) || hasProjector("Qwen3-8B-Q4_K_M.gguf", projectors) {
		t.Fatal("projector match")
	}
	for in, want := range map[string]string{"qwen3:8b": "https://ollama.com/library/qwen3", "hf.co/unsloth/Qwen3-8B-GGUF:Q4_K_M": "https://huggingface.co/unsloth/Qwen3-8B-GGUF", "user/model:tag": "https://ollama.com/user/model"} {
		if got := OllamaSource(in); got != want {
			t.Fatalf("%s: %s", in, got)
		}
	}
}

func TestVisionProjectorIsPassedToPrismML(t *testing.T) {
	dir := t.TempDir()
	model := filepath.Join(dir, "unsloth-gemma-3-27b-it-GGUF-gemma-3-27b-it-Q4_K_M.gguf")
	mmproj := filepath.Join(dir, "unsloth-gemma-3-27b-it-GGUF-mmproj-F16.gguf")
	other := filepath.Join(dir, "Qwen3-8B-Q4_K_M.gguf")
	for _, f := range []string{model, mmproj, other} {
		os.WriteFile(f, []byte("GGUF"), 0o644)
	}
	if ProjectorFor(model, "") != mmproj || ProjectorFor(model, "auto") != mmproj || ProjectorFor(model, "off") != "" || ProjectorFor(other, "auto") != "" {
		t.Fatal("projector choice")
	}
	if ProjectorFor(other, mmproj) != mmproj || ProjectorFor(other, filepath.Join(dir, "gone-mmproj.gguf")) != "" {
		t.Fatal("explicit projector")
	}
	if ps := Projectors(dir); len(ps) != 1 || ps[0].ID != mmproj {
		t.Fatalf("%+v", ps)
	}
	if models, _ := PrismModels(dir); len(models) != 2 {
		t.Fatalf("a projector listed as a model: %+v", models)
	}
	e := &config.Engine{Port: 8801}
	cmd, err := Build(config.PrismML, e, config.Launch{Executable: "/x/llama-server", Settings: map[string]string{"model": model}})
	if err != nil || !strings.Contains(strings.Join(cmd.Args, " "), "--mmproj "+mmproj) {
		t.Fatalf("%v %v", cmd.Args, err)
	}
	cmd, _ = Build(config.PrismML, e, config.Launch{Executable: "/x/llama-server", Settings: map[string]string{"model": model, "mmproj": "off"}})
	if strings.Contains(strings.Join(cmd.Args, " "), "--mmproj") {
		t.Fatalf("off still passes a projector: %v", cmd.Args)
	}
}
