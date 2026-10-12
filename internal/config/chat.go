package config

import (
	"errors"
	"net/url"
	"slices"
	"strings"
)

// Chat holds the panel's own chat: how long chats are kept and the web search through SearXNG.
// Absent from config.json until the owner changes something; DefaultChat applies then.
type Chat struct {
	// RetentionHours is how long a new chat is kept; 0 keeps chats until they are deleted.
	RetentionHours int `json:"retention_hours"`
	// SearchURL is the SearXNG instance (its JSON format must be enabled); empty means no search.
	SearchURL      string `json:"search_url,omitempty"`
	SearchResults  int    `json:"search_results"`
	SearchLanguage string `json:"search_language,omitempty"` // "", ru or en
	// SearchDefault turns web search on in new chats.
	SearchDefault bool `json:"search_default"`
	// NoReadPages keeps the search to SearXNG's snippets: the pages found are not opened.
	NoReadPages bool `json:"no_read_pages,omitempty"`
	// SiteIcons shows the sources' own icons, which the browser loads from each site; off, the
	// sources get letter badges and the panel loads nothing from outside.
	SiteIcons bool `json:"site_icons,omitempty"`
	// FollowUps suggests a few questions to ask next under the last answer; a chat may turn
	// them on or off for itself.
	FollowUps bool `json:"follow_ups,omitempty"`
	// NoAutoTitle keeps a new chat's title from its first message: the model does not name it.
	NoAutoTitle bool `json:"no_auto_title,omitempty"`
	// FollowUpModel writes the suggestions and titles: empty is the chat's own model, otherwise an
	// OpenRouter model (cloud:<id>) — never another local model, which would have to be started.
	FollowUpModel string `json:"follow_up_model,omitempty"`
}

// DefaultChat keeps chats for 30 days and searches with five sources.
func DefaultChat() Chat {
	return Chat{RetentionHours: 30 * 24, SearchResults: 5, SearchDefault: true}
}

// ChatSettings returns the chat settings, defaults filled in.
func (c *Config) ChatSettings() Chat {
	if c.Chat == nil {
		return DefaultChat()
	}
	return *c.Chat
}

// Cloud holds the OpenRouter settings. The key itself is never in config.json: it is kept
// encrypted in run/openrouter-key.bin.
type Cloud struct {
	// ExposeAPI shows the favourite OpenRouter models to the /v1 clients; off by default, since
	// their requests are paid from the owner's key.
	ExposeAPI bool `json:"expose_api"`
	// LowBalanceUSD warns in the panel when the balance is lower; 0 turns the warning off.
	LowBalanceUSD float64 `json:"low_balance_usd"`
}

// DefaultCloud warns below $2 and keeps OpenRouter out of /v1.
func DefaultCloud() Cloud { return Cloud{LowBalanceUSD: 2} }

// CloudSettings returns the OpenRouter settings, defaults filled in.
func (c *Config) CloudSettings() Cloud {
	if c.Cloud == nil {
		return DefaultCloud()
	}
	return *c.Cloud
}

// ModelPrefs are the owner's settings of one local model, applied whenever it starts.
type ModelPrefs struct {
	// Settings are launch settings in the panel's form (see Apply), without the model itself.
	Settings map[string]string `json:"settings,omitempty"`
	// Advanced remembers that the owner opened the advanced settings of this model.
	Advanced bool `json:"advanced,omitempty"`
}

// MaxModelPrefs and MaxFavorites bound what the panel can store.
const (
	MaxModelPrefs = 500
	MaxFavorites  = 200
)

// Model keys name a model across engines and OpenRouter: prismml:<file>, ollama:<name>, cloud:<id>.
func LocalKey(engine, model string) string {
	if engine == PrismML {
		model = modelStem(model) + ".gguf"
	}
	return engine + ":" + model
}

// SplitKey returns the engine (prismml, ollama or cloud) and the model of a key.
func SplitKey(key string) (string, string, bool) {
	kind, model, ok := strings.Cut(key, ":")
	if !ok || model == "" || len(key) > 300 || strings.ContainsAny(key, "\x00\r\n\"\\") {
		return "", "", false
	}
	switch kind {
	case PrismML:
		return kind, model, !strings.ContainsAny(model, "/") && !strings.Contains(model, "..") && strings.HasSuffix(strings.ToLower(model), ".gguf")
	case Ollama:
		return kind, model, ValidOllamaModel(model)
	case "cloud":
		return kind, model, ValidCloudModel(model)
	}
	return "", "", false
}

// ValidCloudModel accepts an OpenRouter model id such as anthropic/claude-sonnet-4.5 or
// deepseek/deepseek-r1:free.
func ValidCloudModel(id string) bool {
	if len(id) > 200 || strings.Contains(id, "..") {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._:-~", r)) {
			return false
		}
	}
	return strings.Count(id, "/") == 1 && !strings.HasPrefix(id, "/") && !strings.HasSuffix(id, "/")
}

// Validate checks the chat settings and normalizes the SearXNG address.
func (c *Chat) Validate() error {
	if c.RetentionHours < 0 || c.RetentionHours > 24*3650 {
		return errors.New("Срок хранения чатов: от 1 часа до 10 лет или «всегда».")
	}
	if c.SearchURL != "" {
		u, err := url.Parse(c.SearchURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("Адрес SearXNG: http(s)://адрес без параметров, например https://search.example.com.")
		}
		c.SearchURL = strings.TrimRight(u.String(), "/")
	}
	if c.SearchResults < 1 || c.SearchResults > 10 {
		return errors.New("Источников на запрос: от 1 до 10.")
	}
	if c.SearchLanguage != "" && c.SearchLanguage != "ru" && c.SearchLanguage != "en" {
		return errors.New("Язык поиска: как в вопросе, ru или en.")
	}
	if c.FollowUpModel != "" {
		if kind, _, ok := SplitKey(c.FollowUpModel); !ok || kind != "cloud" {
			return errors.New("Модель для предложений: та же, что в чате, или модель OpenRouter.")
		}
	}
	return nil
}

func (c *Cloud) validate() error {
	if c.LowBalanceUSD < 0 || c.LowBalanceUSD > 1000 {
		return errors.New("Порог баланса OpenRouter: от 0 до 1000 долларов.")
	}
	return nil
}

func (c *Config) validateModels() error {
	if len(c.Models) > MaxModelPrefs {
		return errors.New("Слишком много сохранённых настроек моделей.")
	}
	for key, p := range c.Models {
		engine, _, ok := SplitKey(key)
		if !ok || engine == "cloud" || p == nil {
			return errors.New("Некорректные настройки модели в config.json.")
		}
		for k, v := range p.Settings {
			if k == "model" || (k == "mmproj" && v != "off") {
				return errors.New("Настройки модели не выбирают сам файл модели.")
			}
			if k == "placement" || k == "idle_minutes" {
				continue
			}
			if err := checkStored(engine, k, v); err != nil {
				return err
			}
		}
	}
	if len(c.Favorites) > MaxFavorites {
		return errors.New("Слишком много избранных моделей.")
	}
	for _, f := range c.Favorites {
		if _, _, ok := SplitKey(f); !ok {
			return errors.New("Некорректная избранная модель в config.json.")
		}
	}
	if c.Panel.StartModel != "" {
		if engine, _, ok := SplitKey(c.Panel.StartModel); !ok || engine == "cloud" {
			return errors.New("start_model: локальная модель prismml:<файл> или ollama:<имя>.")
		}
	}
	if slices.ContainsFunc(c.Favorites, func(f string) bool { return f == "" }) {
		return errors.New("Пустая избранная модель.")
	}
	return nil
}
