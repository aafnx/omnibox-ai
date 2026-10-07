package panel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/web"
)

const (
	sessionLifetime = 8 * time.Hour
	maxSessions     = 32
	maxAttempts     = 6
	lockout         = 5 * time.Minute
	maxTrackedIPs   = 512
	maxJSONBody     = 16384
)

// Options configure a panel server.
type Options struct {
	// Config returns the current settings; the service replaces them after every change.
	Config func() *config.Config
	Now    func() time.Time
	// Status builds /api/status; gpu adds a GPU reading.
	Status func(ctx context.Context, gpu bool) any
	// Telemetry returns a resource sample; once is a single reading the user asked for.
	Telemetry func(ctx context.Context, once bool) any
	// Logs returns the engines' recent log lines; engine "" means all.
	Logs func(engine string) any
	// Action starts a background engine operation from structured fields and returns its id.
	Action func(body map[string]any) (any, error)
}

type session struct {
	csrf    string
	secure  bool
	expires time.Time
}

type attempt struct {
	count int
	until time.Time
}

type asset struct {
	body        []byte
	contentType string
}

// Server is the panel HTTP handler.
type Server struct {
	opt      Options
	cookie   string
	assets   map[string]asset
	mu       sync.Mutex
	sessions map[string]*session
	attempts map[string]*attempt
}

// New builds a panel server with the embedded interface.
func New(opt Options) (*Server, error) {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	s := &Server{opt: opt, cookie: "omnibox_session", sessions: map[string]*session{}, attempts: map[string]*attempt{}}
	files := map[string][2]string{
		"/":                {"index.html", "text/html; charset=utf-8"},
		"/app.js":          {"app.js", "text/javascript; charset=utf-8"},
		"/style.css":       {"style.css", "text/css; charset=utf-8"},
		"/favicon.svg":     {"favicon.svg", "image/svg+xml"},
		"/brand-light.svg": {"brand-light.svg", "image/svg+xml"},
		"/brand-dark.svg":  {"brand-dark.svg", "image/svg+xml"},
		"/inter-400.woff":  {"inter-400.woff.base64", "font/woff"},
		"/inter-600.woff":  {"inter-600.woff.base64", "font/woff"},
	}
	s.assets = map[string]asset{}
	for route, f := range files {
		body, err := fs.ReadFile(web.FS, f[0])
		if err != nil {
			return nil, err
		}
		if f[1] == "font/woff" {
			if body, err = base64.StdEncoding.DecodeString(string(bytes.TrimSpace(body))); err != nil {
				return nil, err
			}
		}
		s.assets[route] = asset{body, f[1]}
	}
	return s, nil
}

