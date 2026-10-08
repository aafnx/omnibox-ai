package config

import (
	"strings"
	"testing"
)

func TestPresetsAreValidSettingsForms(t *testing.T) {
	for _, engine := range EngineNames {
		for _, p := range BuiltinPresets(engine) {
			if _, err := Apply(engine, map[string]string{}, p.Settings, nil); err != nil {
				t.Fatalf("%s %s: %v", engine, p.Name, err)
			}
		}
	}
	prism := map[string]string{"model": "/m/a.gguf", "alias": "a", "context": "65536", "gpu_layers": "30", "n_cpu_moe": "4", "cache_key": "q8_0", "flash_attention": "on"}
	from := PresetFrom(PrismML, prism)
	if _, ok := from["model"]; ok {
		t.Fatal("a preset picks the model")
	}
	next, err := Apply(PrismML, map[string]string{"model": "/m/b.gguf"}, from, nil)
	if err != nil || next["model"] != "/m/b.gguf" || next["gpu_layers"] != "30" || next["context"] != "65536" {
		t.Fatalf("%v %v", next, err)
	}
	ollama := PresetFrom(Ollama, map[string]string{"context": "8192", "placement": "cpu", "keep_alive": "30m"})
	if next, err := Apply(Ollama, map[string]string{}, ollama, nil); err != nil || next["keep_alive"] != "30m" || next["placement"] != "cpu" {
		t.Fatalf("%v %v", next, err)
	}
}

func TestOwnPresetsAreNamedAndLimited(t *testing.T) {
	e := &Engine{Launch: Launch{Settings: map[string]string{"context": "8192"}}}
	if err := e.SavePreset(PrismML, "CPU"); err == nil {
		t.Fatal("a built-in name taken")
	}
	if err := e.SavePreset(PrismML, "../x"); err == nil {
		t.Fatal("a bad name accepted")
	}
	if err := e.SavePreset(PrismML, " Ornith длинный "); err != nil {
		t.Fatal(err)
	}
	if p, ok := e.FindPreset(PrismML, "Ornith длинный"); !ok || p.Builtin || p.Settings["context"] != "8192" {
		t.Fatalf("%+v", p)
	}
	for i := range MaxPresets {
		e.SavePreset(PrismML, "p"+strings.Repeat("x", i%3)+string(rune('a'+i)))
	}
	if err := e.SavePreset(PrismML, "one more"); err == nil {
		t.Fatal("no limit")
	}
	if err := e.DeletePreset(PrismML, "CPU"); err == nil {
		t.Fatal("a built-in preset deleted")
	}
	if err := e.DeletePreset(PrismML, "Ornith длинный"); err != nil {
		t.Fatal(err)
	}
}
