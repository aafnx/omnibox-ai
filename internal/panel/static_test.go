package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
)

func TestStaticFilesAreRevalidatedOrCached(t *testing.T) {
	s := newServer(t, testConfig(t, nil))
	for path, want := range map[string]string{"/": "no-cache", "/app.js": "no-cache", "/style.css": "no-cache", "/inter-400.woff": "max-age=", "/brand-dark.svg": "max-age=",
		"/manifest.webmanifest": "no-cache", "/sw.js": "no-cache", "/icon-512.png": "max-age="} {
		w := call(s, "GET", path, "", nil)
		etag := w.Header().Get("ETag")
		if w.Code != 200 || etag == "" || !strings.Contains(w.Header().Get("Cache-Control"), want) || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: %d %v", path, w.Code, w.Header())
		}
		again := call(s, "GET", path, "", func(r *http.Request) { r.Header.Set("If-None-Match", etag) })
		if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
			t.Fatalf("%s revalidation: %d", path, again.Code)
		}
		// A new version has another ETag, so the browser gets the new script at once.
		if changed := call(s, "GET", path, "", func(r *http.Request) { r.Header.Set("If-None-Match", `"0000"`) }); changed.Code != 200 {
			t.Fatalf("%s with an old ETag: %d", path, changed.Code)
		}
	}
	if w := call(s, "GET", "/api/session", "", nil); w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("API responses must not be cached: %v", w.Header())
	}
}

func TestSystemFailuresAre500AndLogged(t *testing.T) {
	c := testConfig(t, nil)
	log := filepath.Join(t.TempDir(), "logs", "panel.log")
	var fail error
	s, err := New(Options{Config: func() *config.Config { return c }, LogPath: log,
		Action: func(map[string]any) (any, error) { return nil, fail }})
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := login(t, s, nil)
	post := func() map[string]any {
		w := call(s, "POST", "/api/actions", `{"action":"start"}`, func(r *http.Request) { r.AddCookie(cookie); r.Header.Set("X-Omnibox-CSRF", csrf) })
		v := decode(t, w)
		v["code"] = w.Code
		return v
	}
	fail = errors.New("Неизвестный движок.")
	if v := post(); v["code"] != 400 || v["error"] != "Неизвестный движок." {
		t.Fatalf("validation: %v", v)
	}
	fail = fmt.Errorf("сохранение настроек: %w", &fs.PathError{Op: "open", Path: `C:\ProgramData\OmniBox AI\config.json.tmp`, Err: errors.New("disk full")})
	v := post()
	if v["code"] != 500 || strings.Contains(v["error"].(string), "ProgramData") {
		t.Fatalf("system failure: %v", v)
	}
	logged, _ := os.ReadFile(log)
	if !strings.Contains(string(logged), "disk full") || !strings.Contains(string(logged), "/api/actions") {
		t.Fatalf("panel log: %q", logged)
	}
}

func TestContentSearchAndExportRoutes(t *testing.T) {
	c := testConfig(t, nil)
	s, _ := New(Options{Config: func() *config.Config { return c },
		SearchContent: func(q, session string) any { return []string{q + "|" + session} },
		ExportSession: func(session string) ([]byte, bool) { return []byte("{}\n"), session == "0123456789abcdef01234567" }})
	cookie, _ := login(t, s, nil)
	auth := func(r *http.Request) { r.AddCookie(cookie) }
	if w := call(s, "GET", "/api/request-content?q=%D0%B1%D0%B0%D0%B9", "", auth); w.Code != 200 || !strings.Contains(w.Body.String(), "бай|") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w := call(s, "GET", "/api/request-content?session=0123456789abcdef01234567&export=1", "", auth)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "omnibox-session-0123456789ab.jsonl") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	if w := call(s, "GET", "/api/request-content?session=x&export=1", "", auth); w.Code != 400 {
		t.Fatalf("%d", w.Code)
	}
	if w := call(s, "GET", "/api/request-content?session=0123456789abcdef01234567&export=1", "", nil); w.Code != 401 {
		t.Fatalf("export without a session: %d", w.Code)
	}
}

func TestPanelIsInstallable(t *testing.T) {
	s := newServer(t, testConfig(t, nil))
	w := call(s, "GET", "/manifest.webmanifest", "", nil)
	var m struct {
		StartURL string `json:"start_url"`
		Display  string
		Icons    []struct{ Src, Purpose string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || m.StartURL != "/" || m.Display != "standalone" || len(m.Icons) != 3 {
		t.Fatalf("%v %+v", err, m)
	}
	for _, icon := range m.Icons {
		if r := call(s, "GET", icon.Src, "", nil); r.Code != 200 || r.Header().Get("Content-Type") != "image/png" || !bytes.HasPrefix(r.Body.Bytes(), []byte("\x89PNG")) {
			t.Fatalf("%s: %d", icon.Src, r.Code)
		}
	}
	// Tab icons for browsers without SVG icons, and the /favicon.ico every browser asks for.
	for _, path := range []string{"/favicon.ico", "/favicon-32.png"} {
		if r := call(s, "GET", path, "", nil); r.Code != 200 || r.Header().Get("Content-Type") != "image/png" || !bytes.HasPrefix(r.Body.Bytes(), []byte("\x89PNG")) {
			t.Fatalf("%s: %d", path, r.Code)
		}
	}
	if page := call(s, "GET", "/", "", nil).Body.String(); !strings.Contains(page, `href="/favicon-32.png"`) || !strings.Contains(page, `href="/favicon.svg"`) {
		t.Fatal("the page does not link its tab icons")
	}
	if !strings.Contains(call(s, "GET", "/", "", nil).Body.String(), `rel="manifest"`) {
		t.Fatal("the page does not link the manifest")
	}
	if sw := call(s, "GET", "/sw.js", "", nil).Body.String(); !strings.Contains(sw, "fetch") || strings.Contains(sw, "caches.open") {
		t.Fatal("the service worker must not cache panel data")
	}
}
