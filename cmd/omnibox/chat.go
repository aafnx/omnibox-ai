package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	omnibox "github.com/aafnx/omnibox-ai"
	"github.com/aafnx/omnibox-ai/internal/chatgpt"
	"github.com/aafnx/omnibox-ai/internal/chatrun"
	"github.com/aafnx/omnibox-ai/internal/chats"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/control"
	"github.com/aafnx/omnibox-ai/internal/library"
	"github.com/aafnx/omnibox-ai/internal/openrouter"
	"github.com/aafnx/omnibox-ai/internal/platform"
	"github.com/aafnx/omnibox-ai/internal/reasoning"
	"github.com/aafnx/omnibox-ai/internal/storage"
	"github.com/aafnx/omnibox-ai/internal/telemetry"
	"github.com/aafnx/omnibox-ai/internal/websearch"
)

// chatSide is the panel's chat with everything around it: the model list, OpenRouter and the
// chat's settings. Every part asks the disk, the engines or the network only on a request.
type chatSide struct {
	Data       string
	Store      *config.Store
	Services   platform.Services
	Controller *control.Controller
	Estimates  estimator
	Keys       openrouter.KeyStore
	Catalog    *openrouter.Catalog
	Spend      *openrouter.Spend
	Chats      *chats.Store
	Assistants *chats.Assistants
	// GPT is the ChatGPT subscription through Codex's app-server.
	GPT  *chatgpt.Service
	Logf func(format string, args ...any)
}

func newChatSide(data string, store *config.Store, services platform.Services, controller *control.Controller, estimates estimator) *chatSide {
	c := &chatSide{Data: data, Store: store, Services: services, Controller: controller, Estimates: estimates,
		Keys:       openrouter.KeyStore{Path: filepath.Join(data, filepath.FromSlash(openrouter.KeyFile))},
		Spend:      &openrouter.Spend{Path: filepath.Join(data, filepath.FromSlash(openrouter.SpendFile))},
		Chats:      &chats.Store{Dir: filepath.Join(data, "chats")},
		Assistants: &chats.Assistants{Path: filepath.Join(data, "assistants.json")}}
	c.Catalog = &openrouter.Catalog{Path: filepath.Join(data, filepath.FromSlash(openrouter.CatalogFile)), Client: c.client}
	c.GPT = &chatgpt.Service{Dir: filepath.Join(data, chatgpt.Dir), Version: omnibox.Version, Logf: c.logf}
	return c
}

// gptModels is the subscription's model list: kept from Codex's last run, else asked (which
// starts Codex). Nothing without a sign-in.
func (c *chatSide) gptModels(ctx context.Context) ([]chatgpt.Model, error) {
	if !c.GPT.SignedIn() {
		return nil, nil
	}
	if list := c.GPT.CachedModels(); list != nil {
		return list, nil
	}
	return c.GPT.Models(ctx)
}

// gptContext is the history budget of a ChatGPT model: Codex does not list the window, and
// compacts a thread that outgrows it.
const gptContext = 200_000

// client is the OpenRouter client with the saved key, or nil without one.
func (c *chatSide) client() *openrouter.Client {
	saved, ok := c.Keys.Load()
	if !ok {
		return nil
	}
	return &openrouter.Client{Key: saved.Key}
}

func (c *chatSide) running(engine string) bool {
	s, err := c.Services.Query(config.ServiceName(engine))
	return err == nil && s.State == platform.Running
}

