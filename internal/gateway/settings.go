package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/aafnx/omnibox-ai/internal/config"
)

// View is the API settings the panel shows; the key itself is never shown again.
type View struct {
	Enabled             bool   `json:"enabled"`
	PublicURL           string `json:"base_url"`
	RequireKey          bool   `json:"require_key"`
	HasKey              bool   `json:"has_key"`
	QueueDepth          int    `json:"queue_depth"`
	QueueTimeoutSeconds int    `json:"queue_timeout_seconds"`
	// The NAS reverse proxy: its HTTPS address and the addresses it connects from.
	PublicOrigin   string   `json:"public_origin"`
	TrustedProxies []string `json:"trusted_proxies"`
}

// SettingsView returns the API settings without secrets.
func SettingsView(c *config.Config) View {
	return View{Enabled: c.API.Enabled, PublicURL: c.API.PublicURL, RequireKey: c.API.RequireKey, HasKey: c.API.KeyHash != "",
		QueueDepth: c.API.QueueDepth, QueueTimeoutSeconds: c.API.QueueTimeout(),
		PublicOrigin: c.Panel.PublicOrigin, TrustedProxies: append([]string{}, c.Panel.TrustedProxies...)}
}

// NewKey returns a new API key and its SHA256 hash; only the hash is stored.
func NewKey() (key, hash string) {
	var b [32]byte
	_, _ = rand.Read(b[:])
	key = "obai-" + hex.EncodeToString(b[:])
	sum := sha256.Sum256([]byte(key))
	return key, hex.EncodeToString(sum[:])
}

// UpdateSettings applies the panel form: "save" changes the address, key mode, queue and NAS;
// "generate" replaces the key, which revokes the previous one at once. It returns the new key
// only for "generate", to be shown once.
func UpdateSettings(store *config.Store, body map[string]any) (string, error) {
	action, _ := body["action"].(string)
	var key string
	err := store.Update(func(c *config.Config) error {
		switch action {
		case "generate":
			if len(body) != 1 {
				return errors.New("Для нового ключа другие поля не нужны.")
			}
			var hash string
			key, hash = NewKey()
			c.API.KeyHash, c.API.RequireKey = hash, true
			return nil
		case "save":
		default:
			return errors.New("Настройки API: save или generate.")
		}
		for name, value := range body {
			switch name {
			case "action":
			case "enabled", "require_key":
				b, ok := value.(bool)
				if !ok {
					return errors.New(name + ": true или false.")
				}
				if name == "enabled" {
					c.API.Enabled = b
				} else {
					c.API.RequireKey = b
				}
			case "base_url":
				s, ok := value.(string)
				s = strings.TrimRight(s, "/")
				if !ok || (s != "" && !config.ClientBaseURL(s)) {
					return errors.New("Адрес API для клиентов: HTTP/HTTPS с /v1, без пароля и параметров.")
				}
				c.API.PublicURL = s
			case "public_origin":
				s, ok := value.(string)
				if s = strings.TrimSpace(s); !ok {
					return errors.New("public_origin: строка.")
				}
				if s != "" && !strings.Contains(s, "://") {
					s = "https://" + s
				}
				c.Panel.PublicOrigin = s
			case "trusted_proxies":
				list, ok := value.([]any)
				if !ok {
					return errors.New("trusted_proxies: список IP-адресов.")
				}
				c.Panel.TrustedProxies = nil
				for _, item := range list {
					ip, ok := item.(string)
					if !ok {
						return errors.New("trusted_proxies: список IP-адресов.")
					}
					if ip = strings.TrimSpace(ip); ip != "" {
						c.Panel.TrustedProxies = append(c.Panel.TrustedProxies, ip)
					}
				}
			case "queue_depth", "queue_timeout_seconds":
				n, ok := value.(float64)
				if !ok || n != float64(int(n)) {
					return errors.New("Очередь: целые числа.")
				}
				if name == "queue_depth" {
					c.API.QueueDepth = int(n)
				} else {
					c.API.QueueTimeoutSeconds = int(n)
				}
			default:
				return errors.New("Неизвестная настройка API: " + name)
			}
		}
		if c.API.RequireKey && c.API.KeyHash == "" {
			return errors.New("Сначала создайте API key.")
		}
		return nil
	})
	return key, err
}
