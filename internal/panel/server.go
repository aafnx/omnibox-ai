package panel

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	// Password checks at once: PBKDF2 is deliberately slow, so a flood of logins from many
	// addresses is refused instead of taking every CPU core.
	maxVerifying = 2
	maxJSONBody  = 16384
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
	// API serves /v1 once the panel has checked the Host header.
	API http.Handler
	// Activity is the /v1 request history; RequestContent returns one stored request by id.
	Activity       func() any
	RequestContent func(id string) (json.RawMessage, bool)
	// SearchContent finds stored requests by text; ExportSession returns a session as JSON lines.
	SearchContent func(query, session string) any
	ExportSession func(session string) ([]byte, bool)
	// Storage reports disk usage; StorageAction saves log settings or runs a cleanup.
	Storage       func() any
	StorageAction func(body map[string]any) (any, error)
	// Engines lists installed engine versions; Releases asks GitHub for one engine's versions.
	Engines  func() any
	Releases func(ctx context.Context, engine string) (any, error)
	// Catalog searches Hugging Face (query) or lists a repository's GGUF files (repo).
	Catalog func(ctx context.Context, query, repo string) (any, error)
	// Models lists an engine's installed models with their capabilities and sources.
	Models func(ctx context.Context, engine string) (any, error)
	// Gateway shows the API settings; GatewayAction saves them or creates a key.
	Gateway       func() any
	GatewayAction func(body map[string]any) (any, error)
	// Update shows the installed version and whether a GitHub token for updates is saved;
	// UpdateAction checks for a release or saves or forgets that token.
	Update       func() any
	UpdateAction func(ctx context.Context, body map[string]any) (any, error)
	// SetPassword stores a new panel password hash.
	SetPassword func(p config.Password) error
	// Estimate tells where a model fits with the given context and cache (query parameters).
	Estimate func(ctx context.Context, q url.Values) (any, error)
	// LogPath is the panel's own log: internal failures are written there, not shown in full.
	LogPath string
	// SessionsPath keeps the sign-ins across a restart of the service.
	SessionsPath string
	// Handlers serve whole API paths (the chat, its history, models, OpenRouter) after the
	// session, CSRF and Origin checks; they read their own bodies.
	Handlers map[string]http.Handler
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
	etag        string
	// immutable assets (fonts, images) may be kept by the browser; the page, script and styles
	// are always revalidated, so an update reaches the browser at once.
	immutable bool
	// compress: a large script goes gzipped to browsers that take it — a phone over WireGuard
	// gets a quarter of the diagrams library. It is compressed on the first request only.
	compress bool
}

// gzipped holds compressed embedded files by ETag; they never change while the program runs.
var gzipped sync.Map

func (a asset) gzip() []byte {
	if v, ok := gzipped.Load(a.etag); ok {
		return v.([]byte)
	}
	var buf bytes.Buffer
	z, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = z.Write(a.body)
	_ = z.Close()
	v, _ := gzipped.LoadOrStore(a.etag, buf.Bytes())
	return v.([]byte)
}

// Server is the panel HTTP handler.
type Server struct {
	opt      Options
	cookie   string
	assets   map[string]asset
	mu       sync.Mutex
	logMu    sync.Mutex
	verify   chan struct{}
	sessions map[string]*session
	attempts map[string]*attempt
}

