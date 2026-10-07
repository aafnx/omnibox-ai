package config

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
)

func validConfig() *Config {
	c := Default()
	c.Panel.Hosts = []string{"192.168.1.77", "omnibox", "localhost"}
	b64 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	c.Panel.Password = Password{Salt: b64, Hash: b64, Iterations: 210000}
	c.API = API{Enabled: true, PublicURL: "http://192.168.1.77:18090/v1", RequireKey: true, KeyHash: strings.Repeat("a", 64), Engine: PrismML}
	c.Engines[PrismML] = &Engine{Port: 18080, Launch: Launch{Executable: `C:\Program Files\OmniBox AI\engines\prismml\b1\llama-server.exe`, Settings: map[string]string{"context": "32768"}}}
	c.Engines[Ollama] = &Engine{Port: 11434, Launch: Launch{Executable: "/opt/omnibox/engines/ollama/v1/ollama", Settings: map[string]string{"keep_alive": "30m"}}}
	return c
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := validConfig()
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Engines[PrismML].Settings["context"] != "32768" || loaded.API.KeyHash != c.API.KeyHash || loaded.Engines[Ollama].BaseURL() != "http://127.0.0.1:11434" {
		t.Fatalf("%+v", loaded)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil || !strings.Contains(err.Error(), "Нет настроек") {
		t.Fatalf("missing file: %v", err)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	if _, err := Parse([]byte(`{"schema_version":1,"surprise":true}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	cases := map[string]func(c *Config){
		"port":          func(c *Config) { c.Panel.Port = 80 },
		"host":          func(c *Config) { c.Panel.Hosts = []string{"host:80"} },
		"password":      func(c *Config) { c.Panel.Password.Iterations = 1000 },
		"proxy alone":   func(c *Config) { c.Panel.PublicOrigin = "https://llm.fnx.red" },
		"proxy cidr":    func(c *Config) { c.Panel.TrustedProxies = []string{"10.0.0.0/8"}; c.Panel.PublicOrigin = "https://x.y" },
		"api url":       func(c *Config) { c.API.PublicURL = "https://llm.fnx.red/" },
		"key required":  func(c *Config) { c.API.KeyHash = "" },
		"api engine":    func(c *Config) { c.API.Engine = "vllm" },
		"engine name":   func(c *Config) { c.Engines["vllm"] = &Engine{Port: 8000} },
		"engine port":   func(c *Config) { c.Engines[Ollama].Port = 18080 },
		"panel port":    func(c *Config) { c.Engines[Ollama].Port = 18090 },
		"relative exe":  func(c *Config) { c.Engines[PrismML].Executable = `engines\llama-server.exe` },
		"bad setting":   func(c *Config) { c.Engines[PrismML].Settings["gpu_layers"] = "lots" },
		"unknown key":   func(c *Config) { c.Engines[Ollama].Settings["gpu_layers"] = "1" },
		"ollama model":  func(c *Config) { c.Engines[Ollama].Model = "../etc" },
		"newline arg":   func(c *Config) { c.Engines[PrismML].ExtraArguments = []string{"--x\n--y"} },
		"retention":     func(c *Config) { c.Storage.LogRetentionDays = 400 },
		"request log":   func(c *Config) { c.Storage.RequestLog = "all" },
		"previous bad":  func(c *Config) { c.Engines[PrismML].Previous = &Launch{Settings: map[string]string{"batch": "0"}} },
		"models folder": func(c *Config) { c.Engines[PrismML].ModelsDirectory = "models" },
	}
	for name, change := range cases {
		c := validConfig()
		change(c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	c := validConfig()
	c.Panel.PublicOrigin = "https://LLM.fnx.red/"
	c.Panel.TrustedProxies = []string{"192.168.1.10"}
	if err := c.Validate(); err != nil || c.Panel.PublicOrigin != "https://llm.fnx.red" {
		t.Fatalf("proxy: %v %q", err, c.Panel.PublicOrigin)
	}
}

func TestStageKeepsLastWorkingConfigurationAcrossUnappliedChanges(t *testing.T) {
	e := validConfig().Engines[PrismML]
	original := e.Launch.Clone()
	first := e.Launch.Clone()
	first.Settings["context"] = "65536"
	e.Stage(first)
	second := e.Launch.Clone()
	second.Settings["context"] = "131072"
	e.Stage(second)
	if !e.Pending || !e.Previous.Equal(original) || e.Settings["context"] != "131072" {
		t.Fatalf("pending %v previous %+v", e.Pending, e.Previous)
	}
	if !e.RunningLaunch().Equal(original) {
		t.Fatal("a running engine still runs the previous configuration")
	}
	if err := e.Restore(); err != nil || e.Pending || !e.Launch.Equal(original) || !e.Restored() {
		t.Fatalf("restore: %v", err)
	}
	e.Stage(first)
	e.Confirm()
	if e.Pending || !e.Previous.Equal(original) || e.Restored() {
		t.Fatal("confirm keeps the previous configuration for a manual restore")
	}
}

func TestApplyPrismSettings(t *testing.T) {
	known := []string{`C:\models\a-Q4_K_M.gguf`}
	s, err := Apply(PrismML, map[string]string{"context": "32768"}, map[string]string{
		"context": "128k", "model": known[0], "alias": "ornith", "gpu_layers": "40", "n_cpu_moe": "24",
		"cache_key": "q8_0", "cache_value": "q8_0", "flash_attention": "on", "idle_minutes": "30", "temperature": "0.7", "reasoning": "off",
	}, known)
	if err != nil {
		t.Fatal(err)
	}
	if s["context"] != "131072" || s["n_cpu_moe"] != "24" || s["idle_minutes"] != "30" || s["temperature"] != "0.7" || s["model"] != known[0] {
		t.Fatalf("%v", s)
	}
	cleared, err := Apply(PrismML, s, map[string]string{"temperature": "", "n_cpu_moe": "0"}, known)
	if err != nil || cleared["temperature"] != "" || cleared["n_cpu_moe"] != "" {
		t.Fatalf("clear: %v %v", cleared, err)
	}
	if _, ok := cleared["temperature"]; ok {
		t.Fatal("an empty option is removed, so the engine default applies")
	}
	cpu, _ := Apply(PrismML, s, map[string]string{"placement": "cpu"}, known)
	if cpu["gpu_layers"] != "0" || cpu["kv_offload"] != "off" || cpu["n_cpu_moe"] != "" {
		t.Fatalf("cpu: %v", cpu)
	}
	gpu, _ := Apply(PrismML, cpu, map[string]string{"placement": "gpu", "kv_offload": "on"}, known)
	if gpu["gpu_layers"] != "999" || gpu["kv_offload"] != "" {
		t.Fatalf("gpu: %v", gpu)
	}
	bad := []map[string]string{
		{"model": `C:\elsewhere\x.gguf`},
		{"alias": "bad name"},
		{"gpu_layers": "1000"},
		{"n_cpu_moe": "1000"},
		{"cache_key": "q2_k"},
		{"flash_attention": "yes"},
		{"placement": "hybrid", "gpu_layers": "99"},
		{"temperature": "3"},
		{"reasoning_effort": "extreme"},
		{"batch": "64", "ubatch": "128"},
		{"idle_minutes": "0"},
		{"context": "512"},
		{"unknown": "1"},
	}
	for _, changes := range bad {
		if _, err := Apply(PrismML, nil, changes, known); err == nil {
			t.Errorf("%v accepted", changes)
		}
	}
}

func TestApplyOllamaSettingsAndView(t *testing.T) {
	s, err := Apply(Ollama, nil, map[string]string{"context": "32k", "idle_minutes": "45", "placement": "cpu"}, nil)
	if err != nil || s["context"] != "32768" || s["keep_alive"] != "45m" || s["placement"] != "cpu" {
		t.Fatalf("%v %v", s, err)
	}
	if _, err := Apply(Ollama, nil, map[string]string{"gpu_layers": "10"}, nil); err == nil {
		t.Fatal("PrismML setting accepted for Ollama")
	}
	v := View(Ollama, s)
	if v["idle_minutes"] != 45 || v["placement"] != "cpu" || v["keep_alive"] != "45m" {
		t.Fatalf("view %v", v)
	}
	if View(Ollama, nil)["context"] != "(runtime default)" {
		t.Fatal("ollama default context")
	}
}

func TestPrismViewFillsDefaults(t *testing.T) {
	v := View(PrismML, map[string]string{"model": `D:\models\ornith-Q4_K_M.gguf`, "gpu_layers": "30"})
	if v["alias"] != "ornith-Q4_K_M" || v["placement"] != "hybrid" || v["cache_key"] != "f16" || v["kv_offload"] != "on" || v["context"] != "(model default)" || v["n_cpu_moe"] != "0" {
		t.Fatalf("%v", v)
	}
	if v := View(PrismML, map[string]string{}); v["placement"] != "gpu" || v["idle_minutes"] != nil {
		t.Fatalf("defaults: %v", v)
	}
}

func TestParseContext(t *testing.T) {
	for in, want := range map[string]int{"32k": 32768, "128K": 131072, "1m": 1048576, "4096": 4096} {
		if got, err := ParseContext(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"512", "2m", "abc", "-1"} {
		if _, err := ParseContext(in); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
}

func TestFullyQualifiedAcceptsBothSystems(t *testing.T) {
	for _, p := range []string{`C:\x`, `c:/x`, `\\nas\share`, "/opt/x"} {
		if !FullyQualified(p) {
			t.Errorf("%s rejected", p)
		}
	}
	for _, p := range []string{`x\y`, "C:x", ""} {
		if FullyQualified(p) {
			t.Errorf("%s accepted", p)
		}
	}
}
