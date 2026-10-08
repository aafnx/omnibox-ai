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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/catalog"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/platform"
	"github.com/aafnx/omnibox-ai/internal/progress"
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
	var steps [][2]int64
	ctx := progress.With(context.Background(), func(done, total int64) { steps = append(steps, [2]int64{done, total}) })
	message, err := r.c.Do(ctx, Request{Action: "download", Engine: config.PrismML, URL: link, Filename: "author-model-Q4_K_M.gguf", SHA256: hex.EncodeToString(sum[:]), Size: strconv.Itoa(len(content))})
	if err != nil || !strings.Contains(message, "Скачано") {
		t.Fatalf("%s %v", message, err)
	}
	dir := r.store.Get().Engines[config.PrismML].ModelsDirectory
	if _, err := os.Stat(filepath.Join(dir, "author-model-Q4_K_M.gguf")); err != nil {
		t.Fatal("model not in the models directory")
	}
	if len(steps) == 0 || steps[len(steps)-1] != [2]int64{int64(len(content)), int64(len(content))} {
		t.Fatalf("progress %v", steps)
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
	ctx := progress.With(context.Background(), func(done, total int64) { mu.Lock(); last = [2]int64{done, total}; mu.Unlock() })
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

func TestInstalledModelsShowSourceAndUse(t *testing.T) {
	r := newRig(t)
	r.c.DataDir = t.TempDir()
	content := append([]byte("GGUF"), make([]byte, 100)...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.Write(content) }))
	defer srv.Close()
	target, _ := url.Parse(srv.URL)
	r.c.HTTP = &http.Client{Transport: toServer{target}}
	link := "https://huggingface.co/unsloth/gemma-3-12b-it-GGUF/resolve/main/gemma-3-12b-it-Q4_K_M.gguf"
	if _, err := r.c.Do(context.Background(), Request{Action: "download", Engine: config.PrismML, URL: link, Filename: "unsloth-gemma-3-12b-it-GGUF-gemma-3-12b-it-Q4_K_M.gguf"}); err != nil {
		t.Fatal(err)
	}
	models, err := r.c.InstalledModels(context.Background(), config.PrismML)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]engine.Details{}
	for _, m := range models {
		byName[m.Name] = m
	}
	got := byName["unsloth-gemma-3-12b-it-GGUF-gemma-3-12b-it-Q4_K_M.gguf"]
	if got.Source != "https://huggingface.co/unsloth/gemma-3-12b-it-GGUF" || got.SourceSearch || got.InUse || got.Params != "12B" {
		t.Fatalf("%+v", got)
	}
	// A model from before has a search link; the selected one cannot be deleted.
	if good := byName["good-Q4_K_M.gguf"]; !good.Selected || !good.InUse || !good.SourceSearch || !strings.Contains(good.Source, "search=good") {
		t.Fatalf("%+v", good)
	}
	// A removed model leaves the sources file.
	os.Remove(got.ID)
	if _, ok := r.c.sources()[got.ID]; ok {
		t.Fatal("a removed model kept its source")
	}
	if _, err := r.c.InstalledModels(context.Background(), config.Ollama); err == nil {
		t.Fatal("a stopped Ollama listed its models")
	}
}

func TestModelDownloadBringsItsProjector(t *testing.T) {
	r := newRig(t)
	r.c.DataDir = t.TempDir()
	content := append([]byte("GGUF"), make([]byte, 64)...)
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { asked = append(asked, req.URL.Path); w.Write(content) }))
	defer srv.Close()
	target, _ := url.Parse(srv.URL)
	r.c.HTTP = &http.Client{Transport: toServer{target}}
	base := "https://huggingface.co/unsloth/gemma-GGUF/resolve/main/"
	msg, err := r.c.Do(context.Background(), Request{Action: "download", Engine: config.PrismML, URL: base + "gemma-Q4_K_M.gguf", Filename: "unsloth-gemma-GGUF-gemma-Q4_K_M.gguf",
		ProjectorURL: base + "mmproj-F16.gguf", ProjectorFilename: "unsloth-gemma-GGUF-mmproj-F16.gguf"})
	if err != nil || !strings.Contains(msg, "проектор") || len(asked) != 2 {
		t.Fatalf("%q %v %v", msg, err, asked)
	}
	models, _ := r.c.InstalledModels(context.Background(), config.PrismML)
	found := false
	for _, m := range models {
		if m.Name == "unsloth-gemma-GGUF-gemma-Q4_K_M.gguf" {
			found = m.Projector != "" && slices.Contains(m.Capabilities, "vision")
		}
		if strings.Contains(m.Name, "mmproj") {
			t.Fatal("a projector is listed as a model")
		}
	}
	if !found {
		t.Fatalf("the model has no vision: %+v", models)
	}
	// A projector alone, for the selected model, is chosen in its settings for the next start.
	selected := r.store.Get().Engines[config.PrismML].Settings["model"]
	msg, err = r.c.Do(context.Background(), Request{Action: "download", Engine: config.PrismML, URL: "https://huggingface.co/prism-ml/good-gguf/resolve/main/mmproj-good-F16.gguf",
		Filename: "prism-ml-good-gguf-mmproj-good-F16.gguf", ForModel: selected})
	e := r.store.Get().Engines[config.PrismML]
	if err != nil || !strings.HasSuffix(e.Settings["mmproj"], "prism-ml-good-gguf-mmproj-good-F16.gguf") || !strings.Contains(msg, "выбран") {
		t.Fatalf("%q %v %v", msg, err, e.Settings)
	}
	// A projector under a model's name, or a model under a projector's, is refused.
	if _, err := r.c.Do(context.Background(), Request{Action: "download", Engine: config.PrismML, URL: base + "x.gguf", Filename: "plain-model.gguf", ProjectorURL: base + "y.gguf", ProjectorFilename: "plain-vision.gguf"}); err == nil {
		t.Fatal("a projector without mmproj in its name was saved")
	}
}
