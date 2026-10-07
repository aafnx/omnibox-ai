package selfupdate

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

type fakeGitHub struct {
	*httptest.Server
	mu      sync.Mutex
	leaked  bool
	archive []byte
	sums    string
}

func newGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, _ := z.Create("omnibox.exe")
	w.Write([]byte("new binary"))
	z.Close()
	g := &fakeGitHub{archive: buf.Bytes()}
	sum := sha256.Sum256(g.archive)
	g.sums = hex.EncodeToString(sum[:]) + "  omnibox-windows-amd64.zip\n" + strings.Repeat("0", 64) + "  install.ps1\n"
	g.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/storage/") {
			// Signed storage must never see the token.
			if r.Header.Get("Authorization") != "" {
				g.mu.Lock()
				g.leaked = true
				g.mu.Unlock()
			}
			if strings.HasSuffix(r.URL.Path, "SHA256SUMS") {
				w.Write([]byte(g.sums))
			} else {
				w.Write(g.archive)
			}
			return
		}
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch r.URL.Path {
		case "/repos/aafnx/omnibox-ai/releases/latest", "/repos/aafnx/omnibox-ai/releases/tags/v0.19.0":
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.19.0", "assets": []map[string]string{
				{"name": "omnibox-windows-amd64.zip", "url": g.URL + "/assets/1"},
				{"name": "SHA256SUMS", "url": g.URL + "/assets/2"},
			}})
		case "/assets/1":
			http.Redirect(w, r, g.URL+"/storage/omnibox-windows-amd64.zip", http.StatusFound)
		case "/assets/2":
			http.Redirect(w, r, g.URL+"/storage/SHA256SUMS", http.StatusFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *fakeGitHub) updater(token string) *Updater {
	return &Updater{Token: token, API: g.URL, GOOS: "windows", HTTP: g.Client()}
}

func TestFetchChecksTheReleaseAndKeepsTheTokenAtGitHub(t *testing.T) {
	g := newGitHub(t)
	u := g.updater("secret-token")
	r, err := u.Find(context.Background(), "")
	if err != nil || r.Version() != "0.19.0" {
		t.Fatalf("%+v %v", r, err)
	}
	exe, err := u.Fetch(context.Background(), r, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new binary" {
		t.Fatalf("%q", b)
	}
	if g.leaked {
		t.Fatal("the token was sent to the storage server")
	}
}

func TestWrongChecksumTokenOrVersionIsRefused(t *testing.T) {
	g := newGitHub(t)
	if _, err := g.updater("wrong").Find(context.Background(), ""); err == nil {
		t.Fatal("a release found with a wrong token")
	}
	if _, err := g.updater("secret-token").Find(context.Background(), "0.20.0"); err == nil || !strings.Contains(err.Error(), "0.20.0") {
		t.Fatalf("%v", err)
	}
	if _, err := g.updater("secret-token").Find(context.Background(), "../../x"); err == nil {
		t.Fatal("a malformed version accepted")
	}
	u := g.updater("secret-token")
	r, _ := u.Find(context.Background(), "0.19.0")
	g.sums = strings.Repeat("a", 64) + "  omnibox-windows-amd64.zip\n"
	if _, err := u.Fetch(context.Background(), r, t.TempDir()); err == nil || !strings.Contains(err.Error(), "Контрольная сумма") {
		t.Fatalf("%v", err)
	}
}
