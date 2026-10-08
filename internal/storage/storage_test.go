package storage

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

func write(t *testing.T, path string, size int, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if !mod.IsZero() {
		os.Chtimes(path, mod, mod)
	}
}

func setup(t *testing.T) (string, *config.Store) {
	t.Helper()
	data := t.TempDir()
	cfg := config.Default()
	cfg.Panel.Hosts = []string{"localhost"}
	b64 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg.Panel.Password = config.Password{Salt: b64, Hash: b64, Iterations: 210000}
	cfg.Engines[config.PrismML] = &config.Engine{Port: 8801, ModelsDirectory: filepath.Join(data, "models", "prismml"), Launch: config.Launch{Settings: map[string]string{}}}
	cfg.Engines[config.Ollama] = &config.Engine{Port: 8802, ModelsDirectory: filepath.Join(data, "models", "ollama"), Launch: config.Launch{Settings: map[string]string{}}}
	path := filepath.Join(data, "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	return data, config.NewStore(path, cfg)
}

func TestUsageCountsLogsModelsAndDrives(t *testing.T) {
	data, store := setup(t)
	write(t, filepath.Join(data, "logs", "engines", "prismml", "out.log"), 100, time.Time{})
	write(t, filepath.Join(data, "logs", "engines", "prismml", "err.log.1"), 50, time.Time{})
	write(t, filepath.Join(data, "logs", "engines", "prismml", "notes.txt"), 999, time.Time{})
	write(t, filepath.Join(data, "logs", "requests", "requests.jsonl"), 10, time.Time{})
	write(t, filepath.Join(data, "logs", "requests", "content.jsonl.2"), 7, time.Time{})
	write(t, filepath.Join(data, "models", "prismml", "a-Q4_K_M.gguf"), 300, time.Time{})
	write(t, filepath.Join(data, "models", "prismml", "b-00001-of-00002.gguf"), 200, time.Time{})
	write(t, filepath.Join(data, "models", "prismml", "b-00002-of-00002.gguf"), 200, time.Time{})
	write(t, filepath.Join(data, "models", "ollama", "blobs", "sha256-1"), 400, time.Time{})
	write(t, filepath.Join(data, "models", "ollama", "manifests", "registry.ollama.ai", "library", "qwen3", "8b"), 1, time.Time{})
	write(t, filepath.Join(data, "engines", "prismml", "b1", "llama-server"), 60, time.Time{})
	write(t, filepath.Join(data, "engines", ".staging-0123456789abcdef0123456789abcdef", "x.zip"), 40, time.Time{})
	root := string(filepath.Separator)
	if v := filepath.VolumeName(data); v != "" {
		root = v + root
	}
	listDrives = func() []platform.Drive {
		return []platform.Drive{{Name: "data", Root: root, Total: 100 << 30, Free: 5 << 30}, {Name: "other", Root: filepath.Join(root, "nowhere") + string(filepath.Separator), Total: 50 << 30, Free: 40 << 30}}
	}
	defer func() { listDrives = platform.Drives }()
	u := GetUsage(data, store.Get())
	if u.LogsBytes != 167 || u.LogFiles != 4 || u.ContentBytes != 7 || u.EnginesBytes != 60 || u.TempBytes != 40 {
		t.Fatalf("%+v", u)
	}
	if len(u.Models) != 2 || u.Models[0].Bytes != 700 || u.Models[0].Models != 2 || u.Models[1].Bytes != 401 || u.Models[1].Models != 1 || u.Models[0].Drive != "data" {
		t.Fatalf("models %+v", u.Models)
	}
	if len(u.Drives) != 2 || !u.Drives[0].Low || len(u.Drives[0].Roles) != 3 || u.Drives[1].Low || len(u.Drives[1].Roles) != 0 {
		t.Fatalf("drives %+v", u.Drives)
	}
}

func TestCleanupRemovesOnlyOldKnownFiles(t *testing.T) {
	data, store := setup(t)
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -31)
	oldLog := filepath.Join(data, "logs", "engines", "ollama", "out.log.2")
	newLog := filepath.Join(data, "logs", "engines", "ollama", "out.log")
	oldOther := filepath.Join(data, "logs", "engines", "ollama", "keep.txt")
	oldJournal := filepath.Join(data, "logs", "requests", "requests.jsonl.1")
	write(t, oldLog, 10, old)
	write(t, newLog, 10, now)
	write(t, oldOther, 10, old)
	write(t, oldJournal, 5, old)
	stage := filepath.Join(data, "engines", ".staging-0123456789abcdef0123456789abcdef")
	write(t, filepath.Join(stage, "x"), 3, old)
	os.Chtimes(stage, old, old)
	r := Cleanup(data, store.Get(), now)
	if r.Removed != 3 || r.Freed != 18 {
		t.Fatalf("%+v", r)
	}
	for _, p := range []string{oldLog, oldJournal, stage} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s kept", p)
		}
	}
	for _, p := range []string{newLog, oldOther} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s removed", p)
		}
	}
	store.Update(func(c *config.Config) error { c.Storage.LogRetentionDays = 0; return nil })
	write(t, oldLog, 10, old)
	if r := Cleanup(data, store.Get(), now); r.Removed != 0 {
		t.Fatalf("retention 0 removed %+v", r)
	}
}