// models is the whole list; OpenRouter's part only with a saved key.
func (c *chatSide) models(ctx context.Context, cloud bool) ([]library.Model, library.Running, error) {
	cfg := c.Store.Get()
	run := library.RunningOf(cfg, c.running)
	var out []library.Model
	for _, name := range config.EngineNames {
		if _, ok := cfg.Engines[name]; !ok {
			continue
		}
		list, err := c.Controller.InstalledModels(ctx, name)
		if err != nil {
			c.logf("models %s: %v", name, err)
			continue
		}
		out = append(out, library.Local(cfg, name, list, run)...)
	}
	var err error
	if cloud && c.client() != nil {
		var list []openrouter.Model
		list, _, err = c.Catalog.Get(ctx, false)
		out = append(out, library.Cloud(cfg, list)...)
	}
	if cloud {
		list, gerr := c.gptModels(ctx)
		if gerr != nil {
			c.logf("chatgpt models: %v", gerr)
		}
		out = append(out, library.ChatGPT(cfg, list)...)
	}
	return out, run, err
}

// modelInfo describes one model for the chat; a local one only needs its own engine's list.
func (c *chatSide) modelInfo(ctx context.Context, key string) (library.Model, int, error) {
	name, id, ok := config.SplitKey(key)
	if !ok {
		return library.Model{}, 0, errors.New("Выберите модель.")
	}
	cfg := c.Store.Get()
	if name == "chatgpt" {
		list, err := c.gptModels(ctx)
		if err != nil {
			return library.Model{}, 0, err
		}
		if list == nil {
			return library.Model{}, 0, chatgpt.ErrSignedOut
		}
		if m, ok := library.Find(library.ChatGPT(cfg, list), key); ok {
			return m, gptContext, nil
		}
		return library.Model{}, 0, errors.New("Модели " + id + " нет в подписке ChatGPT.")
	}
	if name == "cloud" {
		if c.client() == nil {
			return library.Model{}, 0, errors.New("OpenRouter не подключён: добавьте ключ в «Настройки → OpenRouter».")
		}
		list, _, err := c.Catalog.Get(ctx, false)
		if m, ok := library.Find(library.Cloud(cfg, list), key); ok {
			return m, m.Context, nil
		}
		if err != nil {
			return library.Model{}, 0, err
		}
		return library.Model{}, 0, errors.New("Модели " + id + " нет в списке OpenRouter.")
	}
	e, ok := cfg.Engines[name]
	if !ok {
		return library.Model{}, 0, errors.New(config.Label(name) + " не установлен.")
	}
	list, err := c.Controller.InstalledModels(ctx, name)
	if err != nil {
		return library.Model{}, 0, err
	}
	m, ok := library.Find(library.Local(cfg, name, list, library.RunningOf(cfg, c.running)), key)
	if !ok {
		return library.Model{}, 0, errors.New("Модель не найдена: возможно, она удалена.")
	}
	n, _ := strconv.Atoi(e.RunningLaunch().Settings["context"])
	if n == 0 {
		n = m.Context
	}
	if n == 0 {
		n = 8192
	}
	return m, n, nil
}

func (c *chatSide) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// runner answers chat messages.
func (c *chatSide) runner(local func(ctx context.Context, session string, body []byte) *http.Response) *chatrun.Runner {
	return &chatrun.Runner{Chats: c.Chats, Assistants: c.Assistants, Config: c.Store.Get, Model: c.modelInfo, Local: local, Spend: c.Spend, Logf: c.logf,
		Cloud: func() (*openrouter.Client, error) {
			if cl := c.client(); cl != nil {
				return cl, nil
			}
			return nil, errors.New("OpenRouter не подключён: добавьте ключ в «Настройки → OpenRouter».")
		},
		ChatGPT: c.GPT,
		ChatGPTLimits: func() any {
			if l := c.GPT.Quick().Limits; l != nil {
				return l
			}
			return nil
		},
		Read: websearch.Reader{}.Read,
		Search: func(ctx context.Context, q string) ([]websearch.Result, error) {
			s := c.Store.Get().ChatSettings()
			return websearch.Client{URL: s.SearchURL}.Search(ctx, q, s.SearchLanguage, s.SearchResults)
		}}
}

