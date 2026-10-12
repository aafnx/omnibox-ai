package panel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
)

// Reference PBKDF2-SHA256 (210000 iterations) computed independently with Python hashlib;
// the PowerShell version writes panel.json with the same standard derivation.
const (
	testPassword = "пароль-панели-123"
	testSalt     = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	testHash     = "fNJiCucpCLQxt7lP81Db69so4Pq0/ZW8Qynri2pdgQY="
)

func testConfig(t *testing.T, change func(c *config.Config)) *config.Config {
	t.Helper()
	c := config.Default()
	c.Panel.Hosts = []string{"omnibox", "127.0.0.1"}
	c.Panel.Password = config.Password{Salt: testSalt, Hash: testHash, Iterations: 210000}
	c.Panel.SSHTarget = "user@omnibox"
	c.API = config.API{Enabled: true, PublicURL: "https://llm.fnx.red/v1", Engine: config.PrismML}
	if change != nil {
		change(c)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func newServer(t *testing.T, c *config.Config) *Server {
	t.Helper()
	s, err := New(Options{Config: func() *config.Config { return c }})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func call(s *Server, method, target, body string, mutate func(r *http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Host = "omnibox:18091"
	r.RemoteAddr = "192.168.1.20:50000"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if mutate != nil {
		mutate(r)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return v
}

func login(t *testing.T, s *Server, mutate func(r *http.Request)) (*http.Cookie, string) {
	t.Helper()
	w := call(s, "POST", "/api/login", `{"password":"`+testPassword+`"}`, mutate)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies: %v", cookies)
	}
	return cookies[0], decode(t, w)["csrf"].(string)
}

func TestPasswordHashIsCompatibleWithPowerShellVersion(t *testing.T) {
	p := testConfig(t, nil).Panel.Password
	if !VerifyPassword(testPassword, p) {
		t.Fatal("reference password rejected")
	}
	if VerifyPassword("wrong-password", p) || VerifyPassword("", p) || VerifyPassword(strings.Repeat("a", 257), p) {
		t.Fatal("wrong password accepted")
	}
	fresh, err := NewPassword("новый-пароль-панели")
	if err != nil || !VerifyPassword("новый-пароль-панели", fresh) || VerifyPassword(testPassword, fresh) {
		t.Fatalf("new password: %v", err)
	}
	if _, err := NewPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
}

func TestStaticFilesAndSecurityHeaders(t *testing.T) {
	s := newServer(t, testConfig(t, nil))
	w := call(s, "GET", "/", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<html") || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("index: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy", "Cache-Control"} {
		if w.Header().Get(h) == "" {
			t.Fatalf("missing %s", h)
		}
	}
	if csp := w.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' data:;") {
		t.Fatalf("images from outside allowed by default: %q", csp)
	}
	// Site icons of the web search sources are loaded by the browser only when the owner chose so.
	icons := newServer(t, testConfig(t, func(c *config.Config) { c.Chat = &config.Chat{RetentionHours: 24, SearchResults: 5, SiteIcons: true} }))
	if csp := call(icons, "GET", "/", "", nil).Header().Get("Content-Security-Policy"); !strings.Contains(csp, "img-src 'self' data: https:;") || !strings.Contains(csp, "script-src 'self';") {
		t.Fatalf("site icons: %q", csp)
	}
	font := call(s, "GET", "/inter-400.woff", "", nil)
	if font.Code != 200 || !strings.HasPrefix(font.Body.String(), "wOFF") {
		t.Fatalf("font not decoded: %d %q", font.Code, font.Body.String()[:4])
	}
}

func TestHostMethodAndSessionChecks(t *testing.T) {
	s := newServer(t, testConfig(t, nil))
	if w := call(s, "GET", "/", "", func(r *http.Request) { r.Host = "evil.example:18091" }); w.Code != 403 {
		t.Fatalf("foreign host: %d", w.Code)
	}
	if w := call(s, "PUT", "/api/session", "", nil); w.Code != 405 {
		t.Fatalf("PUT: %d", w.Code)
	}
	if w := call(s, "GET", "/api/session", "", nil); w.Code != 401 {
		t.Fatalf("no session: %d", w.Code)
	}
	if w := call(s, "POST", "/api/login", `{"password":"x"}`, func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }); w.Code != 403 {
		t.Fatalf("foreign origin: %d", w.Code)
	}
	if w := call(s, "POST", "/api/login", `{"password":"x","more":1}`, nil); w.Code != 400 {
		t.Fatalf("extra fields: %d", w.Code)
	}
	if w := call(s, "POST", "/api/login", `{"password":"x"}`, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }); w.Code != 400 {
		t.Fatalf("not JSON: %d", w.Code)
	}
}

func TestLoginSessionCsrfAndLogout(t *testing.T) {
	s := newServer(t, testConfig(t, nil))
	cookie, csrf := login(t, s, nil)
	if cookie.Name != "omnibox_session" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 28800 || cookie.Secure {
		t.Fatalf("cookie: %+v", cookie)
	}
	withCookie := func(r *http.Request) { r.AddCookie(cookie) }
	w := call(s, "GET", "/api/session", "", withCookie)
	if w.Code != 200 || decode(t, w)["csrf"] != csrf || decode(t, w)["ssh_target"] != "user@omnibox" {
		t.Fatalf("session: %d %s", w.Code, w.Body.String())
	}
	if w := call(s, "POST", "/api/logout", "{}", withCookie); w.Code != 403 {
		t.Fatalf("logout without CSRF: %d", w.Code)
	}
	w = call(s, "POST", "/api/logout", "{}", func(r *http.Request) { withCookie(r); r.Header.Set("X-Omnibox-CSRF", csrf) })
	if w.Code != 200 || w.Result().Cookies()[0].MaxAge >= 0 {
		t.Fatalf("logout: %d %v", w.Code, w.Result().Cookies())
	}
	if w := call(s, "GET", "/api/session", "", withCookie); w.Code != 401 {
		t.Fatalf("session after logout: %d", w.Code)
	}
}

func TestLoginLockoutAfterSixFailures(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	c := testConfig(t, nil)
	s, _ := New(Options{Config: func() *config.Config { return c }, Now: func() time.Time { return now }})
	for i := 0; i < 6; i++ {
		if w := call(s, "POST", "/api/login", `{"password":"wrong-password"}`, nil); w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := call(s, "POST", "/api/login", `{"password":"`+testPassword+`"}`, nil); w.Code != 429 {
		t.Fatalf("locked out: %d", w.Code)
	}
	if w := call(s, "POST", "/api/login", `{"password":"`+testPassword+`"}`, func(r *http.Request) { r.RemoteAddr = "192.168.1.21:1" }); w.Code != 200 {
		t.Fatalf("other client: %d", w.Code)
	}
	now = now.Add(6 * time.Minute)
	if w := call(s, "POST", "/api/login", `{"password":"`+testPassword+`"}`, nil); w.Code != 200 {
		t.Fatalf("after lockout: %d", w.Code)
	}
}

func TestSessionExpiresAfterEightHours(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	c := testConfig(t, nil)
	s, _ := New(Options{Config: func() *config.Config { return c }, Now: func() time.Time { return now }})
	cookie, _ := login(t, s, nil)
	now = now.Add(8*time.Hour + time.Second)
	if w := call(s, "GET", "/api/session", "", func(r *http.Request) { r.AddCookie(cookie) }); w.Code != 401 {
		t.Fatalf("expired session: %d", w.Code)
	}
}

func TestReverseProxySessionIsSecureAndNotReusedOverHTTP(t *testing.T) {
	s := newServer(t, testConfig(t, func(c *config.Config) {
		c.Panel.PublicOrigin = "https://llm.fnx.red"
		c.Panel.TrustedProxies = []string{"192.168.1.10"}
	}))
	viaProxy := func(r *http.Request) {
		r.RemoteAddr = "192.168.1.10:40000"
		r.Host = "llm.fnx.red" // Synology keeps the browser's Host
		r.Header.Set("X-Forwarded-Proto", "https")
		r.Header.Set("X-Forwarded-Host", "llm.fnx.red")
		r.Header.Set("X-Forwarded-For", "10.8.0.5")
		r.Header.Set("Origin", "https://llm.fnx.red")
	}
	cookie, _ := login(t, s, viaProxy)
	if !cookie.Secure {
		t.Fatal("cookie through HTTPS proxy must be Secure")
	}
	if w := call(s, "GET", "/api/session", "", func(r *http.Request) { r.AddCookie(cookie) }); w.Code != 401 {
		t.Fatalf("secure session over direct HTTP: %d", w.Code)
	}
	if w := call(s, "GET", "/api/session", "", func(r *http.Request) { viaProxy(r); r.AddCookie(cookie) }); w.Code != 200 {
		t.Fatalf("secure session via proxy: %d", w.Code)
	}
	downgrade := func(r *http.Request) { viaProxy(r); r.Header.Set("X-Forwarded-Proto", "http") }
	if w := call(s, "POST", "/api/login", `{"password":"x"}`, downgrade); w.Code != 400 {
		t.Fatalf("downgraded proto: %d", w.Code)
	}
	if w := call(s, "GET", "/", "", viaProxy); w.Code != 200 {
		t.Fatalf("panel through the proxy: %d", w.Code)
	}
	if w := call(s, "GET", "/v1/models", "", viaProxy); w.Code != 503 {
		t.Fatalf("/v1 through the proxy: %d", w.Code)
	}
	// The public name is answered only from the NAS; headers from another address never make a
	// request secure.
	untrusted := func(r *http.Request) { viaProxy(r); r.RemoteAddr = "192.168.1.99:1" }
	if w := call(s, "GET", "/", "", untrusted); w.Code != 403 {
		t.Fatalf("public name from an untrusted address: %d", w.Code)
	}
	if w := call(s, "GET", "/api/session", "", func(r *http.Request) { untrusted(r); r.Host = "omnibox:18091"; r.AddCookie(cookie) }); w.Code != 401 {
		t.Fatalf("untrusted forwarding: %d", w.Code)
	}
}

func TestUnknownRoutesAndApiHost(t *testing.T) {
	s := newServer(t, testConfig(t, nil))
	cookie, csrf := login(t, s, nil)
	auth := func(r *http.Request) { r.AddCookie(cookie); r.Header.Set("X-Omnibox-CSRF", csrf) }
	if w := call(s, "POST", "/api/updates", `{"action":"apply"}`, auth); w.Code != 404 {
		t.Fatalf("unknown action route: %d", w.Code)
	}
	if w := call(s, "GET", "/api/unknown", "", auth); w.Code != 404 {
		t.Fatalf("unknown route: %d", w.Code)
	}
	// The clients' API host answers /v1 only, never the panel.
	if w := call(s, "GET", "/v1/models", "", func(r *http.Request) { r.Host = "llm.fnx.red" }); w.Code != 503 {
		t.Fatalf("/v1 on the API host: %d", w.Code)
	}
	if w := call(s, "GET", "/", "", func(r *http.Request) { r.Host = "llm.fnx.red" }); w.Code != 403 {
		t.Fatalf("panel on the API host: %d", w.Code)
	}
}

func TestSessionKeyRejectsMalformedTokens(t *testing.T) {
	if sessionKey("abc") != "" || sessionKey(strings.Repeat("A", 64)) != "" {
		t.Fatal("malformed token accepted")
	}
	if k := sessionKey(strings.Repeat("a", 64)); len(k) != 64 || k == strings.Repeat("a", 64) {
		t.Fatalf("key %q", k)
	}
}

func TestStatusTelemetryAndLogsRoutes(t *testing.T) {
	c := testConfig(t, func(c *config.Config) {
		c.Engines[config.PrismML] = &config.Engine{Port: 8801, Launch: config.Launch{Settings: map[string]string{}}}
	})
	var gpuAsked, onceAsked bool
	s, _ := New(Options{
		Config:    func() *config.Config { return c },
		Status:    func(_ context.Context, gpu bool) any { gpuAsked = gpu; return map[string]any{"backends": []any{}} },
		Telemetry: func(_ context.Context, once bool) any { onceAsked = once; return map[string]any{"enabled": false} },
		Logs:      func(engine string) any { return []any{engine} },
	})
	cookie, _ := login(t, s, nil)
	auth := func(r *http.Request) { r.AddCookie(cookie) }
	if w := call(s, "GET", "/api/status?gpu=1", "", auth); w.Code != 200 || !gpuAsked {
		t.Fatalf("status: %d gpu %v", w.Code, gpuAsked)
	}
	if w := call(s, "GET", "/api/telemetry?once=1", "", auth); w.Code != 200 || !onceAsked {
		t.Fatalf("telemetry: %d once %v", w.Code, onceAsked)
	}
	if w := call(s, "GET", "/api/logs?backend=prismml", "", auth); w.Code != 200 || !strings.Contains(w.Body.String(), "prismml") {
		t.Fatalf("logs: %d %s", w.Code, w.Body.String())
	}
	if w := call(s, "GET", "/api/logs?backend=vllm", "", auth); w.Code != 400 {
		t.Fatalf("unknown engine logs: %d", w.Code)
	}
	if w := call(s, "GET", "/api/status", "", nil); w.Code != 401 {
		t.Fatalf("status without login: %d", w.Code)
	}
}

func TestActionsNeedSessionAndCsrfAndPassStructuredFields(t *testing.T) {
	var got map[string]any
	s, _ := New(Options{
		Config: func() *config.Config { return testConfig(t, nil) },
		Action: func(body map[string]any) (any, error) {
			if body["action"] == "exec" {
				return nil, errors.New("Неизвестная операция панели.")
			}
			got = body
			return map[string]any{"id": "op1"}, nil
		},
	})
	cookie, csrf := login(t, s, nil)
	if w := call(s, "POST", "/api/actions", `{"action":"stopall"}`, func(r *http.Request) { r.AddCookie(cookie) }); w.Code != 403 || got != nil {
		t.Fatalf("action without CSRF: %d", w.Code)
	}
	auth := func(r *http.Request) { r.AddCookie(cookie); r.Header.Set("X-Omnibox-CSRF", csrf) }
	if w := call(s, "POST", "/api/actions", `{"action":"start","backend":"prismml"}`, auth); w.Code != 202 || decode(t, w)["id"] != "op1" || got["backend"] != "prismml" {
		t.Fatalf("action: %d %v", w.Code, got)
	}
	if w := call(s, "POST", "/api/actions", `{"action":"exec"}`, auth); w.Code != 400 || !strings.Contains(w.Body.String(), "Неизвестная операция") {
		t.Fatalf("refused action: %d %s", w.Code, w.Body.String())
	}
	if w := call(s, "POST", "/api/actions", `[1]`, auth); w.Code != 400 {
		t.Fatalf("non-object body: %d", w.Code)
	}
}

func TestActivityContentAndApiHandler(t *testing.T) {
	var apiHits int
	s, _ := New(Options{
		Config:   func() *config.Config { return testConfig(t, nil) },
		Activity: func() any { return map[string]any{"requests": []any{}} },
		RequestContent: func(id string) (json.RawMessage, bool) {
			return json.RawMessage(`{"id":"` + id + `"}`), id == "0123456789abcdef0123456789abcdef"
		},
		API: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { apiHits++; w.WriteHeader(204) }),
	})
	cookie, _ := login(t, s, nil)
	auth := func(r *http.Request) { r.AddCookie(cookie) }
	if w := call(s, "GET", "/api/activity", "", auth); w.Code != 200 || !strings.Contains(w.Body.String(), "requests") {
		t.Fatalf("activity: %d", w.Code)
	}
	if w := call(s, "GET", "/api/request-content?id=0123456789abcdef0123456789abcdef", "", auth); w.Code != 200 {
		t.Fatalf("content: %d", w.Code)
	}
	if w := call(s, "GET", "/api/request-content?id=other", "", auth); w.Code != 400 {
		t.Fatalf("missing content: %d", w.Code)
	}
	if w := call(s, "GET", "/api/activity", "", nil); w.Code != 401 {
		t.Fatalf("activity without login: %d", w.Code)
	}
	if w := call(s, "GET", "/v1/models", "", func(r *http.Request) { r.Host = "llm.fnx.red" }); w.Code != 204 || apiHits != 1 {
		t.Fatalf("/v1 handler: %d", w.Code)
	}
	if w := call(s, "GET", "/v1/models", "", func(r *http.Request) { r.Host = "evil.example" }); w.Code != 403 || apiHits != 1 {
		t.Fatalf("/v1 on a foreign host: %d", w.Code)
	}
}

func TestStorageRoutes(t *testing.T) {
	var got map[string]any
	s, _ := New(Options{
		Config:  func() *config.Config { return testConfig(t, nil) },
		Storage: func() any { return map[string]any{"logs_bytes": 1} },
		StorageAction: func(body map[string]any) (any, error) {
			if body["action"] != "save" {
				return nil, errors.New("Неизвестное действие с журналами.")
			}
			got = body
			return map[string]any{"logs_bytes": 2}, nil
		},
	})
	cookie, csrf := login(t, s, nil)
	auth := func(r *http.Request) { r.AddCookie(cookie); r.Header.Set("X-Omnibox-CSRF", csrf) }
	if w := call(s, "GET", "/api/storage", "", auth); w.Code != 200 || decode(t, w)["logs_bytes"] != float64(1) {
		t.Fatalf("view: %d", w.Code)
	}
	if w := call(s, "POST", "/api/storage", `{"action":"save","request_log":"metadata"}`, auth); w.Code != 200 || got["request_log"] != "metadata" {
		t.Fatalf("save: %d", w.Code)
	}
	if w := call(s, "POST", "/api/storage", `{"action":"wipe"}`, auth); w.Code != 400 {
		t.Fatalf("unknown action: %d", w.Code)
	}
	if w := call(s, "POST", "/api/storage", `{"action":"save"}`, func(r *http.Request) { r.AddCookie(cookie) }); w.Code != 403 {
		t.Fatalf("without CSRF: %d", w.Code)
	}
}

func TestEngineRoutes(t *testing.T) {
	var asked string
	s, _ := New(Options{
		Config:  func() *config.Config { return testConfig(t, nil) },
		Engines: func() any { return []any{map[string]any{"name": "prismml"}} },
		Releases: func(_ context.Context, engine string) (any, error) {
			asked = engine
			if engine == "ollama" {
				return nil, errors.New("GitHub недоступен")
			}
			return []any{map[string]any{"tag": "b2"}}, nil
		},
	})
	cookie, _ := login(t, s, nil)
	auth := func(r *http.Request) { r.AddCookie(cookie) }
	if w := call(s, "GET", "/api/engines", "", auth); w.Code != 200 || !strings.Contains(w.Body.String(), "prismml") {
		t.Fatalf("engines: %d", w.Code)
	}
	if w := call(s, "GET", "/api/engine-releases?engine=prismml", "", auth); w.Code != 200 || asked != "prismml" || !strings.Contains(w.Body.String(), "b2") {
		t.Fatalf("releases: %d", w.Code)
	}
	if w := call(s, "GET", "/api/engine-releases?engine=ollama", "", auth); w.Code != 502 {
		t.Fatalf("GitHub failure: %d", w.Code)
	}
	asked = ""
	if w := call(s, "GET", "/api/engine-releases?engine=../x", "", auth); w.Code != 400 || asked != "" {
		t.Fatalf("unknown engine: %d", w.Code)
	}
}

func TestCatalogRoute(t *testing.T) {
	var gotQuery, gotRepo string
	s, _ := New(Options{
		Config: func() *config.Config { return testConfig(t, nil) },
		Catalog: func(_ context.Context, query, repo string) (any, error) {
			gotQuery, gotRepo = query, repo
			if query == "fail" {
				return nil, errors.New("Hugging Face недоступен")
			}
			return map[string]any{"models": []any{}}, nil
		},
	})
	cookie, _ := login(t, s, nil)
	auth := func(r *http.Request) { r.AddCookie(cookie) }
	if w := call(s, "GET", "/api/catalog?q=qwen", "", auth); w.Code != 200 || gotQuery != "qwen" {
		t.Fatalf("search: %d", w.Code)
	}
	if w := call(s, "GET", "/api/catalog?repo=a/b", "", auth); w.Code != 200 || gotRepo != "a/b" {
		t.Fatalf("files: %d", w.Code)
	}
	if w := call(s, "GET", "/api/catalog?q=fail", "", auth); w.Code != 502 {
		t.Fatalf("failure: %d", w.Code)
	}
}

func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	c := testConfig(t, nil)
	var stored config.Password
	s, _ := New(Options{
		Config:      func() *config.Config { return c },
		SetPassword: func(p config.Password) error { stored = p; c.Panel.Password = p; return nil },
	})
	mine, csrf := login(t, s, nil)
	other, _ := login(t, s, nil)
	auth := func(r *http.Request) { r.AddCookie(mine); r.Header.Set("X-Omnibox-CSRF", csrf) }
	if w := call(s, "POST", "/api/password", `{"current":"wrong-password","password":"новый-пароль-панели"}`, auth); w.Code != 403 || stored.Hash != "" {
		t.Fatalf("wrong current password: %d", w.Code)
	}
	if w := call(s, "POST", "/api/password", `{"current":"`+testPassword+`","password":"short"}`, auth); w.Code != 400 || stored.Hash != "" {
		t.Fatalf("short password: %d", w.Code)
	}
	if w := call(s, "POST", "/api/password", `{"current":"`+testPassword+`","password":"новый-пароль-панели"}`, auth); w.Code != 200 || !VerifyPassword("новый-пароль-панели", stored) {
		t.Fatalf("change: %d", w.Code)
	}
	if w := call(s, "GET", "/api/session", "", func(r *http.Request) { r.AddCookie(other) }); w.Code != 401 {
		t.Fatalf("other session survived: %d", w.Code)
	}
	if w := call(s, "GET", "/api/session", "", func(r *http.Request) { r.AddCookie(mine) }); w.Code != 200 {
		t.Fatalf("own session ended: %d", w.Code)
	}
	if w := call(s, "POST", "/api/login", `{"password":"`+testPassword+`"}`, nil); w.Code != 401 {
		t.Fatalf("old password still works: %d", w.Code)
	}
}