func TestSaveValidatesAndRemovesContentWhenTurnedOff(t *testing.T) {
	data, store := setup(t)
	content := filepath.Join(data, "logs", "requests", "content.jsonl")
	if err := Save(data, store, map[string]any{"action": "save", "request_log": "content", "log_retention_days": float64(7), "log_warn_mb": float64(50)}); err != nil {
		t.Fatal(err)
	}
	write(t, content, 5, time.Time{})
	if c := store.Get().Storage; c.RequestLog != "content" || c.LogRetentionDays != 7 || c.LogWarnMB != 50 {
		t.Fatalf("%+v", c)
	}
	for _, bad := range []map[string]any{{"log_retention_days": float64(400)}, {"log_warn_mb": 1.5}, {"request_log": "everything"}, {"path": "/etc"}} {
		if err := Save(data, store, bad); err == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if _, err := os.Stat(content); err != nil {
		t.Fatal("a refused change removed content")
	}
	if err := Save(data, store, map[string]any{"request_log": "metadata"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(content); err == nil {
		t.Fatal("content kept after turning the journal off")
	}
	saved, _ := os.ReadFile(filepath.Join(data, "config.json"))
	if !strings.Contains(string(saved), `"request_log": "metadata"`) {
		t.Fatal("setting not saved")
	}
}

func TestCleanupRemovesStalePartialDownloads(t *testing.T) {
	data, store := setup(t)
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	dir := store.Get().Engines[config.PrismML].ModelsDirectory
	stale := filepath.Join(dir, ".download-0123456789abcdef0123456789abcdef.part")
	fresh := filepath.Join(dir, ".download-fedcba9876543210fedcba9876543210.part")
	model := filepath.Join(dir, "old-model-Q4_K_M.gguf")
	write(t, stale, 100, now.AddDate(0, 0, -8))
	write(t, fresh, 100, now.AddDate(0, 0, -2))
	write(t, model, 100, now.AddDate(-1, 0, 0))
	if r := Cleanup(data, store.Get(), now); r.Removed != 1 {
		t.Fatalf("%+v", r)
	}
	for path, kept := range map[string]bool{stale: false, fresh: true, model: true} {
		if _, err := os.Stat(path); (err == nil) != kept {
			t.Fatalf("%s kept=%v", path, err == nil)
		}
	}
}

func TestRetentionInHoursAndRemovingAllLogs(t *testing.T) {
	data := t.TempDir()
	logs := filepath.Join(data, "logs")
	os.MkdirAll(filepath.Join(logs, "requests"), 0o755)
	old, fresh := filepath.Join(logs, "panel.log.1"), filepath.Join(logs, "panel.log")
	os.WriteFile(old, []byte("old"), 0o644)
	os.WriteFile(fresh, []byte("new"), 0o644)
	os.WriteFile(filepath.Join(logs, "requests", "requests.jsonl"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(logs, "notes.txt"), []byte("mine"), 0o644)
	now := time.Now()
	os.Chtimes(old, now.Add(-7*time.Hour), now.Add(-7*time.Hour))
	cfg := config.Default()
	cfg.Panel.Hosts = []string{"localhost"}
	b64 := base64.StdEncoding.EncodeToString(make([]byte, 32))
	cfg.Panel.Password = config.Password{Salt: b64, Hash: b64, Iterations: 210000}
	cfg.Storage.LogRetentionHours = 6
	if r := Cleanup(data, cfg, now); r.Removed != 1 || fileExists(old) || !fileExists(fresh) {
		t.Fatalf("%+v", r)
	}
	store := config.NewStore(filepath.Join(data, "config.json"), cfg)
	if err := Save(data, store, map[string]any{"log_retention_days": float64(1)}); err != nil || store.Get().Storage.LogRetentionHours != 0 || store.Get().Storage.LogRetention() != 24*time.Hour {
		t.Fatalf("%v %+v", err, store.Get().Storage)
	}
	if err := Save(data, store, map[string]any{"log_retention_hours": float64(9000)}); err == nil {
		t.Fatal("more than a year of hours accepted")
	}
	if r := RemoveLogs(data); r.Removed != 2 || fileExists(fresh) || !fileExists(filepath.Join(logs, "notes.txt")) {
		t.Fatalf("%+v", r)
	}
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