// handlers are the panel's routes of this side.
func (c *chatSide) handlers(local func(ctx context.Context, session string, body []byte) *http.Response) map[string]http.Handler {
	history := &chats.Handler{Chats: c.Chats, Assistants: c.Assistants, Logf: c.logf,
		KeepHours: func() int { return c.Store.Get().ChatSettings().RetentionHours }}
	run := c.runner(local)
	return map[string]http.Handler{
		"/api/chats":          history,
		"/api/assistants":     history,
		"/api/chat/send":      run,
		"/api/chat/followups": http.HandlerFunc(run.FollowUps),
		"/api/chat/title":     http.HandlerFunc(run.Title),
		"/api/chat/active":    http.HandlerFunc(run.Active),
		"/api/chat/stream":    http.HandlerFunc(run.Follow),
		"/api/chat/stop":      http.HandlerFunc(run.Stop),
		"/api/library":        http.HandlerFunc(c.serveLibrary),
		"/api/cloud":          http.HandlerFunc(c.serveCloud),
		"/api/chatgpt":        http.HandlerFunc(c.serveChatGPT),
		"/api/chat-settings":  http.HandlerFunc(c.serveSettings),
	}
}

func reply(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func replyError(w http.ResponseWriter, err error) {
	reply(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}

// body reads a small JSON request of the panel; numbers stay json.Number.
func body(r *http.Request, into any) error {
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") || r.ContentLength > 64<<10 {
		return errors.New("Нужен JSON-запрос до 64 КиБ.")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return errors.New("Некорректный запрос.")
	}
	return nil
}

// serveLibrary is GET /api/library (the list with what the estimate needs) and POST for
// favourites, a model's own settings and the model loaded after boot.
func (c *chatSide) serveLibrary(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		models, run, err := c.models(r.Context(), true)
		cfg := c.Store.Get()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		vram, measured := c.Estimates.vram(ctx)
		cancel()
		out := map[string]any{"models": models, "running": run.Key, "start_model": cfg.Panel.StartModel,
			"system":  map[string]any{"vram_bytes": vram, "vram_measured": measured, "ram_bytes": telemetry.RAMTotal(), "desktop_vram_bytes": desktopVRAM},
			"cloud":   map[string]any{"connected": c.client() != nil},
			"chatgpt": map[string]any{"connected": c.GPT.SignedIn()}}
		if models == nil {
			out["models"] = []library.Model{}
		}
		if err != nil {
			out["cloud"] = map[string]any{"connected": true, "error": err.Error()}
		}
		reply(w, http.StatusOK, out)
		return
	}
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "Метод запрещён."})
		return
	}
	var in struct {
		Action   string            `json:"action"`
		Model    string            `json:"model"`
		On       bool              `json:"on"`
		Settings map[string]string `json:"settings"`
		Advanced bool              `json:"advanced"`
	}
	if err := body(r, &in); err != nil {
		replyError(w, err)
		return
	}
	var err error
	switch in.Action {
	case "favorite":
		err = control.SetFavorite(c.Store, in.Model, in.On)
	case "settings":
		err = control.SaveModelPrefs(c.Store, in.Model, in.Settings, in.Advanced)
	case "start-model":
		_, err = c.Controller.Do(r.Context(), control.Request{Action: "start-model", Model: in.Model})
	default:
		err = errors.New("Неизвестное действие с моделями.")
	}
	if err != nil {
		replyError(w, err)
		return
	}
	reply(w, http.StatusOK, map[string]any{"ok": true, "favorites": c.Store.Get().Favorites})
}

