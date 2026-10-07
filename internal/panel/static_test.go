package panel

import (
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
	for path, want := range map[string]string{"/": "no-cache", "/app.js": "no-cache", "/style.css": "no-cache", "/inter-400.woff": "max-age=", "/brand-dark.svg": "max-age="} {
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
