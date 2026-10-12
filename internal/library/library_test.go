package library

import (
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/chatgpt"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/openrouter"
	"github.com/aafnx/omnibox-ai/internal/reasoning"
)

func TestDisplayName(t *testing.T) {
	for in, want := range map[string]string{
		"unsloth-gemma-4-12b-it-GGUF-gemma-4-12b-it-Q4_K_M.gguf": "gemma-4-12b-it",
		"Qwen3-30B-A3B-UD-Q4_K_XL-00001-of-00002.gguf":           "Qwen3-30B-A3B",
		"Ternary-Bonsai-2-27B-PQ2_0.gguf":                        "Ternary-Bonsai-2-27B",
		"HauhauCS-Gemma4-12B-QAT-Uncensored-HauhauCS-Balanced-Gemma4-12B-QAT-Uncensored-HauhauCS-Balanced-Q4_K_M.gguf": "HauhauCS-Gemma4-12B-QAT-Uncensored-HauhauCS-Balanced",
		"Llama-3-8B-8B.gguf": "Llama-3-8B-8B",
	} {
		if got := DisplayName(config.PrismML, in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
	if DisplayName(config.Ollama, "qwen3:8b") != "qwen3:8b" {
		t.Fatal("ollama names stay")
	}
}

func TestListMergesLocalAndCloud(t *testing.T) {
	cfg := config.Default()
	cfg.Favorites = []string{"ollama:qwen3:8b", "cloud:openai/gpt-5"}
	cfg.Models = map[string]*config.ModelPrefs{"prismml:a-Q4_K_M.gguf": {Settings: map[string]string{"context": "65536", "alias": "mine"}, Advanced: true}}
	run := Running{Key: "prismml:a-Q4_K_M.gguf", Alias: "mine"}
	prism := Local(cfg, config.PrismML, []engine.Details{{Model: engine.Model{ID: "/m/a-Q4_K_M.gguf", Name: "a-Q4_K_M.gguf"}, Capabilities: []string{"reasoning", "vision"}, ThinkToggle: true}}, run)
	if len(prism) != 1 || !prism[0].Running || prism[0].Alias != "mine" || !prism[0].Caps.Vision || prism[0].Reasoning.Mode != reasoning.ModeToggle || prism[0].Settings["context"] != "65536" || !prism[0].Advanced {
		t.Fatalf("%+v", prism)
	}
	ollama := Local(cfg, config.Ollama, []engine.Details{{Model: engine.Model{ID: "qwen3:8b", Name: "qwen3:8b"}, Capabilities: []string{"reasoning", "tools"}}}, run)
	if !ollama[0].Favorite || ollama[0].Running || ollama[0].Reasoning.Mode != reasoning.ModeToggle || !ollama[0].Caps.Tools {
		t.Fatalf("%+v", ollama)
	}
	cloud := Cloud(cfg, []openrouter.Model{{ID: "openai/gpt-5", Name: "OpenAI: GPT-5", Provider: "OpenAI", Caps: openrouter.Caps{Vision: true, Tools: true}}, {ID: "bad id", Name: "x"}})
	if len(cloud) != 1 || cloud[0].Name != "GPT-5" || !cloud[0].Favorite || cloud[0].Key != "cloud:openai/gpt-5" {
		t.Fatalf("%+v", cloud)
	}
	if m, ok := Find(append(prism, cloud...), "cloud:openai/gpt-5"); !ok || m.Source != "cloud" {
		t.Fatal("find")
	}
}

func TestRunningOf(t *testing.T) {
	cfg := config.Default()
	cfg.Engines[config.PrismML] = &config.Engine{Port: 8801, Launch: config.Launch{Settings: map[string]string{"model": "/m/x.gguf", "alias": "x"}}}
	cfg.Engines[config.Ollama] = &config.Engine{Port: 8802, Model: "qwen3:8b"}
	if r := RunningOf(cfg, func(e string) bool { return e == config.Ollama }); r.Key != "ollama:qwen3:8b" {
		t.Fatal(r)
	}
	if r := RunningOf(cfg, func(e string) bool { return e == config.PrismML }); r.Key != "prismml:x.gguf" || r.Alias != "x" {
		t.Fatal(r)
	}
	if r := RunningOf(cfg, func(string) bool { return false }); r.Key != "" {
		t.Fatal(r)
	}
}

func TestChatGPTModels(t *testing.T) {
	cfg := config.Default()
	cfg.Favorites = []string{"chatgpt:gpt-5.5"}
	list := ChatGPT(cfg, []chatgpt.Model{
		{ID: "gpt-5.5", Name: "GPT-5.5", Efforts: []string{"high", "low", "xhigh"}, Default: "medium", Images: true},
		{ID: "bad/id", Name: "Bad"},
	})
	if len(list) != 1 {
		t.Fatalf("%+v", list)
	}
	m := list[0]
	if m.Key != "chatgpt:gpt-5.5" || m.Source != "chatgpt" || !m.Favorite || !m.Caps.Vision || m.Caps.Tools || !m.Caps.Search || m.Alias != "chatgpt/gpt-5.5" || m.Context != ChatGPTWindow {
		t.Fatalf("%+v", m)
	}
	// Levels keep the scale's order; a default the model does not list falls to the lightest.
	if strings.Join(m.Reasoning.Efforts, ",") != "low,high,xhigh" || m.Reasoning.Default != "low" || m.Reasoning.Mode != reasoning.ModeAlways {
		t.Fatalf("%+v", m.Reasoning)
	}
}
