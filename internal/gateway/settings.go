package gateway

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

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
	PublicOrigin   string    `json:"public_origin"`
	TrustedProxies []string  `json:"trusted_proxies"`
	Keys           []KeyView `json:"keys"`
}

// KeyView is one API key as the panel lists it: name and use, never the key or its hash.
type KeyView struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Created  *time.Time `json:"created"`
	LastUsed *time.Time `json:"last_used"`
	Requests int        `json:"requests"`
	Tokens   int64      `json:"tokens"`
}

// SettingsView returns the API settings without secrets; usage is the journal's count per key.
func SettingsView(c *config.Config, usage map[string]KeyUsage) View {
	v := View{Enabled: c.API.Enabled, PublicURL: c.API.PublicURL, RequireKey: c.API.RequireKey, HasKey: len(c.API.Keys) > 0 || c.API.KeyHash != "",
		QueueDepth: c.API.QueueDepth, QueueTimeoutSeconds: c.API.QueueTimeout(),
		PublicOrigin: c.Panel.PublicOrigin, TrustedProxies: append([]string{}, c.Panel.TrustedProxies...), Keys: []KeyView{}}
	for _, k := range c.API.Keys {
		kv := KeyView{ID: config.KeyID(k.Hash), Name: k.Name}
		if !k.Created.IsZero() {
			created := k.Created
			kv.Created = &created
		}
		if u, ok := usage[kv.ID]; ok {
			last := u.LastUsed
			kv.LastUsed, kv.Requests, kv.Tokens = &last, u.Requests, u.Tokens
		}
		v.Keys = append(v.Keys, kv)
	}
	return v
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
// "add" creates a named key for one more client; "revoke" removes a key at once; "generate"
// replaces every key with a new one (ai key new: when a key may have leaked). It returns a new
// key only for "add" and "generate", to be shown once.
func UpdateSettings(store *config.Store, body map[string]any) (string, error) {
	action, _ := body["action"].(string)
	var key string
	err := store.Update(func(c *config.Config) error {
		switch action {
		case "generate", "add":
			name := "Основной"
			if action == "add" {
				n, _ := body["name"].(string)
				if name = strings.TrimSpace(n); len(body) != 2 {
					return errors.New("Для нового ключа нужно только имя.")
				}
				if err := config.ValidKeyName(name); err != nil {
					return err
				}
				for _, k := range c.API.Keys {
					if k.Name == name {
						return fmt.Errorf("Ключ «%s» уже есть.", name)
					}
				}
				if len(c.API.Keys) >= config.MaxKeys {
					return fmt.Errorf("Не больше %d ключей; отзовите ненужный.", config.MaxKeys)
				}
			} else if len(body) != 1 {
				return errors.New("Для нового ключа другие поля не нужны.")
			}
			var hash string
			key, hash = NewKey()
			k := config.APIKey{ID: config.KeyID(hash), Name: name, Hash: hash, Created: time.Now().UTC()}
			if action == "generate" {
				c.API.Keys, c.API.KeyHash = nil, ""
			}
			c.API.Keys = append(c.API.Keys, k)
			c.API.RequireKey = true
			return nil
		case "revoke":
			id, _ := body["id"].(string)
			if len(body) != 2 || !slices.ContainsFunc(c.API.Keys, func(k config.APIKey) bool { return config.KeyID(k.Hash) == id }) {
				return errors.New("Такого ключа нет.")
			}
			c.API.Keys = slices.DeleteFunc(c.API.Keys, func(k config.APIKey) bool { return config.KeyID(k.Hash) == id })
			if c.API.RequireKey && len(c.API.Keys) == 0 {
				return errors.New("Это последний ключ. Создайте другой или выключите «Требовать API key».")
			}
			return nil
		case "save":
		default:
			return errors.New("Настройки API: save, add, revoke или generate.")
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
				// The panel decodes numbers as json.Number; tests and older callers pass float64.
				n, ok := value.(float64)
				if num, isNum := value.(json.Number); isNum {
					f, err := num.Float64()
					n, ok = f, err == nil
				}
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
		if c.API.RequireKey && len(c.API.Keys) == 0 && c.API.KeyHash == "" {
			return errors.New("Сначала создайте API key.")
		}
		return nil
	})
	return key, err
}
