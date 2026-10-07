package config

import (
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Credential is the PBKDF2-SHA256 hash of the panel password.
type Credential struct {
	Salt       []byte
	Hash       []byte
	Iterations int
	// Raw values as stored, used to bind saved sessions to this password.
	SaltText, HashText string
}

// Panel is panel.json.
type Panel struct {
	Port                  int
	AllowedHosts          []string
	Credential            Credential
	SSHTarget             string
	PublicOrigin          string // scheme://authority, empty without a reverse proxy
	TrustedProxyAddresses []net.IP
}

var (
	hostPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)
	sshPattern  = regexp.MustCompile(`^[a-zA-Z0-9_.-]+@[a-zA-Z0-9_.-]+$`)
)

// LoadPanel reads and validates panel.json.
func LoadPanel(path string) (*Panel, error) {
	data, err := platform.ReadShared(path)
	if err != nil {
		return nil, err
	}
	return ParsePanel(data)
}

// ParsePanel validates panel.json content.
func ParsePanel(data []byte) (*Panel, error) {
	root, err := parseObject(data)
	if err != nil {
		return nil, err
	}
	p := &Panel{}
	schema, ok1 := root.integer("schema_version")
	port, ok2 := root.integer("port")
	if !ok1 || schema != 1 || !ok2 || port < 1024 || port > 65535 {
		return nil, errors.New("Некорректная конфигурация панели или порт.")
	}
	p.Port = int(port)
	hosts, ok := root.strings("allowed_hosts")
	if !ok || len(hosts) < 1 || len(hosts) > 20 {
		return nil, errors.New("Нужны allowed_hosts панели.")
	}
	for _, h := range hosts {
		if !hostPattern.MatchString(h) || len(h) > 253 {
			return nil, errors.New("allowed_hosts: IPv4 или DNS-имя без порта/пути.")
		}
	}
	p.AllowedHosts = hosts
	if p.Credential, err = parseCredential(root); err != nil {
		return nil, err
	}
	p.SSHTarget, _ = root.str("ssh_target")
	if !sshPattern.MatchString(p.SSHTarget) {
		return nil, errors.New("ssh_target: user@host без параметров.")
	}
	if err := parseProxy(root, p); err != nil {
		return nil, err
	}
	return p, nil
}

func parseCredential(root *object) (Credential, error) {
	bad := errors.New("Некорректный hash пароля панели.")
	c, ok := root.child("credential")
	if !ok {
		return Credential{}, bad
	}
	iterations, ok := c.integer("iterations")
	saltText, _ := c.str("salt")
	hashText, _ := c.str("hash")
	salt, err1 := base64.StdEncoding.DecodeString(saltText)
	hash, err2 := base64.StdEncoding.DecodeString(hashText)
	if !ok || iterations < 210000 || iterations > 1000000 || err1 != nil || err2 != nil || len(salt) != 32 || len(hash) != 32 {
		return Credential{}, bad
	}
	return Credential{Salt: salt, Hash: hash, Iterations: int(iterations), SaltText: saltText, HashText: hashText}, nil
}

func parseProxy(root *object, p *Panel) error {
	origin := ""
	if root.has("public_origin") {
		v, ok := root.str("public_origin")
		if !ok {
			return errors.New("public_origin: нужен HTTPS origin.")
		}
		origin = v
	}
	var addresses []string
	if root.has("trusted_proxy_addresses") {
		list, ok := root.values["trusted_proxy_addresses"].([]any)
		if !ok || len(list) > 20 {
			return errors.New("trusted_proxy_addresses: список IP NAS, до 20 адресов.")
		}
		for _, item := range list {
			s, ok := item.(string)
			if !ok || net.ParseIP(s) == nil || strings.Contains(s, "/") {
				return errors.New("trusted_proxy_addresses: только конкретные IP, без CIDR/wildcard.")
			}
			addresses = append(addresses, s)
		}
	}
	if origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Host == "" || !hostPattern.MatchString(u.Hostname()) {
			return errors.New("public_origin: HTTPS-адрес без пути, например https://ai.example.com.")
		}
		origin = "https://" + strings.ToLower(u.Host)
	}
	if (origin != "") != (len(addresses) > 0) {
		return errors.New("Reverse proxy: укажи public_origin и IP NAS вместе или оставь оба пустыми.")
	}
	p.PublicOrigin = origin
	for _, a := range addresses {
		p.TrustedProxyAddresses = append(p.TrustedProxyAddresses, net.ParseIP(a))
	}
	return nil
}
