package panel

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
)

type restartRig struct {
	t    *testing.T
	c    *config.Config
	now  time.Time
	path string
}

func (r *restartRig) start() *Server {
	r.t.Helper()
	s, err := New(Options{Config: func() *config.Config { return r.c }, Now: func() time.Time { return r.now }, SessionsPath: r.path,
		SetPassword: func(p config.Password) error { r.c.Panel.Password = p; return nil }})
	if err != nil {
		r.t.Fatal(err)
	}
	return s
}

func signedIn(s *Server, cookie *http.Cookie) bool {
	return call(s, "GET", "/api/session", "", func(r *http.Request) { r.AddCookie(cookie) }).Code == 200
}

func TestSessionsSurviveARestart(t *testing.T) {
	r := &restartRig{t: t, c: testConfig(t, nil), now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), path: filepath.Join(t.TempDir(), "run", "sessions.bin")}
	cookie, _ := login(t, r.start(), nil)
	saved, _ := os.ReadFile(r.path)
	if strings.Contains(string(saved), cookie.Value) {
		t.Fatal("the cookie itself was saved")
	}
	if !signedIn(r.start(), cookie) {
		t.Fatal("session lost after a restart")
	}
	// A restart does not extend the eight hours.
	r.now = r.now.Add(8*time.Hour + time.Second)
	if signedIn(r.start(), cookie) {
		t.Fatal("expired session restored")
	}
}

func TestLogoutAndPasswordChangeEndSavedSessions(t *testing.T) {
	r := &restartRig{t: t, c: testConfig(t, nil), now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), path: filepath.Join(t.TempDir(), "sessions.bin")}
	s := r.start()
	first, csrf := login(t, s, nil)
	second, _ := login(t, s, nil)
	call(s, "POST", "/api/logout", "{}", func(q *http.Request) { q.AddCookie(first); q.Header.Set("X-Omnibox-CSRF", csrf) })
	s = r.start()
	if signedIn(s, first) || !signedIn(s, second) {
		t.Fatal("logout not saved")
	}
	// A reset from the command line changes the password while the panel may be stopped.
	r.c.Panel.Password = config.Password{Salt: testHash, Hash: testSalt, Iterations: 210000}
	if signedIn(r.start(), second) {
		t.Fatal("session survived a password change")
	}
}

func TestDamagedSessionsFileMeansSigningIn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.bin")
	os.WriteFile(path, []byte("not a sessions file"), 0o600)
	r := &restartRig{t: t, c: testConfig(t, nil), now: time.Now(), path: path}
	s := r.start()
	if w := call(s, "GET", "/api/session", "", nil); w.Code != 401 {
		t.Fatalf("%d", w.Code)
	}
	if cookie, _ := login(t, s, nil); !signedIn(r.start(), cookie) {
		t.Fatal("a damaged file blocks new sessions")
	}
}
