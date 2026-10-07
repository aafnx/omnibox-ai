package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fileServer serves one model file with ranges, ETag/If-Range, cuts and stalls.
type fileServer struct {
	mu      sync.Mutex
	content []byte
	etag    string
	cutAt   int  // abort the connection after this many bytes of the body (0: never)
	stall   bool // send headers and a few bytes, then nothing
	ranges  []string
	srv     *httptest.Server
}

func newFileServer(t *testing.T, content []byte) *fileServer {
	f := &fileServer{content: content, etag: `"v1"`}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fileServer) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	content, etag, cut, stall := f.content, f.etag, f.cutAt, f.stall
	f.ranges = append(f.ranges, r.Header.Get("Range"))
	f.mu.Unlock()
	w.Header().Set("ETag", etag)
	start := 0
	if rng := r.Header.Get("Range"); rng != "" && (r.Header.Get("If-Range") == "" || r.Header.Get("If-Range") == etag) {
		start, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"))
		if start >= len(content) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Range", "bytes "+strconv.Itoa(start)+"-"+strconv.Itoa(len(content)-1)+"/"+strconv.Itoa(len(content)))
		w.Header().Set("Content-Length", strconv.Itoa(len(content)-start))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	}
	body := content[start:]
	if stall {
		w.Write(body[:4])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return
	}
	if cut > 0 && cut < len(body) {
		w.Write(body[:cut])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}
	w.Write(body)
}

func gguf(n int, fill byte) []byte {
	b := make([]byte, n)
	copy(b, "GGUF")
	for i := 4; i < n; i++ {
		b[i] = fill + byte(i%7)
	}
	return b
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func (f *fileServer) download(t *testing.T, dir, path string, content []byte) *Download {
	u, _ := url.Parse(f.srv.URL + path)
	return &Download{URL: u, Dir: dir, Filename: "author-model-Q4_K_M.gguf", SHA256: sum(content), Size: int64(len(content)), HTTP: f.srv.Client(), Idle: time.Second}
}

const pinned = "/author/model/resolve/0123456789abcdef0123456789abcdef01234567/model-Q4_K_M.gguf"

func parts(t *testing.T, dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if PartPattern.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestDownloadResumesFromTheSameByte(t *testing.T) {
	content := gguf(300000, 1)
	f := newFileServer(t, content)
	f.cutAt = 120000
	dir := t.TempDir()
	var progress []int64
	d := f.download(t, dir, pinned, content)
	d.Progress = func(done, total int64) { progress = append(progress, done) }
	err := d.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "продолжится") || len(parts(t, dir)) != 2 {
		t.Fatalf("interrupted download: %v %v", err, parts(t, dir))
	}
	f.cutAt = 0
	if err := d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, d.Filename))
	if sum(got) != sum(content) || len(parts(t, dir)) != 0 {
		t.Fatalf("file differs or parts left: %v", parts(t, dir))
	}
	if f.ranges[1] != "bytes=120000-" || progress[len(progress)-1] != int64(len(content)) {
		t.Fatalf("ranges %q, progress %v", f.ranges, progress)
	}
	if err := d.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "уже существует") {
		t.Fatalf("overwrite: %v", err)
	}
}

func TestChangedFileStartsOver(t *testing.T) {
	first := gguf(200000, 1)
	f := newFileServer(t, first)
	f.cutAt = 50000
	dir := t.TempDir()
	d := f.download(t, dir, "/author/model/resolve/main/model-Q4_K_M.gguf", first)
	d.SHA256, d.Size = "", 0
	d.Run(context.Background())
	second := gguf(210000, 9)
	f.content, f.etag, f.cutAt = second, `"v2"`, 0
	if err := d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, d.Filename))
	if sum(got) != sum(second) {
		t.Fatal("a changed file was stitched to the old part")
	}
}

func TestUnpinnedLinkWithoutETagDoesNotResume(t *testing.T) {
	content := gguf(100000, 3)
	f := newFileServer(t, content)
	f.etag = ""
	f.cutAt = 40000
	dir := t.TempDir()
	d := f.download(t, dir, "/author/model/resolve/main/model-Q4_K_M.gguf", content)
	d.Run(context.Background())
	f.cutAt = 0
	if err := d.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.ranges[1] != "" {
		t.Fatalf("resumed without proof the file is the same: %q", f.ranges)
	}
}