// serveCloud is the OpenRouter section: the key (never returned), spending, the account balance
// (asked from OpenRouter only with ?account=1) and the settings.
func (c *chatSide) serveCloud(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if r.URL.Query().Get("account") == "1" {
			cl := c.client()
			if cl == nil {
				replyError(w, errors.New("OpenRouter не подключён."))
				return
			}
			a, err := cl.Account(r.Context())
			if err != nil {
				reply(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
				return
			}
			reply(w, http.StatusOK, a)
			return
		}
		reply(w, http.StatusOK, c.cloudView())
		return
	}
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "Метод запрещён."})
		return
	}
	var in struct {
		Action     string   `json:"action"`
		Key        string   `json:"key"`
		ExposeAPI  *bool    `json:"expose_api"`
		LowBalance *float64 `json:"low_balance_usd"`
	}
	if err := body(r, &in); err != nil {
		replyError(w, err)
		return
	}
	switch in.Action {
	case "key-save":
		// The key is checked with OpenRouter before it is kept.
		if _, err := (&openrouter.Client{Key: strings.TrimSpace(in.Key)}).Account(r.Context()); err != nil {
			var oe *openrouter.Error
			if errors.As(err, &oe) && oe.Status == http.StatusUnauthorized {
				replyError(w, errors.New("OpenRouter не принял ключ. Проверьте, что он скопирован целиком."))
				return
			}
			if !strings.HasPrefix(strings.TrimSpace(in.Key), "sk-or-") {
				replyError(w, errors.New("Ключ OpenRouter начинается с sk-or-."))
				return
			}
			c.logf("openrouter: key check failed, key saved anyway: %v", err)
		}
		if err := c.Keys.Save(in.Key, time.Now()); err != nil {
			replyError(w, err)
			return
		}
		_, _, _ = c.Catalog.Get(r.Context(), true)
	case "key-forget":
		if err := c.Keys.Forget(); err != nil {
			replyError(w, err)
			return
		}
	case "settings":
		err := c.Store.Update(func(cfg *config.Config) error {
			s := cfg.CloudSettings()
			if in.ExposeAPI != nil {
				s.ExposeAPI = *in.ExposeAPI
			}
			if in.LowBalance != nil {
				s.LowBalanceUSD = *in.LowBalance
			}
			cfg.Cloud = &s
			return nil
		})
		if err != nil {
			replyError(w, err)
			return
		}
	case "refresh":
		if _, _, err := c.Catalog.Get(r.Context(), true); err != nil {
			reply(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
	default:
		replyError(w, errors.New("Неизвестное действие OpenRouter."))
		return
	}
	reply(w, http.StatusOK, c.cloudView())
}

// serveChatGPT is the ChatGPT section: installing Codex, the sign-in with a device code, the
// account and its limits (asked from OpenAI only with ?account=1 or "limits") and the settings.
func (c *chatSide) serveChatGPT(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		out := c.gptView(c.GPT.Quick())
		if r.URL.Query().Get("account") == "1" && c.GPT.Quick().Installed {
			a, err := c.GPT.Account(r.Context())
			if err == nil && a != nil {
				_, err = c.GPT.Limits(r.Context())
				if err == nil {
					_, err = c.GPT.Models(r.Context())
				}
			}
			out = c.gptView(c.GPT.Quick())
			if err != nil {
				out["error"] = err.Error()
			}
		}
		reply(w, http.StatusOK, out)
		return
	}
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "Метод запрещён."})
		return
	}
	var in struct {
		Action    string `json:"action"`
		ExposeAPI *bool  `json:"expose_api"`
	}
	if err := body(r, &in); err != nil {
		replyError(w, err)
		return
	}
	var err error
	switch in.Action {
	case "install":
		err = c.GPT.StartInstall()
	case "login":
		_, err = c.GPT.StartLogin(r.Context())
	case "login-cancel":
		err = c.GPT.CancelLogin(r.Context())
	case "logout":
		err = c.GPT.Logout(r.Context())
	case "remove":
		err = c.GPT.Remove()
	case "limits":
		if _, err = c.GPT.Limits(r.Context()); err == nil {
			_, err = c.GPT.Account(r.Context())
		}
	case "settings":
		err = c.Store.Update(func(cfg *config.Config) error {
			s := cfg.ChatGPTSettings()
			if in.ExposeAPI != nil {
				s.ExposeAPI = *in.ExposeAPI
			}
			cfg.ChatGPT = &s
			return nil
		})
	default:
		err = errors.New("Неизвестное действие ChatGPT.")
	}
	if err != nil {
		replyError(w, err)
		return
	}
	reply(w, http.StatusOK, c.gptView(c.GPT.Quick()))
}

