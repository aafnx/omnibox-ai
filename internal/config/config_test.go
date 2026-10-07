package config

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func exampleConfig(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestExampleConfigIsValidAndKeepsBackendOrder(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseConfig(append([]byte("\xef\xbb\xbf"), data...))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Backends) != 2 || cfg.Backends[0].Name != "bonsai" || cfg.Backends[1].Name != "ollama" {
		t.Fatalf("backends: %+v", cfg.Backends)
	}
	if b, ok := cfg.Backend(""); !ok || b.Type != "prism" || b.BaseURL != "http://127.0.0.1:18080" {
		t.Fatalf("default backend: %+v", b)
	}
	if cfg.StartupTimeout != 180 || cfg.StopTimeout != 30 || !cfg.Exclusive {
		t.Fatalf("settings: %+v", cfg)
	}
}

func TestConfigRejectsInvalidValuesWithKnownMessages(t *testing.T) {
	cases := []struct {
		name   string
		change func(c map[string]any)
		want   string
	}{
		{"schema", func(c map[string]any) { c["schema_version"] = 2 }, "schema_version"},
		{"default", func(c map[string]any) { c["default_backend"] = "missing" }, "default_backend не найден"},
		{"exclusive", func(c map[string]any) { c["exclusive"] = "yes" }, "exclusive должен быть"},
		{"timeout type", func(c map[string]any) { c["startup_timeout_seconds"] = 1.5 }, "целым числом"},
		{"timeout range", func(c map[string]any) { c["stop_timeout_seconds"] = 0 }, "от 1 до 3600"},
		{"engine type", func(c map[string]any) { backend(c, "bonsai")["type"] = "vllm" }, "Неизвестный движок: vllm"},
		{"service", func(c map[string]any) { backend(c, "bonsai")["service"] = "a b" }, "Некорректное имя службы"},
		{"shared service", func(c map[string]any) { backend(c, "ollama")["service"] = "BONSAI" }, "одной службой"},
		{"remote url", func(c map[string]any) { backend(c, "bonsai")["base_url"] = "http://192.168.1.2:18080" }, "локальным HTTP-адресом"},
		{"url path", func(c map[string]any) { backend(c, "bonsai")["base_url"] = "http://127.0.0.1:18080/v1" }, "локальным HTTP-адресом"},
		{"log prefix", func(c map[string]any) { backend(c, "bonsai")["log_prefix"] = "../x" }, "log_prefix"},
		{"no profile", func(c map[string]any) { delete(backend(c, "bonsai"), "service_config") }, "Не задан service_config"},
		{"ollama exe", func(c map[string]any) { delete(backend(c, "ollama"), "executable") }, "Не задан executable"},
		{"relative models", func(c map[string]any) { backend(c, "bonsai")["models_directory"] = `relative\models` }, "models_directory"},
		{"active model", func(c map[string]any) { backend(c, "ollama")["active_model"] = "../etc" }, "выбранной модели"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := exampleConfig(t)
			tc.change(c)
			_, err := ParseConfig(encode(t, c))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func backend(c map[string]any, name string) map[string]any {
	return c["backends"].(map[string]any)[name].(map[string]any)
}

func TestConfigAcceptsWindowsModelDirectoryAndModelName(t *testing.T) {
	c := exampleConfig(t)
	backend(c, "bonsai")["models_directory"] = `C:\tools\ai\models`
	backend(c, "ollama")["active_model"] = "qwen3:8b"
	cfg, err := ParseConfig(encode(t, c))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backends[0].ModelsDirectory != `C:\tools\ai\models` || cfg.Backends[1].ActiveModel != "qwen3:8b" {
		t.Fatalf("%+v", cfg.Backends)
	}
}

func TestMissingConfigNamesTheFile(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "config.json"))
	if err == nil || !strings.Contains(err.Error(), "Нет конфигурации") {
		t.Fatalf("got %v", err)
	}
}

func panelJSON(t *testing.T, change func(p map[string]any)) []byte {
	t.Helper()
	b64 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	p := map[string]any{"schema_version": 1, "port": 18090, "allowed_hosts": []any{"192.168.1.77", "omnibox"},
		"credential": map[string]any{"salt": b64, "hash": b64, "iterations": 210000}, "ssh_target": "user@omnibox"}
	if change != nil {
		change(p)
	}
	return encode(t, p)
}

func TestPanelConfig(t *testing.T) {
	p, err := ParsePanel(panelJSON(t, nil))
	if err != nil || p.Port != 18090 || len(p.AllowedHosts) != 2 || p.PublicOrigin != "" {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = ParsePanel(panelJSON(t, func(p map[string]any) {
		p["public_origin"] = "https://LLM.fnx.red/"
		p["trusted_proxy_addresses"] = []any{"192.168.1.10"}
	}))
	if err != nil || p.PublicOrigin != "https://llm.fnx.red" || len(p.TrustedProxyAddresses) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	bad := []struct {
		change func(p map[string]any)
		want   string
	}{
		{func(p map[string]any) { p["port"] = 80 }, "порт"},
		{func(p map[string]any) { p["allowed_hosts"] = []any{"host:80"} }, "allowed_hosts"},
		{func(p map[string]any) { p["credential"].(map[string]any)["iterations"] = 1000 }, "hash пароля"},
		{func(p map[string]any) { p["ssh_target"] = "-oProxyCommand=x" }, "ssh_target"},
		{func(p map[string]any) { p["public_origin"] = "https://llm.fnx.red" }, "вместе"},
		{func(p map[string]any) {
			p["public_origin"] = "http://llm"
			p["trusted_proxy_addresses"] = []any{"1.2.3.4"}
		}, "HTTPS-адрес"},
		{func(p map[string]any) { p["trusted_proxy_addresses"] = []any{"10.0.0.0/8"} }, "без CIDR"},
	}
	for _, tc := range bad {
		if _, err := ParsePanel(panelJSON(t, tc.change)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("want %q, got %v", tc.want, err)
		}
	}
}

func TestGatewayConfig(t *testing.T) {
	g, err := LoadGateway(filepath.Join(t.TempDir(), "gateway.json"))
	if err != nil || g.Enabled || g.Port != 11435 {
		t.Fatalf("missing file: %+v %v", g, err)
	}
	hash := strings.Repeat("a", 64)
	good := map[string]any{"schema_version": 1, "enabled": true, "port": 18090, "base_url": "https://llm.fnx.red/v1", "backend": "bonsai", "require_key": true, "key_hash": hash}
	if g, err := ParseGateway(encode(t, good)); err != nil || g.KeyHash != hash || !g.RequireKey {
		t.Fatalf("%+v %v", g, err)
	}
	for key, value := range map[string]any{"base_url": "https://llm.fnx.red/", "key_hash": "", "port": 99, "backend": "a b"} {
		bad := map[string]any{}
		for k, v := range good {
			bad[k] = v
		}
		bad[key] = value
		if _, err := ParseGateway(encode(t, bad)); err == nil {
			t.Fatalf("%s=%v accepted", key, value)
		}
	}
}

func TestStorageFallsBackToDefaultsPerValue(t *testing.T) {
	dir := t.TempDir()
	if s := LoadStorage(dir); s != DefaultStorage() {
		t.Fatalf("defaults: %+v", s)
	}
	os.WriteFile(filepath.Join(dir, "storage.json"), []byte(`{"schema_version":1,"log_retention_days":7,"log_warn_mb":5,"request_log":"content"}`), 0o600)
	s := LoadStorage(dir)
	if s.LogRetentionDays != 7 || s.LogWarnMB != 200 || s.RequestLog != "content" {
		t.Fatalf("%+v", s)
	}
	os.WriteFile(filepath.Join(dir, "storage.json"), []byte(`not json`), 0o600)
	if s := LoadStorage(dir); s != DefaultStorage() {
		t.Fatalf("damaged file: %+v", s)
	}
}