func TestCancelRemovesPartAndStallKeepsIt(t *testing.T) {
	content := gguf(100000, 5)
	f := newFileServer(t, content)
	f.stall = true
	dir := t.TempDir()
	d := f.download(t, dir, pinned, content)
	d.Idle = 300 * time.Millisecond
	if err := d.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "нет данных") || len(parts(t, dir)) != 2 {
		t.Fatalf("stall: %v %v", err, parts(t, dir))
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.Idle = 10 * time.Second
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	if err := d.Run(ctx); !errors.Is(err, ErrCanceled) || len(parts(t, dir)) != 0 {
		t.Fatalf("cancel: %v %v", err, parts(t, dir))
	}
}

func TestVerificationFailuresRemoveTheFile(t *testing.T) {
	dir := t.TempDir()
	notGGUF := []byte(strings.Repeat("x", 1000))
	f := newFileServer(t, notGGUF)
	if err := f.download(t, dir, pinned, notGGUF).Run(context.Background()); err == nil || !strings.Contains(err.Error(), "заголовка GGUF") {
		t.Fatalf("header: %v", err)
	}
	content := gguf(1000, 2)
	f.content = content
	d := f.download(t, dir, pinned, content)
	d.SHA256 = strings.Repeat("0", 64)
	if err := d.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "SHA256") {
		t.Fatalf("sha: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files left: %v", entries)
	}
	d.Filename = "../escape.gguf"
	if err := d.Run(context.Background()); err == nil {
		t.Fatal("path in file name accepted")
	}
}

func TestModelURLAcceptsOnlyHuggingFaceFiles(t *testing.T) {
	for _, bad := range []string{
		"http://huggingface.co/a/b/resolve/main/m.gguf", "https://evil.example/a/b/resolve/main/m.gguf",
		"https://huggingface.co/a/b/blob/main/m.gguf", "https://huggingface.co/a/b/resolve/main/m.bin",
		"https://user@huggingface.co/a/b/resolve/main/m.gguf", "https://huggingface.co/a/b/resolve/main/../../x.gguf",
	} {
		if _, err := ModelURL(bad); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
	u, err := ModelURL("https://huggingface.co" + pinned)
	if err != nil || !Pinned(u) {
		t.Fatalf("%v", err)
	}
}

func TestSearchAndFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/models" {
			if r.URL.Query().Get("filter") != "gguf" || r.URL.Query().Get("search") != "qwen 3" {
				t.Errorf("query %v", r.URL.Query())
			}
			json.NewEncoder(w).Encode([]map[string]any{{"id": "Qwen/Qwen3-8B-GGUF", "downloads": 10, "gated": false}, {"id": "../x/y", "downloads": 1}, {"id": "a/b", "gated": "auto"}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"sha": "0123456789abcdef0123456789abcdef01234567", "siblings": []map[string]any{
			{"rfilename": "Qwen3-8B-Q4_K_M.gguf", "size": 5000, "lfs": map[string]any{"sha256": strings.Repeat("a", 64)}},
			{"rfilename": "mmproj-F16.gguf"}, {"rfilename": "README.md"},
			{"rfilename": "big/Qwen3-8B-00001-of-00002.gguf"}, {"rfilename": "Bonsai-2-PQ2.gguf"},
		}})
	}))
	defer srv.Close()
	h := &Hub{Base: srv.URL}
	models, err := h.Search(context.Background(), "qwen 3")
	if err != nil || len(models) != 2 || models[0].ID != "Qwen/Qwen3-8B-GGUF" || models[0].Gated || !models[1].Gated {
		t.Fatalf("%+v %v", models, err)
	}
	if _, err := h.Search(context.Background(), "q"); err == nil {
		t.Fatal("short query accepted")
	}
	files, err := h.Files(context.Background(), "Qwen/Qwen3-8B-GGUF")
	if err != nil || len(files) != 3 {
		t.Fatalf("%+v %v", files, err)
	}
	if files[0].URL != "https://huggingface.co/Qwen/Qwen3-8B-GGUF/resolve/0123456789abcdef0123456789abcdef01234567/Qwen3-8B-Q4_K_M.gguf" || files[0].Filename != "Qwen-Qwen3-8B-GGUF-Qwen3-8B-Q4_K_M.gguf" || *files[0].Bytes != 5000 {
		t.Fatalf("%+v", files[0])
	}
	if !files[1].Split || !files[2].PrismOnly {
		t.Fatalf("%+v", files)
	}
	if _, err := h.Files(context.Background(), "../etc"); err == nil {
		t.Fatal("bad repo accepted")
	}
}
