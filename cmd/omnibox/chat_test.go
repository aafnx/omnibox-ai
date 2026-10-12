package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/control"
	"github.com/aafnx/omnibox-ai/internal/platform"
	"github.com/aafnx/omnibox-ai/internal/telemetry"
)

func chatRig(t *testing.T) (*chatSide, string) {
	data := t.TempDir()
	cfg := config.Default()
	cfg.Panel.Hosts = []string{"127.0.0.1"}
	b64 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg.Panel.Password = config.Password{Salt: b64, Hash: b64, Iterations: 210000}
	path := filepath.Join(data, "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	store, err := config.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	services := platform.NewFakeServices()
	c := newChatSide(data, store, services, &control.Controller{Store: store, Services: services, DataDir: data}, estimator{Config: store.Get,
		GPUs: func(context.Context) []telemetry.GPU { return nil }, RAM: func() int64 { return 32 << 30 }})
	return c, data
}

func post(h http.Handler, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestChatSettingsAndDeleteAllData(t *testing.T) {
	c, data := chatRig(t)
	h := http.HandlerFunc(c.serveSettings)
	w := post(h, "/api/chat-settings", `{"action":"save","retention_hours":168,"search_url":"https://search.example.com/","search_results":8,"search_default":false}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	s := c.Store.Get().ChatSettings()
	if s.RetentionHours != 168 || s.SearchURL != "https://search.example.com" || s.SearchResults != 8 || s.SearchDefault {
		t.Fatalf("%+v", s)
	}
	if s.SiteIcons || strings.Contains(w.Body.String(), `"site_icons":true`) {
		t.Fatal("site icons are off by default")
	}
	if w := post(h, "/api/chat-settings", `{"action":"save","site_icons":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"site_icons":true`) || !c.Store.Get().ChatSettings().SiteIcons {
		t.Fatalf("site icons: %d %s", w.Code, w.Body)
	}
	if w := post(h, "/api/chat-settings", `{"action":"save","search_url":"ftp://x"}`); w.Code != 400 {
		t.Fatalf("%d", w.Code)
	}
	if _, err := c.Chats.Create("a", 1, "", ""); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(data, "logs"), 0o755)
	os.WriteFile(filepath.Join(data, "logs", "panel.log"), []byte("x"), 0o600)
	if w := post(h, "/api/chat-settings", `{"action":"delete-all-data"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"deleted_chats":1`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if list, _ := c.Chats.List(); len(list) != 0 {
		t.Fatal("chats left")
	}
	if _, err := os.Stat(filepath.Join(data, "logs", "panel.log")); err == nil {
		t.Fatal("logs left")
	}
}

func TestCloudModelsReachV1OnlyWhenAllowed(t *testing.T) {
	c, _ := chatRig(t)
	route := cloudRoute{c}
	c.Store.Update(func(cfg *config.Config) error {
		cfg.Favorites = []string{"cloud:openai/gpt-5", "ollama:qwen3:8b"}
		return nil
	})
	if len(route.Models()) != 0 {
		t.Fatal("exposed without the option and the key")
	}
	if w := post(http.HandlerFunc(c.serveCloud), "/api/cloud", `{"action":"settings","expose_api":true}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(route.Models()) != 0 {
		t.Fatal("exposed without a key")
	}
	if err := c.Keys.Save("sk-or-v1-0123456789abcdef0123", time.Now()); err != nil {
		t.Fatal(err)
	}
	if m := route.Models(); len(m) != 1 || m[0] != "openai/gpt-5" {
		t.Fatal(m)
	}
	// The key is never returned to the panel.
	r := httptest.NewRequest(http.MethodGet, "/api/cloud", nil)
	w := httptest.NewRecorder()
	c.serveCloud(w, r)
	if strings.Contains(w.Body.String(), "0123456789abcdef") || !strings.Contains(w.Body.String(), `"connected":true`) {
		t.Fatal(w.Body.String())
	}
}

func TestLibraryFavorites(t *testing.T) {
	c, _ := chatRig(t)
	h := http.HandlerFunc(c.serveLibrary)
	if w := post(h, "/api/library", `{"action":"favorite","model":"cloud:openai/gpt-5","on":true}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := post(h, "/api/library", `{"action":"favorite","model":"../x","on":true}`); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if f := c.Store.Get().Favorites; len(f) != 1 {
		t.Fatal(f)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/library", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"models":[]`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// ChatGPT's models reach /v1 only with the option on and an account signed in, from the list
// Codex gave last: listing them never starts Codex.
func TestChatGPTModelsReachV1OnlyWhenAllowed(t *testing.T) {
	c, data := chatRig(t)
	route := cloudRoute{c}
	r := httptest.NewRequest(http.MethodGet, "/api/chatgpt", nil)
	w := httptest.NewRecorder()
	c.serveChatGPT(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"installed":false`) || !strings.Contains(w.Body.String(), `"signed_in":false`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := post(http.HandlerFunc(c.serveChatGPT), "/api/chatgpt", `{"action":"settings","expose_api":true}`); w.Code != 200 || !c.Store.Get().ChatGPTSettings().ExposeAPI {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(route.Models()) != 0 {
		t.Fatal("exposed without Codex and a sign-in")
	}
	// Codex installed, signed in, its model list kept.
	dir := filepath.Join(data, "chatgpt")
	os.MkdirAll(filepath.Join(dir, "app"), 0o700)
	os.MkdirAll(filepath.Join(dir, "home"), 0o700)
	os.WriteFile(filepath.Join(dir, "app", "codex-app-server"), []byte("x"), 0o755)
	os.WriteFile(filepath.Join(dir, "app", ".omnibox-codex.json"), []byte(`{"tag":"rust-v1","executable":"codex-app-server"}`), 0o600)
	os.WriteFile(filepath.Join(dir, "home", "auth.json"), []byte("{}"), 0o600)
	os.WriteFile(filepath.Join(dir, "models.json"), []byte(`[{"id":"gpt-5.5","name":"GPT-5.5","images":true}]`), 0o600)
	if m := route.Models(); len(m) != 1 || m[0] != "chatgpt/gpt-5.5" {
		t.Fatal(m)
	}
	models, _, _ := c.models(context.Background(), true)
	found := false
	for _, m := range models {
		found = found || m.Key == "chatgpt:gpt-5.5"
	}
	if !found || c.GPT.Quick().Running {
		t.Fatalf("listed %v, running %v", found, c.GPT.Quick().Running)
	}
	if _, err := route.Forward(context.Background(), "chatgpt/other", map[string]any{}); err == nil {
		t.Fatal("a model outside the list is refused")
	}
	if w := post(http.HandlerFunc(c.serveChatGPT), "/api/chatgpt", `{"action":"settings","expose_api":false}`); w.Code != 200 || len(route.Models()) != 0 {
		t.Fatal("still exposed")
	}
	if w := post(http.HandlerFunc(c.serveChatGPT), "/api/chatgpt", `{"action":"remove"}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, "home", "auth.json")); err == nil {
		t.Fatal("the sign-in is removed with Codex")
	}
}

// The search engine is chosen in the settings; one with a key is ready only with its key, and
// the key never reaches the panel.
func TestSearchEngineSettings(t *testing.T) {
	c, _ := chatRig(t)
	h := http.HandlerFunc(c.serveSettings)
	w := post(h, "/api/chat-settings", `{"action":"save","search_engine":"tavily","plan_queries":false}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"search_ready":false`) || !strings.Contains(w.Body.String(), `"plan_queries":false`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := post(h, "/api/chat-settings", `{"action":"save","search_engine":"google"}`); w.Code != 400 {
		t.Fatalf("unknown engine: %d", w.Code)
	}
	if w := post(h, "/api/chat-settings", `{"action":"search-key","engine":"tavily","key":"wrong-0123456789abcdef"}`); w.Code != 400 {
		t.Fatalf("a Tavily key starts with tvly-: %d", w.Code)
	}
	if err := c.SearchKeys.Save("tavily", "tvly-0123456789abcdefWXYZ", time.Now()); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/chat-settings", nil)
	w = httptest.NewRecorder()
	c.serveSettings(w, r)
	if !strings.Contains(w.Body.String(), `"search_ready":true`) || !strings.Contains(w.Body.String(), "…WXYZ") || strings.Contains(w.Body.String(), "tvly-") {
		t.Fatalf("%s", w.Body)
	}
	if w := post(h, "/api/chat-settings", `{"action":"search-key-forget","engine":"tavily"}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"search_ready":false`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	// Sonar needs the OpenRouter key; ChatGPT searches with OpenAI's search unless told otherwise.
	post(h, "/api/chat-settings", `{"action":"save","search_engine":"sonar","sonar_model":"perplexity/sonar-pro"}`)
	if s := c.Store.Get().ChatSettings(); s.SonarModel != "perplexity/sonar-pro" || c.searchReady(s) || !c.openaiSearch() {
		t.Fatalf("%+v", s)
	}
	if _, err := c.search(context.Background(), "q"); err == nil || !strings.Contains(err.Error(), "ключом OpenRouter") {
		t.Fatalf("%v", err)
	}
}

// A recording is written down only through OpenRouter; anything but a WAV is refused.
func TestTranscribeNeedsOpenRouterAndWav(t *testing.T) {
	c, _ := chatRig(t)
	h := http.HandlerFunc(c.serveTranscribe)
	if w := post(h, "/api/chat/transcribe", `{"data":"not audio"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "Некорректная запись") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	wav := `{"data":"UklGR` + strings.Repeat("A", 80) + `"}`
	if w := post(h, "/api/chat/transcribe", wav); w.Code != 400 || !strings.Contains(w.Body.String(), "Подключите OpenRouter") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
