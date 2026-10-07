package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/selfupdate"
)

func releaseServer(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, _ := z.Create("omnibox.exe")
	w.Write([]byte("new"))
	z.Close()
	sum := sha256.Sum256(buf.Bytes())
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/releases/"):
			json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": []map[string]string{
				{"name": "omnibox-windows-amd64.zip", "url": srv.URL + "/zip"}, {"name": "SHA256SUMS", "url": srv.URL + "/sums"}}})
		case r.URL.Path == "/zip":
			w.Write(buf.Bytes())
		case r.URL.Path == "/sums":
			io.WriteString(w, hex.EncodeToString(sum[:])+"  omnibox-windows-amd64.zip\n")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testUpdater(t *testing.T, srv *httptest.Server, installed *string) updater {
	return updater{
		Token: func() (string, error) { return "token", nil },
		New: func(token string) *selfupdate.Updater {
			return &selfupdate.Updater{Token: token, API: srv.URL, GOOS: "windows", HTTP: srv.Client()}
		},
		Install: func(exe string, _, _ io.Writer) int {
			b, _ := os.ReadFile(exe)
			*installed = string(b)
			return 0
		},
		TempDir: func() (string, error) { return t.TempDir(), nil },
		Current: "0.19.0",
	}
}

func TestAiUpdateInstallsANewerReleaseOnly(t *testing.T) {
	var installed string
	var out, errOut bytes.Buffer
	same := testUpdater(t, releaseServer(t, "v0.19.0"), &installed)
	if code := runUpdate(nil, same, &out, &errOut); code != 0 || installed != "" || !strings.Contains(out.String(), "последняя версия 0.19.0") {
		t.Fatalf("%d %q %s %s", code, installed, out.String(), errOut.String())
	}
	// Naming the version reinstalls it (or rolls back to an older one).
	if code := runUpdate([]string{"0.19.0"}, same, &out, &errOut); code != 0 || installed != "new" {
		t.Fatalf("explicit version: %d %q %s", code, installed, errOut.String())
	}
	installed = ""
	newer := testUpdater(t, releaseServer(t, "v0.20.0"), &installed)
	if code := runUpdate(nil, newer, &out, &errOut); code != 0 || installed != "new" {
		t.Fatalf("newer: %d %q %s", code, installed, errOut.String())
	}
}

func TestAiUpdateStopsWithoutAToken(t *testing.T) {
	var installed string
	u := testUpdater(t, releaseServer(t, "v0.20.0"), &installed)
	u.Token = func() (string, error) {
		return "", errors.New("Token не введён: обновление отменено.")
	}
	var out, errOut bytes.Buffer
	if code := runUpdate(nil, u, &out, &errOut); code != 1 || installed != "" || !strings.Contains(errOut.String(), "Token не введён") {
		t.Fatalf("%d %s", code, errOut.String())
	}
	if code := runUpdate([]string{"1", "2"}, u, &out, &errOut); code != 2 {
		t.Fatalf("extra arguments: %d", code)
	}
}
