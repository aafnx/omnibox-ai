package enginemgr

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, _ := w.Create(name)
		f.Write([]byte(body))
	}
	w.Close()
	return buf.Bytes()
}

func tgzOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

type fakeGitHub struct {
	t       *testing.T
	srv     *httptest.Server
	files   map[string][]byte
	digests map[string]string
	api     atomic.Int32
}

func newGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, files: map[string][]byte{}, digests: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			f.api.Add(1)
			json.NewEncoder(w).Encode(f.releases())
			return
		}
		body, ok := f.files[strings.TrimPrefix(r.URL.Path, "/dl/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) add(name string, body []byte) {
	f.files[name] = body
	sum := sha256.Sum256(body)
	f.digests[name] = "sha256:" + hex.EncodeToString(sum[:])
}

func (f *fakeGitHub) asset(name string) map[string]any {
	return map[string]any{"name": name, "browser_download_url": f.srv.URL + "/dl/" + name, "size": len(f.files[name]), "digest": f.digests[name]}
}

func (f *fakeGitHub) releases() []map[string]any {
	return []map[string]any{
		{"tag_name": "b4-pre", "prerelease": true, "published_at": "2026-10-05T00:00:00Z", "assets": []any{f.asset("llama-b4-bin-win-cuda-13.0-x64.zip"), f.asset("cudart-llama-bin-win-cuda-13.0-x64.zip")}},
		{"tag_name": "b3", "draft": true, "published_at": "2026-10-04T00:00:00Z", "assets": []any{}},
		{"tag_name": "b2", "published_at": "2026-10-02T00:00:00Z", "assets": []any{f.asset("llama-b2-bin-win-cuda-12.4-x64.zip"), f.asset("cudart-llama-bin-win-cuda-12.4-x64.zip"), f.asset("llama-b2-bin-win-cpu-x64.zip")}},
		{"tag_name": "b1", "published_at": "2026-09-20T00:00:00Z", "assets": []any{f.asset("llama-b1-bin-win-cpu-x64.zip")}},
		{"tag_name": "../evil", "published_at": "2026-09-19T00:00:00Z", "assets": []any{f.asset("llama-b2-bin-win-cuda-12.4-x64.zip"), f.asset("cudart-llama-bin-win-cuda-12.4-x64.zip")}},
	}
}

func (f *fakeGitHub) manager(t *testing.T) *Manager {
	return &Manager{DataDir: t.TempDir(), API: f.srv.URL, Download: func(string) string { return f.srv.URL + "/dl/" },
		Kinds: kinds("windows"), Check: func(context.Context, string) error { return nil }}
}

func TestReleasesPickTheBestWindowsBuild(t *testing.T) {
	gh := newGitHub(t)
	for _, n := range []string{"llama-b4-bin-win-cuda-13.0-x64.zip", "cudart-llama-bin-win-cuda-13.0-x64.zip", "llama-b2-bin-win-cuda-12.4-x64.zip", "cudart-llama-bin-win-cuda-12.4-x64.zip"} {
		gh.add(n, zipOf(t, map[string]string{"x.txt": n}))
	}
	m := gh.manager(t)
	releases, err := m.Releases(context.Background(), config.PrismML)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 2 || releases[0].Tag != "b4-pre" || !releases[0].Prerelease || releases[1].Tag != "b2" || len(releases[1].Assets) != 2 || !strings.Contains(releases[1].Assets[0].Name, "cuda-12") {
		t.Fatalf("%+v", releases)
	}
	if r, err := m.Find(context.Background(), config.PrismML, ""); err != nil || r.Tag != "b2" {
		t.Fatalf("latest stable: %+v %v", r, err)
	}
	if _, err := m.Find(context.Background(), config.PrismML, "b9"); err == nil {
		t.Fatal("unknown tag found")
	}
	if _, err := m.Find(context.Background(), config.PrismML, "../b2"); err == nil {
		t.Fatal("bad tag accepted")
	}
}

func TestInstallVerifiesUnpacksAndIsIdempotent(t *testing.T) {
	gh := newGitHub(t)
	gh.add("llama-b2-bin-win-cuda-12.4-x64.zip", zipOf(t, map[string]string{"llama-server.exe": "server", "ggml.dll": "lib"}))
	gh.add("cudart-llama-bin-win-cuda-12.4-x64.zip", zipOf(t, map[string]string{"cudart64_12.dll": "cuda"}))
	m := gh.manager(t)
	var checked string
	m.Check = func(_ context.Context, exe string) error { checked = exe; return nil }
	v, err := m.Install(context.Background(), config.PrismML, "b2")
	if err != nil {
		t.Fatal(err)
	}
	if v.Tag != "b2" || filepath.Base(v.Executable) != "llama-server.exe" || checked == "" || m.TagOf(config.PrismML, v.Executable) != "b2" {
		t.Fatalf("%+v checked %q", v, checked)
	}
	if _, err := os.Stat(filepath.Join(v.Dir, "cudart64_12.dll")); err != nil {
		t.Fatal("cudart archive not unpacked next to the server")
	}
	calls := gh.api.Load()
	if again, err := m.Install(context.Background(), config.PrismML, "b2"); err != nil || again.Dir != v.Dir {
		t.Fatalf("reinstall: %+v %v", again, err)
	}
	if installed := m.Installed(config.PrismML); len(installed) != 1 {
		t.Fatalf("%+v", installed)
	}
	if gh.api.Load() != calls+1 {
		t.Fatal("reinstall did not use the release list once")
	}
	if m.TagOf(config.PrismML, `C:\tools\bonsai\llama-server.exe`) != "" {
		t.Fatal("outside executable counted as managed")
	}
	entries, _ := os.ReadDir(filepath.Join(m.DataDir, "engines"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-") {
			t.Fatal("staging directory left behind")
		}
	}
}

func TestInstallRefusesBadDownloads(t *testing.T) {
	gh := newGitHub(t)
	gh.add("llama-b2-bin-win-cuda-12.4-x64.zip", zipOf(t, map[string]string{"llama-server.exe": "server"}))
	gh.add("cudart-llama-bin-win-cuda-12.4-x64.zip", zipOf(t, map[string]string{"cudart.dll": "cuda"}))
	m := gh.manager(t)

	good := gh.files["cudart-llama-bin-win-cuda-12.4-x64.zip"]
	gh.files["cudart-llama-bin-win-cuda-12.4-x64.zip"] = append(append([]byte{}, good[:len(good)-1]...), good[len(good)-1]^1)
	if _, err := m.Install(context.Background(), config.PrismML, "b2"); err == nil || !strings.Contains(err.Error(), "Контрольная сумма") {
		t.Fatalf("corrupt file: %v", err)
	}
	gh.files["cudart-llama-bin-win-cuda-12.4-x64.zip"] = good

	m.Download = func(string) string { return "https://github.com/PrismML-Eng/llama.cpp/releases/download/" }
	if _, err := m.Install(context.Background(), config.PrismML, "b2"); err == nil || !strings.Contains(err.Error(), "не указывает на выпуск GitHub") {
		t.Fatalf("foreign URL: %v", err)
	}
	m.Download = func(string) string { return gh.srv.URL + "/dl/" }

	m.Check = func(context.Context, string) error {
		return errors.New("Движок не запустился: missing cudart")
	}
	if _, err := m.Install(context.Background(), config.PrismML, "b2"); err == nil || !strings.Contains(err.Error(), "не запустился") {
		t.Fatalf("broken binary: %v", err)
	}
	if len(m.Installed(config.PrismML)) != 0 {
		t.Fatal("a failed install left a version")
	}
}

func TestExtractRefusesEscapingEntries(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string][]byte{
		"bad.zip":    zipOf(t, map[string]string{"../outside.txt": "x"}),
		"bad.tar.gz": tgzOf(t, map[string]string{"../../outside.txt": "x"}),
	} {
		archive := filepath.Join(dir, name)
		os.WriteFile(archive, data, 0o644)
		if err := Extract(archive, filepath.Join(dir, "out")); err == nil {
			t.Fatalf("%s: escaping entry accepted", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "outside.txt")); err == nil {
		t.Fatal("file written outside the target")
	}
	archive := filepath.Join(dir, "ok.tgz")
	os.WriteFile(archive, tgzOf(t, map[string]string{"bin/ollama": "x", "lib/ollama/libggml.so": "y"}), 0o644)
	if err := Extract(archive, filepath.Join(dir, "ok")); err != nil {
		t.Fatal(err)
	}
	if findExe(filepath.Join(dir, "ok"), "ollama") == "" {
		t.Fatal("executable not found in tar.gz")
	}
}

func TestRemoveUnusedKeepsCurrentAndRollbackVersions(t *testing.T) {
	m := &Manager{DataDir: t.TempDir()}
	for _, tag := range []string{"b1", "b2", "b3"} {
		dir := filepath.Join(m.DataDir, "engines", "prismml", tag)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "llama-server.exe"), []byte("x"), 0o755)
		data, _ := json.Marshal(manifest{SchemaVersion: 1, Tag: tag, Executable: "llama-server.exe"})
		os.WriteFile(filepath.Join(dir, manifestName), data, 0o644)
	}
	exe := func(tag string) string {
		return filepath.Join(m.DataDir, "engines", "prismml", tag, "llama-server.exe")
	}
	e := &config.Engine{Launch: config.Launch{Executable: exe("b3")}, Previous: &config.Launch{Executable: exe("b2")}}
	removed := m.RemoveUnused(config.PrismML, e)
	if len(removed) != 1 || removed[0] != "b1" || len(m.Installed(config.PrismML)) != 2 {
		t.Fatalf("%v", removed)
	}
}
