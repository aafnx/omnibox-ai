// Package config is the single config.json of the Go version: panel, API, storage and the
// engines with their launch profiles. Only the service writes it, atomically.
package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Engine names; the engine type is the name.
const (
	PrismML = "prismml"
	Ollama  = "ollama"
)

// EngineNames lists the supported engines in display order.
var EngineNames = []string{PrismML, Ollama}

// Label is the engine name shown to people.
func Label(name string) string {
	switch name {
	case PrismML:
		return "PrismML"
	case Ollama:
		return "Ollama"
	}
	return name
}

// ServiceName is the operating system service that runs the engine.
func ServiceName(engine string) string { return "omnibox-" + engine }

// PanelService is the service of the panel itself.
const PanelService = "omnibox"

// UpdateService is the temporary service that installs an update started from the panel: the
// installer restarts the panel, so it cannot run inside it.
const UpdateService = "omnibox-update"

// Default ports: the panel and /v1 share one port for the NAS; engines listen on loopback next to it.
const (
	DefaultPanelPort = 8800
)

// DefaultEnginePort is where a newly installed engine listens on loopback.
func DefaultEnginePort(engine string) int {
	if engine == Ollama {
		return 8802
	}
	return 8801
}

// Config is config.json.
type Config struct {
	SchemaVersion int                `json:"schema_version"`
	Panel         Panel              `json:"panel"`
	API           API                `json:"api"`
	Storage       Storage            `json:"storage"`
	Engines       map[string]*Engine `json:"engines"`
	// Chat, Cloud, Models and Favorites appear once the owner changes them in the panel.
	Chat   *Chat                  `json:"chat,omitempty"`
	Cloud  *Cloud                 `json:"cloud,omitempty"`
	Models map[string]*ModelPrefs `json:"models,omitempty"`
	// Favorites are model keys (prismml:<file>, ollama:<name>, cloud:<id>) shown first.
	Favorites []string `json:"favorites,omitempty"`
}

// Panel holds the web panel's address and password hash.
type Panel struct {
	Port           int      `json:"port"`
	Hosts          []string `json:"hosts"`
	PublicOrigin   string   `json:"public_origin,omitempty"`
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
	Password       Password `json:"password"`
	SSHTarget      string   `json:"ssh_target,omitempty"`
	// StartEngine is started when the computer boots (if OmniBox starts with it) and by
	// "ai on"; empty means no engine starts by itself. Off by default.
	StartEngine string `json:"start_engine,omitempty"`
	// StartModel is the local model loaded once after the computer boots (prismml:<file> or
	// ollama:<name>); it replaces StartEngine when set. Off by default.
	StartModel string `json:"start_model,omitempty"`
}

// Password is a PBKDF2-SHA256 hash (base64), never the password itself.
type Password struct {
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
}

// Decode returns the raw salt and hash.
func (p Password) Decode() (salt, hash []byte, err error) {
	salt, err1 := base64.StdEncoding.DecodeString(p.Salt)
	hash, err2 := base64.StdEncoding.DecodeString(p.Hash)
	if err1 != nil || err2 != nil || len(salt) != 32 || len(hash) != 32 || p.Iterations < 210000 || p.Iterations > 1000000 {
		return nil, nil, errors.New("Некорректный hash пароля панели.")
	}
	return salt, hash, nil
}

// API is the OpenAI-compatible endpoint served at /v1 on the panel port.
type API struct {
	Enabled    bool   `json:"enabled"`
	PublicURL  string `json:"public_url,omitempty"`
	RequireKey bool   `json:"require_key"`
	// KeyHash is the single key of 0.18; it becomes the key named "Основной" on load.
	KeyHash string `json:"key_hash,omitempty"`
	// Keys are the clients' API keys: names and SHA256 hashes, never the keys.
	Keys   []APIKey `json:"keys,omitempty"`
	Engine string   `json:"engine"`
	// QueueDepth is how many requests wait for the running generation; 0 answers 409 at once.
	QueueDepth int `json:"queue_depth"`
	// QueueTimeoutSeconds bounds the wait in the queue; 0 means the default of 60 s.
	QueueTimeoutSeconds int `json:"queue_timeout_seconds,omitempty"`
}

// APIKey is one client's key, e.g. Jan, Pi or Open WebUI.
type APIKey struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Hash    string    `json:"hash"`
	Created time.Time `json:"created,omitempty"`
}

// MaxKeys is how many API keys the panel keeps.
const MaxKeys = 20