func (c *chatSide) gptView(st chatgpt.Status) map[string]any {
	models := c.GPT.CachedModels()
	if !c.GPT.SignedIn() || models == nil {
		models = []chatgpt.Model{}
	}
	return map[string]any{"status": st, "signed_in": c.GPT.SignedIn(), "expose_api": c.Store.Get().ChatGPTSettings().ExposeAPI, "models": models}
}

func (c *chatSide) cloudView() map[string]any {
	s := c.Store.Get().CloudSettings()
	out := map[string]any{"connected": false, "expose_api": s.ExposeAPI, "low_balance_usd": s.LowBalanceUSD,
		"days": c.Spend.Days(14), "by_model": c.Spend.ByModel(30), "by_source": c.Spend.BySource(30), "month": total(c.Spend.Days(30)), "today": total(c.Spend.Days(1))}
	if saved, ok := c.Keys.Load(); ok {
		out["connected"], out["hint"], out["saved"] = true, saved.Hint, saved.Saved
	}
	if info, err := os.Stat(c.Catalog.Path); err == nil {
		out["fetched"] = info.ModTime().UTC()
	}
	return out
}

func total(days []openrouter.Day) float64 {
	sum := 0.0
	for _, d := range days {
		sum += d.USD
	}
	return sum
}

// serveSettings is the chat's settings: retention, web search, and deleting chats and data.
func (c *chatSide) serveSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		reply(w, http.StatusOK, c.settingsView())
		return
	}
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "Метод запрещён."})
		return
	}
	var in struct {
		Action         string  `json:"action"`
		RetentionHours *int    `json:"retention_hours"`
		SearchURL      *string `json:"search_url"`
		SearchResults  *int    `json:"search_results"`
		SearchLanguage *string `json:"search_language"`
		SearchDefault  *bool   `json:"search_default"`
		SiteIcons      *bool   `json:"site_icons"`
		FollowUps      *bool   `json:"follow_ups"`
		ReadPages      *bool   `json:"read_pages"`
		AutoTitle      *bool   `json:"auto_title"`
		FollowUpModel  *string `json:"follow_up_model"`
	}
	if err := body(r, &in); err != nil {
		replyError(w, err)
		return
	}
	out := map[string]any{}
	switch in.Action {
	case "save":
		err := c.Store.Update(func(cfg *config.Config) error {
			s := cfg.ChatSettings()
			if in.RetentionHours != nil {
				s.RetentionHours = *in.RetentionHours
			}
			if in.SearchURL != nil {
				s.SearchURL = strings.TrimSpace(*in.SearchURL)
			}
			if in.SearchResults != nil {
				s.SearchResults = *in.SearchResults
			}
			if in.SearchLanguage != nil {
				s.SearchLanguage = *in.SearchLanguage
			}
			if in.SearchDefault != nil {
				s.SearchDefault = *in.SearchDefault
			}
			if in.SiteIcons != nil {
				s.SiteIcons = *in.SiteIcons
			}
			if in.AutoTitle != nil {
				s.NoAutoTitle = !*in.AutoTitle
			}
			if in.ReadPages != nil {
				s.NoReadPages = !*in.ReadPages
			}
			if in.FollowUps != nil {
				s.FollowUps = *in.FollowUps
			}
			if in.FollowUpModel != nil {
				s.FollowUpModel = strings.TrimSpace(*in.FollowUpModel)
			}
			cfg.Chat = &s
			return nil
		})
		if err != nil {
			replyError(w, err)
			return
		}
	case "check-search":
		u := c.Store.Get().ChatSettings().SearchURL
		if in.SearchURL != nil {
			probe := config.Chat{SearchURL: strings.TrimSpace(*in.SearchURL), SearchResults: 1}
			if err := probe.Validate(); err != nil {
				replyError(w, err)
				return
			}
			u = probe.SearchURL
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		took, err := websearch.Client{URL: u}.Check(ctx)
		cancel()
		if err != nil {
			replyError(w, err)
			return
		}
		out["search_ms"] = took.Milliseconds()
	case "delete-all-data":
		// Chats, journals and request texts; models and settings stay.
		n, err := c.Chats.DeleteAll(false)
		if err != nil {
			replyError(w, err)
			return
		}
		logs := storage.RemoveLogs(c.Data)
		out["deleted_chats"], out["logs"] = n, logs
	default:
		replyError(w, errors.New("Неизвестное действие."))
		return
	}
	for k, v := range c.settingsView() {
		out[k] = v
	}
	reply(w, http.StatusOK, out)
}

