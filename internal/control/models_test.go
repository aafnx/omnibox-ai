package control

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
)

func TestModelSettingsAreNormalizedAndChecked(t *testing.T) {
	r := newRig(t)
	key := "prismml:next-Q2_0.gguf"
	if err := SaveModelPrefs(r.store, key, map[string]string{"context": "64k", "cache_value": "q4_0", "temperature": "0.2", "alias": "", "mmproj": "off"}, true); err != nil {
		t.Fatal(err)
	}
	p := r.store.Get().Models[key]
	if p == nil || p.Settings["context"] != "65536" || p.Settings["cache_value"] != "q4_0" || p.Settings["mmproj"] != "off" || !p.Advanced {
		t.Fatalf("%+v", p)
	}
	if _, ok := p.Settings["alias"]; ok {
		t.Fatal("an empty alias is the automatic one, not stored")
	}
	for _, bad := range []map[string]string{{"ubatch": "4096", "batch": "1024"}, {"temperature": "7"}, {"mmproj": "/x/mmproj.gguf"}, {"model": "/x.gguf"}} {
		if err := SaveModelPrefs(r.store, key, bad, false); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	if err := SaveModelPrefs(r.store, "cloud:openai/gpt-5", map[string]string{"context": "8k"}, false); err == nil {
		t.Fatal("cloud models have no launch settings")
	}
	if err := SaveModelPrefs(r.store, key, nil, false); err != nil || r.store.Get().Models[key] != nil {
		t.Fatal("empty settings forget the model's own settings")
	}
}

func TestStartModelUsesItsOwnSettingsAndClearsTheOthers(t *testing.T) {
	r := newRig(t)
	r.store.Update(func(c *config.Config) error {
		c.Engines[config.PrismML].Settings["temperature"] = "1.5"
		return nil
	})
	key := "prismml:next-Q2_0.gguf"
	if err := SaveModelPrefs(r.store, key, map[string]string{"context": "16384", "gpu_layers": "20"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.StartModel(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	s := r.launched[config.PrismML]
	if filepath.Base(s["model"]) != "next-Q2_0.gguf" || s["context"] != "16384" || s["gpu_layers"] != "20" || s["alias"] != "next-q2_0" {
		t.Fatalf("%v", s)
	}
	if _, ok := s["temperature"]; ok {
		t.Fatal("a setting of the previous model stayed")
	}
	if s["cache_key"] != "q8_0" || s["flash_attention"] != "on" {
		t.Fatalf("base preset missing: %v", s)
	}
	if _, err := r.c.StartModel(context.Background(), "prismml:missing.gguf"); err == nil {
		t.Fatal("a missing model started")
	}
	if _, err := r.c.StartModel(context.Background(), "cloud:x/y"); err == nil {
		t.Fatal("a cloud model is not started")
	}
}

func TestFavorites(t *testing.T) {
	r := newRig(t)
	SetFavorite(r.store, "cloud:openai/gpt-5", true)
	SetFavorite(r.store, "ollama:qwen3:8b", true)
	SetFavorite(r.store, "cloud:openai/gpt-5", true)
	SetFavorite(r.store, "ollama:qwen3:8b", false)
	if f := r.store.Get().Favorites; len(f) != 1 || f[0] != "cloud:openai/gpt-5" {
		t.Fatal(f)
	}
	if err := SetFavorite(r.store, "x", true); err == nil {
		t.Fatal("a bad key was accepted")
	}
}
