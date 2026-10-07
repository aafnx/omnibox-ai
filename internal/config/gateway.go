package config

import (
	"errors"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Gateway is gateway.json: the OpenAI-compatible API settings.
type Gateway struct {
	Enabled    bool
	Port       int
	BaseURL    string
	Backend    string
	RequireKey bool
	KeyHash    string // SHA256 hex of the API key; never the key itself
}

var (
	gatewayBackend = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
	sha256Hex      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	v1Suffix       = regexp.MustCompile(`/v1/?$`)
)

// LoadGateway reads gateway.json; a missing file means the API is not set up.
func LoadGateway(path string) (*Gateway, error) {
	data, err := platform.ReadShared(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Gateway{Enabled: false, Port: 11435, Backend: "bonsai"}, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseGateway(data)
}

// ParseGateway validates gateway.json content.
func ParseGateway(data []byte) (*Gateway, error) {
	root, err := parseObject(data)
	if err != nil {
		return nil, err
	}
	g := &Gateway{}
	schema, ok1 := root.integer("schema_version")
	var ok2, ok3 bool
	g.Enabled, ok2 = root.boolean("enabled")
	g.RequireKey, ok3 = root.boolean("require_key")
	port, ok4 := root.integer("port")
	g.Backend, _ = root.str("backend")
	if !ok1 || schema != 1 || !ok2 || !ok3 || !ok4 || port < 1024 || port > 65535 || !gatewayBackend.MatchString(g.Backend) {
		return nil, errors.New("Некорректная gateway.json.")
	}
	g.Port = int(port)
	g.BaseURL, _ = root.str("base_url")
	if !ClientBaseURL(g.BaseURL) {
		return nil, errors.New("OpenAI Base URL: HTTP/HTTPS-адрес с /v1, без пароля/параметров.")
	}
	hash, ok := root.str("key_hash")
	if !ok || (hash != "" && !sha256Hex.MatchString(hash)) || (g.RequireKey && hash == "") {
		return nil, errors.New("Некорректный hash API key.")
	}
	g.KeyHash = hash
	return g, nil
}

// ClientBaseURL accepts the address clients use for the API: http(s), ends in /v1.
func ClientBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" {
		return false
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	return v1Suffix.MatchString(path) && !strings.Contains(u.Hostname(), ":")
}