func (c *chatSide) settingsView() map[string]any {
	s := c.Store.Get().ChatSettings()
	list, _ := c.Chats.List()
	size := int64(0)
	if entries, err := os.ReadDir(c.Chats.Dir); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil && !e.IsDir() {
				size += info.Size()
			}
		}
	}
	return map[string]any{"retention_hours": s.RetentionHours, "search_url": s.SearchURL, "search_results": s.SearchResults,
		"search_language": s.SearchLanguage, "search_default": s.SearchDefault, "site_icons": s.SiteIcons, "read_pages": !s.NoReadPages, "auto_title": !s.NoAutoTitle,
		"follow_ups": s.FollowUps, "follow_up_model": s.FollowUpModel, "chats": len(list), "chats_bytes": size}
}

// cloudRoute shows the favourite OpenRouter models to /v1 clients when the owner allowed it.
type cloudRoute struct{ c *chatSide }

func (r cloudRoute) Models() []string {
	cfg := r.c.Store.Get()
	var out []string
	if cfg.CloudSettings().ExposeAPI && r.c.client() != nil {
		for _, f := range cfg.Favorites {
			if id, ok := strings.CutPrefix(f, "cloud:"); ok {
				out = append(out, id)
			}
		}
	}
	// The subscription's models, from the list Codex gave last: /v1/models never starts Codex.
	if cfg.ChatGPTSettings().ExposeAPI && r.c.GPT.SignedIn() {
		for _, m := range r.c.GPT.CachedModels() {
			out = append(out, chatgpt.Prefix+m.ID)
		}
	}
	return out
}

func (r cloudRoute) Forward(ctx context.Context, model string, body map[string]any) (*http.Response, error) {
	if strings.HasPrefix(model, chatgpt.Prefix) {
		if !slices.Contains(r.Models(), model) {
			return nil, statusError{http.StatusNotFound, errors.New("model not available")}
		}
		data, _ := json.Marshal(body)
		return r.c.GPT.Chat(ctx, data)
	}
	cl := r.c.client()
	if cl == nil || !slices.Contains(r.Models(), model) {
		return nil, statusError{http.StatusNotFound, errors.New("model not available")}
	}
	if list, _, _ := r.c.Catalog.Get(ctx, false); list != nil {
		for _, m := range list {
			if m.ID == model {
				reasoning.Sanitize(m.Reasoning, body)
			}
		}
	}
	data, _ := json.Marshal(body)
	resp, err := cl.Chat(ctx, data)
	var oe *openrouter.Error
	if errors.As(err, &oe) {
		return nil, statusError{oe.Status, err}
	}
	return resp, err
}

func (r cloudRoute) Spent(model string, usd float64) {
	if err := r.c.Spend.Add(model, usd, openrouter.SourceAPI); err != nil {
		r.c.logf("openrouter: spend ledger: %v", err)
	}
}

type statusError struct {
	status int
	err    error
}

func (e statusError) Error() string   { return e.err.Error() }
func (e statusError) HTTPStatus() int { return e.status }
