package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Backend is one engine entry of config.json.
type Backend struct {
	Name            string
	Type            string // prism or ollama
	Service         string
	BaseURL         string
	ServiceConfig   string
	LogDirectory    string
	LogPrefix       string
	Executable      string
	ModelsDirectory string
	ActiveModel     string
}

// Config is config.json, schema 1. Backends keep their file order.
type Config struct {
	DefaultBackend string
	Exclusive      bool
	StartupTimeout int
	StopTimeout    int
	Backends       []Backend
}

// Backend returns the named engine, or the default one for an empty name.
func (c *Config) Backend(name string) (*Backend, bool) {
	if name == "" {
		name = c.DefaultBackend
	}
	for i := range c.Backends {
		if c.Backends[i].Name == name {
			return &c.Backends[i], true
		}
	}
	return nil, false
}

var (
	backendName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	serviceName = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
	modelName   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_./:-]*$`)
)

// LoadConfig reads and validates config.json.
func LoadConfig(path string) (*Config, error) {
	data, err := platform.ReadShared(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("Нет конфигурации: %s. Запусти install.ps1 или создай config.json из config.example.json.", path)
		}
		return nil, err
	}
	return ParseConfig(data)
}

// ParseConfig validates config.json content.
func ParseConfig(data []byte) (*Config, error) {
	root, err := parseObject(data)
	if err != nil {
		return nil, err
	}
	if v, ok := root.integer("schema_version"); !ok || v != 1 {
		return nil, errors.New("Неподдерживаемая schema_version; ожидается 1.")
	}
	backends, ok := root.child("backends")
	if !ok || len(backends.keys) == 0 {
		return nil, errors.New("backends должен содержать хотя бы один сервер.")
	}
	cfg := &Config{}
	cfg.DefaultBackend, _ = root.str("default_backend")
	if !backends.has(cfg.DefaultBackend) {
		return nil, errors.New("default_backend не найден в backends.")
	}
	if cfg.Exclusive, ok = root.boolean("exclusive"); !ok {
		return nil, errors.New("exclusive должен быть true или false.")
	}
	for _, key := range []string{"startup_timeout_seconds", "stop_timeout_seconds"} {
		v, ok := root.integer(key)
		if !ok {
			return nil, fmt.Errorf("%s должен быть целым числом.", key)
		}
		if v < 1 || v > 3600 {
			return nil, fmt.Errorf("%s должен быть от 1 до 3600.", key)
		}
		if key == "startup_timeout_seconds" {
			cfg.StartupTimeout = int(v)
		} else {
			cfg.StopTimeout = int(v)
		}
	}
	services := map[string]bool{}
	for _, name := range backends.keys {
		if !backendName.MatchString(name) {
			return nil, fmt.Errorf("Некорректное имя сервера: %s", name)
		}
		entry, ok := backends.child(name)
		if !ok {
			return nil, fmt.Errorf("Некорректная конфигурация %s.", name)
		}
		b, err := parseBackend(name, entry)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(b.Service)
		if services[key] {
			return nil, errors.New("Разные серверы не могут управлять одной службой.")
		}
		services[key] = true
		cfg.Backends = append(cfg.Backends, b)
	}
	return cfg, nil
}

func parseBackend(name string, e *object) (Backend, error) {
	b := Backend{Name: name}
	b.Type, _ = e.str("type")
	if b.Type != "prism" && b.Type != "ollama" {
		return b, fmt.Errorf("Неизвестный движок: %s", b.Type)
	}
	b.Service, _ = e.str("service")
	if !serviceName.MatchString(b.Service) {
		return b, fmt.Errorf("Некорректное имя службы: %s", name)
	}
	b.BaseURL, _ = e.str("base_url")
	if !loopbackRoot(b.BaseURL) {
		return b, fmt.Errorf("base_url для %s должен быть локальным HTTP-адресом без пути, например http://127.0.0.1:18080.", name)
	}
	for _, key := range []string{"log_directory", "log_prefix", "service_config"} {
		if v, _ := e.str(key); strings.TrimSpace(v) == "" {
			return b, fmt.Errorf("Не задан %s для %s.", key, name)
		}
	}
	b.LogDirectory, _ = e.str("log_directory")
	b.LogPrefix, _ = e.str("log_prefix")
	b.ServiceConfig, _ = e.str("service_config")
	if !serviceName.MatchString(b.LogPrefix) {
		return b, fmt.Errorf("Некорректный log_prefix для %s.", name)
	}
	b.Executable, _ = e.str("executable")
	if b.Type == "ollama" && strings.TrimSpace(b.Executable) == "" {
		return b, fmt.Errorf("Не задан executable для %s.", name)
	}
	if e.has("models_directory") {
		v, ok := e.str("models_directory")
		if !ok || !fullyQualified(v) {
			return b, fmt.Errorf("models_directory для %s должен быть полным путём.", name)
		}
		b.ModelsDirectory = v
	}
	if e.has("active_model") {
		v, ok := e.str("active_model")
		if !ok || len(v) > 200 || !modelName.MatchString(v) || strings.Contains(v, "..") {
			return b, fmt.Errorf("Некорректное имя выбранной модели: %s", name)
		}
		b.ActiveModel = v
	}
	return b, nil
}

// loopbackRoot accepts http://<loopback host>[:port] with no path, query, fragment or user.
func loopbackRoot(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// fullyQualified accepts absolute paths of either OS, so files written on Windows
// validate in tests on Linux and the reverse.
func fullyQualified(p string) bool {
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return true
	}
	return len(p) >= 3 && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) && p[1] == ':' && (p[2] == '\\' || p[2] == '/') ||
		strings.HasPrefix(p, `\\`)
}
