package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/aafnx/omnibox-ai/internal/catalog"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// toServer sends every request to the test server, as if it were huggingface.co.
type toServer struct{ target *url.URL }

func (t toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host = t.target.Scheme, t.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

func TestDownloadGGUFFromHuggingFace(t *testing.T) {
	r := newRig(t)
	content := append([]byte("GGUF"), make([]byte, 5000)...)
	sum := sha256.Sum256(content)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.Write(content)
	}))
	defer srv.Close()
	target, _ := url.Parse(srv.URL)
	r.c.HTTP = &http.Client{Transport: toServer{target}}
	link := "https://huggingface.co/author/model/resolve/0123456789abcdef0123456789abcdef01234567/model-Q4_K_M.gguf"
	var progress [][2]int64
	ctx := context.WithValue(context.Background(), progressKey{}, func(done, total int64) { progress = append(progress, [2]int64{done, total}) })
	message, err := r.c.Do(ctx, Request{Action: "download", Engine: config.PrismML, URL: link, Filename: "author-model-Q4_K_M.gguf", SHA256: hex.EncodeToString(sum[:]), Size: strconv.Itoa(len(content))})
	if err != nil || !strings.Contains(message, "Скачано") {
		t.Fatalf("%s %v", message, err)
	}
	dir := r.store.Get().Engines[config.PrismML].ModelsDirectory
	if _, err := os.Stat(filepath.Join(dir, "author-model-Q4_K_M.gguf")); err != nil {
		t.Fatal("model not in the models directory")
	}
	if len(progress) == 0 || progress[len(progress)-1] != [2]int64{int64(len(content)), int64(len(content))} {
		t.Fatalf("progress %v", progress)
	}
	for _, bad := range []Request{
		{Action: "download", Engine: config.Ollama, URL: link, Filename: "x.gguf"},
		{Action: "download", Engine: config.PrismML, URL: "https://evil.example/a/b/resolve/main/x.gguf", Filename: "x.gguf"},
		{Action: "download", Engine: config.PrismML, URL: link, Filename: "x.gguf", SHA256: "xyz"},
	} {
		if _, err := r.c.Do(context.Background(), bad); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
}

func TestPullStreamsProgressAndReportsErrors(t *testing.T) {
	r := newRig(t)
	r.services.Set(config.ServiceName(config.Ollama), platform.Running)
	var mu sync.Mutex
	var last [2]int64
	ctx := context.WithValue(context.Background(), progressKey{}, func(done, total int64) { mu.Lock(); last = [2]int64{done, total}; mu.Unlock() })
	if _, err := r.c.Do(ctx, Request{Action: "pull", Engine: config.Ollama, Model: "qwen3:8b"}); err != nil {
		t.Fatal(err)
	}
	if last != [2]int64{100, 100} {
		t.Fatalf("progress %v", last)
	}
	if _, err := r.c.Do(ctx, Request{Action: "pull", Engine: config.Ollama, Model: "broken:1b"}); err == nil || !strings.Contains(err.Error(), "file does not exist") {
		t.Fatalf("%v", err)
	}
}

func TestOperationsCancelOnlyDownloadsAndShowProgress(t *testing.T) {
	started := make(chan struct{}, 2)
	ops := &Operations{Run: func(ctx context.Context, r Request) (string, error) {
		Progress(ctx, 10, 100)
		started <- struct{}{}
		if r.Action == "download" {
			<-ctx.Done()
			return "", catalog.ErrCanceled
		}
		time.Sleep(50 * time.Millisecond)
		return "ok", nil
	}}
	download, _ := ops.Start(Request{Action: "download", Engine: config.PrismML})
	control, _ := ops.Start(Request{Action: "stop", Engine: config.PrismML})
	<-started
	<-started
	list := ops.List()
	for _, op := range list {
		if op.ID == download && (op.Done != 10 || op.Total != 100) {
			t.Fatalf("progress not shown: %+v", op)
		}
	}
	if err := ops.Cancel(control); !errors.Is(err, ErrNotCancelable) {
		t.Fatalf("control operation canceled: %v", err)
	}
	if err := ops.Cancel(download); err != nil {
		t.Fatal(err)
	}
	for ops.Running() {
		time.Sleep(5 * time.Millisecond)
	}
	for _, op := range ops.List() {
		if op.ID == download && (op.State != "failed" || !strings.Contains(op.Message, "отменено")) {
			t.Fatalf("%+v", op)
		}
	}
	if err := ops.Cancel(download); err == nil {
		t.Fatal("a finished operation was canceled")
	}
}
