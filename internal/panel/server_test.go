package panel

import (
	"encoding/json"
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

func testPanel(t *testing.T, change func(p map[string]any)) *config.Panel {
	t.Helper()
	p := map[string]any{"schema_version": 1, "port": 18090, "allowed_hosts": []any{"omnibox", "127.0.0.1"},
		"credential": map[string]any{"salt": testSalt, "hash": testHash, "iterations": 210000}, "ssh_target": "user@omnibox"}
	if change != nil {
		change(p)
	}
	data, _ := json.Marshal(p)
	cfg, err := config.ParsePanel(data)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func newServer(t *testing.T, p *config.Panel, preview bool) *Server {
	t.Helper()
	s, err := New(Options{Panel: p, Gateway: &config.Gateway{Enabled: true, Port: 18090, BaseURL: "https://llm.fnx.red/v1"}, Preview: preview})
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
	p := testPanel(t, nil)
	if !VerifyPassword(testPassword, p.Credential) {
		t.Fatal("reference password rejected")
	}
	if VerifyPassword("wrong-password", p.Credential) || VerifyPassword("", p.Credential) || VerifyPassword(strings.Repeat("a", 257), p.Credential) {
		t.Fatal("wrong password accepted")
	}
}

func TestStaticFilesAndSecurityHeaders(t *testing.T) {
	s := newServer(t, testPanel(t, nil), true)
	w := call(s, "GET", "/", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "<html") || w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Fatalf("index: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	for _, h := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy", "Cache-Control"} {
		if w.Header().Get(h) == "" {
			t.Fatalf("missing %s", h)
		}
	}
	font := call(s, "GET", "/inter-400.woff", "", nil)
	if font.Code != 200 || !strings.HasPrefix(font.Body.String(), "wOFF") {
		t.Fatalf("font not decoded: %d %q", font.Code, font.Body.String()[:4])
	}
}

func TestHostMethodAndSessionChecks(t *testing.T) {
	s := newServer(t, testPanel(t, nil), true)
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
	s := newServer(t, testPanel(t, nil), false)
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
	s, _ := New(Options{Panel: testPanel(t, nil), Now: func() time.Time { return now }})
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
	s, _ := New(Options{Panel: testPanel(t, nil), Now: func() time.Time { return now }})
	cookie, _ := login(t, s, nil)
	now = now.Add(8*time.Hour + time.Second)
	if w := call(s, "GET", "/api/session", "", func(r *http.Request) { r.AddCookie(cookie) }); w.Code != 401 {
		t.Fatalf("expired session: %d", w.Code)
	}
}

func TestReverseProxySessionIsSecureAndNotReusedOverHTTP(t *testing.T) {
	p := testPanel(t, func(p map[string]any) {
		p["public_origin"] = "https://llm.fnx.red"
		p["trusted_proxy_addresses"] = []any{"192.168.1.10"}
	})
	s := newServer(t, p, false)
	viaProxy := func(r *http.Request) {
		r.RemoteAddr = "192.168.1.10:40000"
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
	// Headers from an untrusted address never make a request secure.
	untrusted := func(r *http.Request) { viaProxy(r); r.RemoteAddr = "192.168.1.99:1" }
	if w := call(s, "GET", "/api/session", "", func(r *http.Request) { untrusted(r); r.AddCookie(cookie) }); w.Code != 401 {
		t.Fatalf("untrusted forwarding: %d", w.Code)
	}
}

func TestPreviewIsReadOnlyAndUsesItsOwnCookie(t *testing.T) {
	s := newServer(t, testPanel(t, nil), true)
	cookie, csrf := login(t, s, nil)
	if cookie.Name == "omnibox_session" {
		t.Fatal("preview must not overwrite the working panel's cookie")
	}
	auth := func(r *http.Request) { r.AddCookie(cookie); r.Header.Set("X-Omnibox-CSRF", csrf) }
	if w := call(s, "POST", "/api/actions", `{"action":"stopall"}`, auth); w.Code != 403 {
		t.Fatalf("control in preview: %d", w.Code)
	}
	if w := call(s, "GET", "/api/status", "", auth); w.Code != 501 {
		t.Fatalf("pending route: %d", w.Code)
	}
	if w := call(s, "GET", "/v1/models", "", nil); w.Code != 503 {
		t.Fatalf("/v1 in preview: %d", w.Code)
	}
	if w := call(s, "GET", "/api/unknown", "", auth); w.Code != 404 {
		t.Fatalf("unknown route: %d", w.Code)
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