// New builds a panel server with the embedded interface.
func New(opt Options) (*Server, error) {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	s := &Server{opt: opt, cookie: "omnibox_session", sessions: map[string]*session{}, attempts: map[string]*attempt{}, verify: make(chan struct{}, maxVerifying)}
	files := map[string][2]string{
		"/":                {"index.html", "text/html; charset=utf-8"},
		"/app.js":          {"app.js", "text/javascript; charset=utf-8"},
		"/chat.js":         {"chat.js", "text/javascript; charset=utf-8"},
		"/models.js":       {"models.js", "text/javascript; charset=utf-8"},
		"/server.js":       {"server.js", "text/javascript; charset=utf-8"},
		"/settings.js":     {"settings.js", "text/javascript; charset=utf-8"},
		"/style.css":       {"style.css", "text/css; charset=utf-8"},
		"/favicon.svg":     {"favicon.svg", "image/svg+xml"},
		"/brand-light.svg": {"brand-light.svg", "image/svg+xml"},
		"/brand-dark.svg":  {"brand-dark.svg", "image/svg+xml"},
		"/inter-400.woff":  {"inter-400.woff.base64", "font/woff"},
		"/inter-600.woff":  {"inter-600.woff.base64", "font/woff"},
		// Installable on a phone (PWA): the manifest, the icons and a service worker at the root.
		"/manifest.webmanifest":  {"manifest.webmanifest", "application/manifest+json"},
		"/sw.js":                 {"sw.js", "text/javascript; charset=utf-8"},
		"/icon-192.png":          {"icon-192.png", "image/png"},
		"/icon-512.png":          {"icon-512.png", "image/png"},
		"/icon-maskable-512.png": {"icon-maskable-512.png", "image/png"},
		"/apple-touch-icon.png":  {"apple-touch-icon.png", "image/png"},
		// Tab icons: browsers that skip SVG icons take the PNG; /favicon.ico is asked for anyway.
		"/favicon-32.png": {"favicon-32.png", "image/png"},
		"/favicon.ico":    {"favicon-32.png", "image/png"},
		// Formulas, code highlighting and diagrams in chat answers: loaded only when an answer
		// has them. Unchanged builds; versions and licenses are in vendor/LICENSES.txt.
		"/vendor/katex.min.js":     {"vendor/katex.min.js", "text/javascript; charset=utf-8"},
		"/vendor/highlight.min.js": {"vendor/highlight.min.js", "text/javascript; charset=utf-8"},
		"/vendor/mermaid.min.js":   {"vendor/mermaid.min.js", "text/javascript; charset=utf-8"},
		"/vendor/LICENSES.txt":     {"vendor/LICENSES.txt", "text/plain; charset=utf-8"},
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
		sum := sha256.Sum256(body)
		a := asset{body: body, contentType: f[1], etag: `"` + hex.EncodeToString(sum[:8]) + `"`,
			immutable: f[1] == "font/woff" || f[1] == "image/svg+xml" || f[1] == "image/png"}
		a.compress = len(body) > 64<<10 && strings.HasPrefix(f[1], "text/")
		s.assets[route] = a
	}
	s.loadSessions()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if v := recover(); v != nil {
			s.logf("panic %s %s: %v", r.Method, r.URL.Path, v)
			s.json(w, http.StatusInternalServerError, map[string]any{"error": internalMessage})
		}
	}()
	cfg := s.opt.Config()
	path := r.URL.Path
	isAPI := cfg.API.Enabled && (path == "/v1" || strings.HasPrefix(path, "/v1/"))
	if !hostAllowed(cfg, r, isAPI) {
		s.json(w, http.StatusForbidden, map[string]any{"error": "Host запрещён."})
		return
	}
	if isAPI && s.opt.API != nil {
		s.opt.API.ServeHTTP(w, r)
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
		s.static(w, r, a)
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
	if h, ok := s.opt.Handlers[path]; ok {
		s.headers(w.Header(), "application/json; charset=utf-8", "no-store")
		h.ServeHTTP(w, r)
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
	case route == "GET /api/activity" && s.opt.Activity != nil:
		s.json(w, http.StatusOK, s.opt.Activity())
	case route == "GET /api/request-content" && r.URL.Query().Has("q") && s.opt.SearchContent != nil:
		q := r.URL.Query()
		s.json(w, http.StatusOK, map[string]any{"matches": s.opt.SearchContent(q.Get("q"), q.Get("session"))})
	case route == "GET /api/request-content" && r.URL.Query().Get("export") == "1" && s.opt.ExportSession != nil:
		session := r.URL.Query().Get("session")
		data, ok := s.opt.ExportSession(session)
		if !ok {
			s.json(w, http.StatusBadRequest, map[string]any{"error": "Содержимое этого сеанса не сохранено."})
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="omnibox-session-`+session[:12]+`.jsonl"`)
		s.write(w, http.StatusOK, "application/x-ndjson; charset=utf-8", data)
	case route == "GET /api/request-content" && s.opt.RequestContent != nil:
		line, ok := s.opt.RequestContent(r.URL.Query().Get("id"))
		if !ok {
			s.json(w, http.StatusBadRequest, map[string]any{"error": "Содержимое этого запроса не сохранено."})
			return
		}
		s.write(w, http.StatusOK, "application/json; charset=utf-8", line)
	case route == "GET /api/engines" && s.opt.Engines != nil:
		s.json(w, http.StatusOK, map[string]any{"engines": s.opt.Engines()})
	case route == "GET /api/engine-releases" && s.opt.Releases != nil:
		name := r.URL.Query().Get("engine")
		if name != config.PrismML && name != config.Ollama {
			s.json(w, http.StatusBadRequest, map[string]any{"error": "Нужен engine: prismml или ollama."})
			return
		}
		// GitHub is asked only when the owner opens the version list.
		releases, err := s.opt.Releases(r.Context(), name)
		if err != nil {
			s.json(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		s.json(w, http.StatusOK, map[string]any{"releases": releases})
	case route == "GET /api/catalog" && s.opt.Catalog != nil:
		q := r.URL.Query()
		result, err := s.opt.Catalog(r.Context(), q.Get("q"), q.Get("repo"))
		if err != nil {
			s.json(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		s.json(w, http.StatusOK, result)
	case route == "GET /api/models" && s.opt.Models != nil:
		result, err := s.opt.Models(r.Context(), r.URL.Query().Get("backend"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.json(w, http.StatusOK, result)
	case route == "GET /api/estimate" && s.opt.Estimate != nil:
		result, err := s.opt.Estimate(r.Context(), r.URL.Query())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.json(w, http.StatusOK, result)
	case route == "GET /api/gateway" && s.opt.Gateway != nil:
		s.json(w, http.StatusOK, s.opt.Gateway())
	case route == "POST /api/gateway" && s.opt.GatewayAction != nil:
		body, err := readJSON(r)
		if err == nil {
			var result any
			if result, err = s.opt.GatewayAction(body); err == nil {
				s.json(w, http.StatusOK, result)
				return
			}
		}
		s.fail(w, r, err)
	case route == "GET /api/update" && s.opt.Update != nil:
		s.json(w, http.StatusOK, s.opt.Update())
	case route == "POST /api/update" && s.opt.UpdateAction != nil:
		body, err := readJSON(r)
		if err == nil {
			var result any
			if result, err = s.opt.UpdateAction(r.Context(), body); err == nil {
				s.json(w, http.StatusOK, result)
				return
			}
		}
		s.fail(w, r, err)
	case route == "POST /api/password" && s.opt.SetPassword != nil:
		s.changePassword(w, r, t, key, cfg)
	case route == "GET /api/storage" && s.opt.Storage != nil:
		s.json(w, http.StatusOK, s.opt.Storage())
	case route == "POST /api/storage" && s.opt.StorageAction != nil:
		body, err := readJSON(r)
		if err == nil {
			var result any
			if result, err = s.opt.StorageAction(body); err == nil {
				s.json(w, http.StatusOK, result)
				return
			}
		}
		s.fail(w, r, err)
	case route == "POST /api/actions" && s.opt.Action != nil:
		body, err := readJSON(r)
		if err == nil {
			var result any
			if result, err = s.opt.Action(body); err == nil {
				s.json(w, http.StatusAccepted, result)
				return
			}
		}
		s.fail(w, r, err)
	case route == "POST /api/logout":
		s.mu.Lock()
		delete(s.sessions, key)
		s.saveSessionsLocked()
		s.mu.Unlock()
		http.SetCookie(w, s.sessionCookie("", -1, t.secure))
		s.json(w, http.StatusOK, map[string]any{"ok": true})
	default:
		s.json(w, http.StatusNotFound, map[string]any{"error": "Не найдено."})
	}
}

// hostAllowed answers only the configured names; /v1 also answers the clients' API host.
// The NAS proxy keeps the browser's Host (llm.fnx.red), so public_origin is accepted from the
// trusted proxy addresses only.
func hostAllowed(cfg *config.Config, r *http.Request, api bool) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	hosts := cfg.Panel.Hosts
	if api && cfg.API.PublicURL != "" {
		if u, err := url.Parse(cfg.API.PublicURL); err == nil {
			hosts = append(append([]string{}, hosts...), u.Hostname())
		}
	}
	if cfg.Panel.PublicOrigin != "" && fromTrustedProxy(r, &cfg.Panel) {
		if u, err := url.Parse(cfg.Panel.PublicOrigin); err == nil {
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
		s.logf("вход: %s заблокирован после %d ошибок", t.client, maxAttempts)
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
	select {
	case s.verify <- struct{}{}:
	default:
		s.json(w, http.StatusTooManyRequests, map[string]any{"error": "Панель проверяет другие попытки входа. Повтори через несколько секунд."})
		return
	}
	valid := VerifyPassword(password, cfg.Panel.Password)
	<-s.verify
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
		s.logf("вход: неверный пароль с %s (%s)", t.client, map[bool]string{true: "через NAS", false: "напрямую"}[t.secure])
		s.json(w, http.StatusUnauthorized, map[string]any{"error": "Неверный пароль."})
		return
	}
	delete(s.attempts, t.client)
	s.logf("вход: успешно с %s (%s)", t.client, map[bool]string{true: "через NAS", false: "напрямую"}[t.secure])
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
	s.saveSessionsLocked()
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

const internalMessage = "Внутренняя ошибка панели. Подробности записаны в журнал панели (panel.log в каталоге журналов)."

// fail answers a refused request: a check the owner can fix is 400 with its message; a failure of
// the system (disk, network, files) is 500 with a general message, and its details go to the
// panel log, since they may name paths and internals.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if internalError(err) {
		s.logf("%s %s: %v", r.Method, r.URL.Path, err)
		s.json(w, http.StatusInternalServerError, map[string]any{"error": internalMessage})
		return
	}
	s.json(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
}

func internalError(err error) bool {
	var path *fs.PathError
	var link *os.LinkError
	var sys *os.SyscallError
	var op *net.OpError
	var u *url.Error
	return errors.As(err, &path) || errors.As(err, &link) || errors.As(err, &sys) || errors.As(err, &op) || errors.As(err, &u)
}

// Logf writes to the panel log, for the parts of the panel served by other packages.
func (s *Server) Logf(format string, args ...any) { s.logf(format, args...) }

// logf appends a line to the panel log, which rotates at 1 MiB and keeps two older copies.
func (s *Server) logf(format string, args ...any) {
	if s.opt.LogPath == "" {
		return
	}
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if info, err := os.Stat(s.opt.LogPath); err == nil && info.Size() >= panelLogLimit {
		_ = os.Remove(s.opt.LogPath + ".2")
		_ = os.Rename(s.opt.LogPath+".1", s.opt.LogPath+".2")
		_ = os.Rename(s.opt.LogPath, s.opt.LogPath+".1")
	}
	if err := os.MkdirAll(filepath.Dir(s.opt.LogPath), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(s.opt.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", s.opt.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
}

const panelLogLimit = 1 << 20

// static serves an embedded file with an ETag, so a revalidation costs a 304 and an update is
// picked up on the next load.
func (s *Server) static(w http.ResponseWriter, r *http.Request, a asset) {
	h := w.Header()
	h.Set("ETag", a.etag)
	cache := "no-cache"
	if a.immutable {
		cache = "public, max-age=604800"
	}
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, a.etag) {
		s.headers(h, a.contentType, cache)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	s.headers(h, a.contentType, cache)
	if a.compress {
		h.Add("Vary", "Accept-Encoding")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h.Set("Content-Encoding", "gzip")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(a.gzip())
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(a.body)
}

// write sends a response with the panel's security headers.
func (s *Server) write(w http.ResponseWriter, status int, contentType string, body []byte) {
	s.headers(w.Header(), contentType, "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) headers(h http.Header, contentType, cache string) {
	h.Set("Content-Type", contentType)
	h.Set("Cache-Control", cache)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src "+s.images()+"; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
}

// images is the CSP source list for images: the panel's own, and the web search sources' icons
// over https only when the owner turned them on (the browser then asks each source site).
func (s *Server) images() string {
	if s.opt.Config != nil {
		if cfg := s.opt.Config(); cfg != nil && cfg.ChatSettings().SiteIcons {
			return "'self' data: https:"
		}
	}
	return "'self' data:"
}

// changePassword replaces the panel password after checking the current one, and ends every
// other session: whoever knew the old password is signed out.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request, t transport, current string, cfg *config.Config) {
	body, err := readJSON(r)
	if err != nil {
		s.json(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	old, ok1 := body["current"].(string)
	next, ok2 := body["password"].(string)
	if len(body) != 2 || !ok1 || !ok2 {
		s.json(w, http.StatusBadRequest, map[string]any{"error": "Нужны текущий и новый пароль."})
		return
	}
	s.mu.Lock()
	if a, ok := s.attempts[t.client]; ok && a.count >= maxAttempts {
		s.mu.Unlock()
		s.json(w, http.StatusTooManyRequests, map[string]any{"error": "Слишком много попыток. Повтори через 5 минут."})
		return
	}
	s.mu.Unlock()
	if !VerifyPassword(old, cfg.Panel.Password) {
		s.mu.Lock()
		a, ok := s.attempts[t.client]
		if !ok && len(s.attempts) < maxTrackedIPs {
			a = &attempt{until: s.opt.Now().UTC().Add(lockout)}
			s.attempts[t.client] = a
		}
		if a != nil {
			a.count++
		}
		s.mu.Unlock()
		s.json(w, http.StatusForbidden, map[string]any{"error": "Текущий пароль неверен."})
		return
	}
	hash, err := NewPassword(next)
	if err == nil {
		err = s.opt.SetPassword(hash)
	}
	if err != nil {
		s.json(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	s.mu.Lock()
	for k := range s.sessions {
		if k != current {
			delete(s.sessions, k)
		}
	}
	s.saveSessionsLocked()
	s.mu.Unlock()
	s.json(w, http.StatusOK, map[string]any{"ok": true})
}

// EndSessions signs everyone out, e.g. after a password reset from the command line.
func (s *Server) EndSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.sessions)
	s.saveSessionsLocked()
}