// Routes of the PowerShell panel that later stages port; until then they answer 501.
var pendingRoutes = map[string]bool{
	"GET /api/activity": true,
	"GET /api/storage":  true, "GET /api/engines": true, "GET /api/engine-releases": true, "GET /api/gateway": true,
	"GET /api/updates": true, "GET /api/catalog": true, "GET /api/request-content": true,
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if v := recover(); v != nil {
			s.json(w, http.StatusInternalServerError, map[string]any{"error": "Внутренняя ошибка панели."})
		}
	}()
	cfg := s.opt.Config()
	path := r.URL.Path
	isAPI := cfg.API.Enabled && (path == "/v1" || strings.HasPrefix(path, "/v1/"))
	if !hostAllowed(cfg, r.Host, isAPI) {
		s.json(w, http.StatusForbidden, map[string]any{"error": "Host запрещён."})
		return
	}
	if isAPI {
		s.json(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"message": "The API is not available yet.", "type": "omnibox_error"}})
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		s.json(w, http.StatusMethodNotAllowed, map[string]any{"error": "Метод запрещён."})
		return
	}
	if a, ok := s.assets[path]; ok && r.Method == http.MethodGet {
		s.write(w, http.StatusOK, a.contentType, a.body)
		return
	}
	t, err := requestTransport(r, &cfg.Panel)
	if err != nil {
		s.json(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if r.Method == http.MethodPost && !originAllowed(r.Header.Get("Origin"), t.authority, t.scheme) {
		s.json(w, http.StatusForbidden, map[string]any{"error": "Origin запрещён."})
		return
	}
	if r.Method == http.MethodPost && path == "/api/login" {
		s.login(w, r, t, cfg)
		return
	}
	key, sess := s.currentSession(r, t)
	if sess == nil {
		s.json(w, http.StatusUnauthorized, map[string]any{"error": "Войди в панель."})
		return
	}
	if r.Method == http.MethodPost && r.Header.Get("X-Omnibox-CSRF") != sess.csrf {
		s.json(w, http.StatusForbidden, map[string]any{"error": "CSRF-проверка не пройдена."})
		return
	}
	route := r.Method + " " + path
	switch {
	case route == "GET /api/session":
		s.json(w, http.StatusOK, map[string]any{"csrf": sess.csrf, "ssh_target": cfg.Panel.SSHTarget, "port": cfg.Panel.Port})
	case route == "GET /api/status" && s.opt.Status != nil:
		s.json(w, http.StatusOK, s.opt.Status(r.Context(), r.URL.Query().Get("gpu") == "1"))
	case route == "GET /api/telemetry" && s.opt.Telemetry != nil:
		s.json(w, http.StatusOK, s.opt.Telemetry(r.Context(), r.URL.Query().Get("once") == "1"))
	case route == "GET /api/logs" && s.opt.Logs != nil:
		name := r.URL.Query().Get("backend")
		if _, ok := cfg.Engines[name]; name != "" && !ok {
			s.json(w, http.StatusBadRequest, map[string]any{"error": "Неизвестный движок."})
			return
		}
		s.json(w, http.StatusOK, map[string]any{"logs": s.opt.Logs(name)})
	case route == "POST /api/actions" && s.opt.Action != nil:
		body, err := readJSON(r)
		if err == nil {
			var result any
			if result, err = s.opt.Action(body); err == nil {
				s.json(w, http.StatusAccepted, result)
				return
			}
		}
		s.json(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
	case route == "POST /api/logout":
		s.mu.Lock()
		delete(s.sessions, key)
		s.mu.Unlock()
		http.SetCookie(w, s.sessionCookie("", -1, t.secure))
		s.json(w, http.StatusOK, map[string]any{"ok": true})
	case pendingRoutes[route] || r.Method == http.MethodPost && strings.HasPrefix(path, "/api/"):
		s.json(w, http.StatusNotImplemented, map[string]any{"error": "Ещё не перенесено в Go-версию."})
	default:
		s.json(w, http.StatusNotFound, map[string]any{"error": "Не найдено."})
	}
}

// hostAllowed answers only the configured names; /v1 also answers the clients' API host.
func hostAllowed(cfg *config.Config, hostport string, api bool) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	hosts := cfg.Panel.Hosts
	if api && cfg.API.PublicURL != "" {
		if u, err := url.Parse(cfg.API.PublicURL); err == nil {
			hosts = append(append([]string{}, hosts...), u.Hostname())
		}
	}
	for _, h := range hosts {
		if strings.EqualFold(h, host) {
			return true
		}
	}
	return false
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, t transport, cfg *config.Config) {
	now := s.opt.Now().UTC()
	s.mu.Lock()
	for ip, a := range s.attempts {
		if a.until.Before(now) {
			delete(s.attempts, ip)
		}
	}
	if a, ok := s.attempts[t.client]; ok && a.count >= maxAttempts {
		s.mu.Unlock()
		s.json(w, http.StatusTooManyRequests, map[string]any{"error": "Слишком много попыток. Повтори через 5 минут."})
		return
	}
	s.mu.Unlock()
	body, err := readJSON(r)
	if err != nil {
		s.json(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	password, ok := body["password"].(string)
	if len(body) != 1 || !ok {
		s.json(w, http.StatusBadRequest, map[string]any{"error": "Нужен пароль."})
		return
	}
	// PBKDF2 runs outside the lock: one slow login never blocks other requests.
	valid := VerifyPassword(password, cfg.Panel.Password)
	s.mu.Lock()
	if !valid {
		a, ok := s.attempts[t.client]
		if !ok {
			if len(s.attempts) >= maxTrackedIPs {
				s.mu.Unlock()
				s.json(w, http.StatusTooManyRequests, map[string]any{"error": "Повтори позже."})
				return
			}
			a = &attempt{until: now.Add(lockout)}
			s.attempts[t.client] = a
		}
		a.count++
		s.mu.Unlock()
		s.json(w, http.StatusUnauthorized, map[string]any{"error": "Неверный пароль."})
		return
	}
	delete(s.attempts, t.client)
	for k, v := range s.sessions {
		if v.expires.Before(now) {
			delete(s.sessions, k)
		}
	}
	if len(s.sessions) >= maxSessions {
		keys := make([]string, 0, len(s.sessions))
		for k := range s.sessions {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return s.sessions[keys[i]].expires.Before(s.sessions[keys[j]].expires) })
		delete(s.sessions, keys[0])
	}
	id, csrf := newNonce(), newNonce()
	s.sessions[sessionKey(id)] = &session{csrf: csrf, secure: t.secure, expires: now.Add(sessionLifetime)}
	s.mu.Unlock()
	http.SetCookie(w, s.sessionCookie(id, int(sessionLifetime/time.Second), t.secure))
	s.json(w, http.StatusOK, map[string]any{"csrf": csrf})
}

func (s *Server) sessionCookie(value string, maxAge int, secure bool) *http.Cookie {
	return &http.Cookie{Name: s.cookie, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: maxAge, Secure: secure}
}

func (s *Server) currentSession(r *http.Request, t transport) (string, *session) {
	c, err := r.Cookie(s.cookie)
	if err != nil {
		return "", nil
	}
	key := sessionKey(c.Value)
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[key]
	// A session created over HTTPS through the NAS is never accepted over plain HTTP.
	if !ok || sess.expires.Before(s.opt.Now().UTC()) || (sess.secure && !t.secure) {
		return "", nil
	}
	return key, sess
}

var jsonType = regexp.MustCompile(`^application/json(?:\s*;|$)`)

// readJSON accepts a JSON object up to 16 KiB with an explicit Content-Length.
func readJSON(r *http.Request) (map[string]any, error) {
	if !jsonType.MatchString(r.Header.Get("Content-Type")) || r.ContentLength < 0 || r.ContentLength > maxJSONBody {
		return nil, errors.New("Нужен JSON-запрос до 16 КиБ с Content-Length.")
	}
	data := make([]byte, r.ContentLength)
	if _, err := io.ReadFull(r.Body, data); err != nil {
		return nil, errors.New("Неполный запрос.")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil || body == nil {
		return nil, errors.New("Нужен JSON-объект.")
	}
	return body, nil
}

func (s *Server) json(w http.ResponseWriter, status int, value any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value)
	s.write(w, status, "application/json; charset=utf-8", bytes.TrimRight(buf.Bytes(), "\n"))
}

// write sends a response with the panel's security headers.
func (s *Server) write(w http.ResponseWriter, status int, contentType string, body []byte) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