func TestGatewayRoutesNeverReturnTheHash(t *testing.T) {
	hash := strings.Repeat("b", 64)
	c := testConfig(t, func(c *config.Config) { c.API.KeyHash, c.API.RequireKey = hash, true })
	s, _ := New(Options{
		Config:        func() *config.Config { return c },
		Gateway:       func() any { return map[string]any{"has_key": true} },
		GatewayAction: func(map[string]any) (any, error) { return map[string]any{"key": "obai-x"}, nil },
	})
	cookie, csrf := login(t, s, nil)
	w := call(s, "GET", "/api/gateway", "", func(r *http.Request) { r.AddCookie(cookie) })
	if w.Code != 200 || strings.Contains(w.Body.String(), hash) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := call(s, "POST", "/api/gateway", `{"action":"generate"}`, func(r *http.Request) { r.AddCookie(cookie) }); w.Code != 403 {
		t.Fatalf("without CSRF: %d", w.Code)
	}
	if w := call(s, "POST", "/api/gateway", `{"action":"generate"}`, func(r *http.Request) { r.AddCookie(cookie); r.Header.Set("X-Omnibox-CSRF", csrf) }); w.Code != 200 {
		t.Fatalf("generate: %d", w.Code)
	}
}

func TestHandlersRunOnlyAfterSessionAndCSRF(t *testing.T) {
	c := testConfig(t, nil)
	calls := 0
	s, err := New(Options{Config: func() *config.Config { return c }, Handlers: map[string]http.Handler{
		"/api/chats": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write([]byte(`{"ok":true}`)) }),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if w := call(s, "GET", "/api/chats", "", nil); w.Code != 401 || calls != 0 {
		t.Fatalf("without a session: %d", w.Code)
	}
	cookie, csrf := login(t, s, nil)
	withCookie := func(r *http.Request) { r.AddCookie(cookie) }
	if w := call(s, "POST", "/api/chats", "{}", withCookie); w.Code != 403 || calls != 0 {
		t.Fatalf("without CSRF: %d", w.Code)
	}
	w := call(s, "POST", "/api/chats", "{}", func(r *http.Request) { withCookie(r); r.Header.Set("X-Omnibox-CSRF", csrf) })
	if w.Code != 200 || calls != 1 || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("%d %d %v", w.Code, calls, w.Header())
	}
}