var keyName = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._+-]{0,39}$`)

// KeyID is the short id of a key: the start of its hash, stable and safe to show.
func KeyID(hash string) string { return hash[:12] }

// ValidKeyName checks a key's name.
func ValidKeyName(name string) error {
	if !keyName.MatchString(name) {
		return errors.New("Имя ключа: буквы, цифры, пробел, точка, дефис, до 40 символов.")
	}
	return nil
}

// QueueTimeout is the queue wait in seconds.
func (a API) QueueTimeout() int {
	if a.QueueTimeoutSeconds == 0 {
		return 60
	}
	return a.QueueTimeoutSeconds
}

// Storage controls log retention and the request journal.
type Storage struct {
	LogRetentionDays int `json:"log_retention_days"` // 0 keeps logs forever
	// LogRetentionHours, when set, replaces the days: 6 hours keeps only today's work.
	LogRetentionHours int    `json:"log_retention_hours,omitempty"`
	LogWarnMB         int    `json:"log_warn_mb"`
	RequestLog        string `json:"request_log"` // metadata or content
}

// Default returns a configuration for a new installation; the installer fills in the rest.
func Default() *Config {
	return &Config{
		SchemaVersion: 1,
		Panel:         Panel{Port: DefaultPanelPort},
		API:           API{Engine: PrismML, QueueDepth: 4},
		Storage:       Storage{LogRetentionDays: 30, LogWarnMB: 200, RequestLog: "metadata"},
		Engines:       map[string]*Engine{},
	}
}

// Load reads and validates config.json.
func Load(path string) (*Config, error) {
	data, err := platform.ReadShared(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("Нет настроек: %s. Установите OmniBox AI заново.", path)
	}
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes and validates config.json content; unknown fields are rejected.
func Parse(data []byte) (*Config, error) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("Некорректный config.json: %v", err)
	}
	if c.Engines == nil {
		c.Engines = map[string]*Engine{}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save validates and writes config.json atomically.
func (c *Config) Save(path string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return platform.WriteAtomic(path, append(data, '\n'), 0o600)
}

// Clone returns a deep copy, so a change can be prepared and validated before it is saved.
func (c *Config) Clone() *Config {
	data, _ := json.Marshal(c)
	var out Config
	_ = json.Unmarshal(data, &out)
	if out.Engines == nil {
		out.Engines = map[string]*Engine{}
	}
	return &out
}

var (
	hostPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)
	sshPattern  = regexp.MustCompile(`^[a-zA-Z0-9_.-]+@[a-zA-Z0-9_.-]+$`)
	sha256Hex   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	v1Suffix    = regexp.MustCompile(`/v1/?$`)
)

// Validate checks every section; messages are shown to the owner as is.
func (c *Config) Validate() error {
	if c.SchemaVersion != 1 {
		return errors.New("Неподдерживаемая schema_version; ожидается 1.")
	}
	if err := c.Panel.validate(); err != nil {
		return err
	}
	if err := c.API.validate(c); err != nil {
		return err
	}
	if err := c.Storage.validate(); err != nil {
		return err
	}
	if c.Chat != nil {
		if err := c.Chat.validate(); err != nil {
			return err
		}
	}
	if c.Cloud != nil {
		if err := c.Cloud.validate(); err != nil {
			return err
		}
	}
	if err := c.validateModels(); err != nil {
		return err
	}
	ports := map[int]string{c.Panel.Port: "панель"}
	for name, e := range c.Engines {
		if name != PrismML && name != Ollama {
			return fmt.Errorf("Неизвестный движок: %s", name)
		}
		if e == nil {
			return fmt.Errorf("Пустые настройки движка %s.", name)
		}
		if err := e.validate(name); err != nil {
			return err
		}
		if other, ok := ports[e.Port]; ok {
			return fmt.Errorf("Порт %d уже занят: %s.", e.Port, other)
		}
		ports[e.Port] = Label(name)
	}
	return nil
}

func (p *Panel) validate() error {
	if p.Port < 1024 || p.Port > 65535 {
		return errors.New("Порт панели: от 1024 до 65535.")
	}
	if len(p.Hosts) < 1 || len(p.Hosts) > 20 {
		return errors.New("Нужны адреса панели (hosts), от 1 до 20.")
	}
	for _, h := range p.Hosts {
		if err := ValidHost(h); err != nil {
			return err
		}
	}
	if _, _, err := p.Password.Decode(); err != nil {
		return err
	}
	if p.SSHTarget != "" && !sshPattern.MatchString(p.SSHTarget) {
		return errors.New("ssh_target: user@host без параметров.")
	}
	if p.StartEngine != "" && p.StartEngine != PrismML && p.StartEngine != Ollama {
		return errors.New("start_engine: prismml, ollama или пусто.")
	}
	if len(p.TrustedProxies) > 20 {
		return errors.New("trusted_proxies: до 20 адресов NAS.")
	}
	for _, a := range p.TrustedProxies {
		if net.ParseIP(a) == nil {
			return errors.New("trusted_proxies: только конкретные IP, без CIDR и масок.")
		}
	}
	if p.PublicOrigin != "" {
		origin, err := PublicOrigin(p.PublicOrigin)
		if err != nil {
			return err
		}
		p.PublicOrigin = origin
	}
	if (p.PublicOrigin != "") != (len(p.TrustedProxies) > 0) {
		return errors.New("Reverse proxy: укажите public_origin и IP NAS вместе или оставьте оба пустыми.")
	}
	return nil
}

// TrustedProxyIPs returns the NAS addresses whose X-Forwarded-* headers are trusted.
func (p *Panel) TrustedProxyIPs() []net.IP {
	out := make([]net.IP, 0, len(p.TrustedProxies))
	for _, a := range p.TrustedProxies {
		out = append(out, net.ParseIP(a))
	}
	return out
}

func (a *API) validate(c *Config) error {
	if a.Engine != PrismML && a.Engine != Ollama {
		return errors.New("api.engine: prismml или ollama.")
	}
	if a.PublicURL != "" && !ClientBaseURL(a.PublicURL) {
		return errors.New("Адрес API для клиентов: HTTP/HTTPS с /v1, без пароля и параметров.")
	}
	if a.KeyHash != "" {
		if !sha256Hex.MatchString(a.KeyHash) {
			return errors.New("Некорректный hash API key.")
		}
		if !slices.ContainsFunc(a.Keys, func(k APIKey) bool { return k.Hash == a.KeyHash }) {
			a.Keys = append([]APIKey{{ID: KeyID(a.KeyHash), Name: "Основной", Hash: a.KeyHash}}, a.Keys...)
		}
		a.KeyHash = ""
	}
	if len(a.Keys) > MaxKeys {
		return fmt.Errorf("Не больше %d API-ключей.", MaxKeys)
	}
	seen := map[string]bool{}
	for i := range a.Keys {
		k := &a.Keys[i]
		if !sha256Hex.MatchString(k.Hash) || ValidKeyName(k.Name) != nil || seen[k.Hash] {
			return errors.New("Некорректный API key в настройках.")
		}
		seen[k.Hash], k.ID = true, KeyID(k.Hash)
	}
	if a.RequireKey && len(a.Keys) == 0 {
		return errors.New("Сначала создайте API key.")
	}
	if a.QueueDepth < 0 || a.QueueDepth > 16 {
		return errors.New("Очередь запросов: от 0 до 16.")
	}
	if a.QueueTimeoutSeconds < 0 || a.QueueTimeoutSeconds > 600 {
		return errors.New("Ожидание в очереди: от 1 до 600 секунд.")
	}
	return nil
}

// ValidHost accepts a panel address: an IPv4 address or a DNS name, without port or path.
func ValidHost(h string) error {
	if !hostPattern.MatchString(h) || len(h) > 253 {
		return errors.New("hosts: IPv4 или DNS-имя без порта и пути.")
	}
	return nil
}

// PublicOrigin checks the HTTPS address of the reverse proxy and returns it normalized.
func PublicOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || !hostPattern.MatchString(u.Hostname()) {
		return "", errors.New("public_origin: HTTPS-адрес без пути, например https://ai.example.com.")
	}
	return "https://" + strings.ToLower(u.Host), nil
}

// ClientBaseURL accepts the address clients use for the API: http(s), ends in /v1.
func ClientBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" {
		return false
	}
	return v1Suffix.MatchString(u.Path)
}

// LogRetention is how long logs are kept; 0 keeps them.
func (s Storage) LogRetention() time.Duration {
	if s.LogRetentionHours > 0 {
		return time.Duration(s.LogRetentionHours) * time.Hour
	}
	return time.Duration(s.LogRetentionDays) * 24 * time.Hour
}

func (s *Storage) validate() error {
	if s.LogRetentionDays < 0 || s.LogRetentionDays > 365 {
		return errors.New("Срок хранения журналов: от 0 (не удалять) до 365 дней.")
	}
	if s.LogRetentionHours < 0 || s.LogRetentionHours > 8760 {
		return errors.New("Срок хранения журналов: от 1 часа до 8760 часов (год).")
	}
	if s.LogWarnMB < 10 || s.LogWarnMB > 100000 {
		return errors.New("Порог объёма журналов: от 10 до 100000 МБ.")
	}
	if s.RequestLog != "metadata" && s.RequestLog != "content" {
		return errors.New("Журнал запросов: metadata или content.")
	}
	return nil
}
