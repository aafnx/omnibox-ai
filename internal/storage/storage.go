// Package storage reports what OmniBox AI keeps on disk and removes what it no longer needs:
// logs past the retention period and leftovers of interrupted engine installs. Cleanup runs at
// service start and when the owner asks; never on a timer, and only on known name patterns.
package storage

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aafnx/omnibox-ai/internal/catalog"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

var (
	engineLog  = regexp.MustCompile(`^(?:out|err|host)\.log(?:\.\d+)?$`)
	panelLog   = regexp.MustCompile(`^(?:panel|update)\.log(?:\.\d+)?$`)
	requestLog = regexp.MustCompile(`^(?:requests|content)\.jsonl(?:\.\d+)?$`)
	contentLog = regexp.MustCompile(`^content\.jsonl(?:\.\d+)?$`)
	staging    = regexp.MustCompile(`^\.staging-[0-9a-f]{32}$`)
)

// Usage is the /api/storage document.
type Usage struct {
	LogsBytes        int64        `json:"logs_bytes"`
	LogFiles         int          `json:"log_files"`
	ContentBytes     int64        `json:"content_bytes"`
	RequestLog       string       `json:"request_log"`
	EnginesBytes     int64        `json:"engines_bytes"`
	TempBytes        int64        `json:"temp_bytes"`
	LogRetentionDays int          `json:"log_retention_days"`
	LogRetentionHrs  int          `json:"log_retention_hours"`
	LogWarnMB        int          `json:"log_warn_mb"`
	LogsOverLimit    bool         `json:"logs_over_limit"`
	Drives           []DriveUsage `json:"drives"`
	Models           []ModelUsage `json:"models"`
	Cleanup          *Result      `json:"cleanup,omitempty"`
}

// DriveUsage is a disk with the roles it plays for OmniBox AI.
type DriveUsage struct {
	platform.Drive
	Used  int64    `json:"used"`
	Roles []string `json:"roles"`
	Low   bool     `json:"low"`
}

// ModelUsage is the size of one engine's models.
type ModelUsage struct {
	Name   string `json:"name"`
	Engine string `json:"engine"`
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Models int    `json:"models"`
	Drive  string `json:"drive"`
}

// Result is what a cleanup removed.
type Result struct {
	Removed int   `json:"removed"`
	Freed   int64 `json:"freed_bytes"`
	Busy    int   `json:"busy,omitempty"` // files open by their writer, left in place
}

// Paths of the data directory that storage knows about.
func logsDir(data string) string     { return filepath.Join(data, "logs") }
func requestsDir(data string) string { return filepath.Join(data, "logs", "requests") }

// EnginesDir holds installed engine versions.
func EnginesDir(data string) string { return filepath.Join(data, "engines") }

type file struct {
	path string
	size int64
	mod  time.Time
	dir  bool
}

// matching lists regular files (or directories) in dir whose names match; links are skipped.
func matching(dir string, pattern *regexp.Regexp, dirs bool) []file {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []file
	for _, e := range entries {
		if !pattern.MatchString(e.Name()) || e.Type()&fs.ModeSymlink != 0 || e.IsDir() != dirs {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		f := file{path: filepath.Join(dir, e.Name()), size: info.Size(), mod: info.ModTime(), dir: dirs}
		if dirs {
			f.size = dirSize(f.path)
		}
		out = append(out, f)
	}
	return out
}

// logFiles are every engine log, the panel log and the request journals, including rotated copies.
func logFiles(data string) []file {
	out := matching(logsDir(data), panelLog, false)
	for _, name := range config.EngineNames {
		out = append(out, matching(filepath.Join(logsDir(data), "engines", name), engineLog, false)...)
	}
	return append(out, matching(requestsDir(data), requestLog, false)...)
}

func dirSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// GetUsage measures logs, engines and models and lists the drives. It reads the disk only when
// the panel asks.
func GetUsage(data string, cfg *config.Config) Usage {
	u := Usage{RequestLog: cfg.Storage.RequestLog, LogRetentionDays: cfg.Storage.LogRetentionDays, LogRetentionHrs: cfg.Storage.LogRetentionHours, LogWarnMB: cfg.Storage.LogWarnMB,
		Drives: []DriveUsage{}, Models: []ModelUsage{}}
	for _, f := range logFiles(data) {
		u.LogsBytes += f.size
		u.LogFiles++
	}
	for _, f := range matching(requestsDir(data), contentLog, false) {
		u.ContentBytes += f.size
	}
	u.EnginesBytes = dirSize(EnginesDir(data))
	for _, f := range matching(EnginesDir(data), staging, true) {
		u.TempBytes += f.size
	}
	u.EnginesBytes -= u.TempBytes
	u.LogsOverLimit = u.LogsBytes > int64(cfg.Storage.LogWarnMB)<<20
	for _, name := range config.EngineNames {
		if e, ok := cfg.Engines[name]; ok {
			u.Models = append(u.Models, modelUsage(name, e))
		}
	}
	u.Drives = drives(data, u.Models)
	return u
}

func modelUsage(name string, e *config.Engine) ModelUsage {
	m := ModelUsage{Name: name, Engine: config.Label(name)}
	if name == config.PrismML {
		m.Path = engine.ModelDirectory(e)
		models, _ := engine.PrismModels(m.Path)
		for _, x := range models {
			m.Bytes += x.Bytes
			m.Models++
		}
		return m
	}
	// Ollama: blobs hold the weights, one manifest per installed model tag.
	m.Path = e.ModelsDirectory
	if m.Path == "" {
		return m
	}
	m.Bytes = dirSize(m.Path)
	_ = filepath.WalkDir(filepath.Join(m.Path, "manifests"), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			m.Models++
		}
		return nil
	})
	return m
}

// owner is the drive that stores path: the deepest mount point containing it.
func owner(all []platform.Drive, path string) *platform.Drive {
	full, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	var best *platform.Drive
	for i := range all {
		root := all[i].Root
		if hasPrefixFold(full+string(filepath.Separator), root) && (best == nil || len(root) > len(best.Root)) {
			best = &all[i]
		}
	}
	return best
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// drives marks which drive holds the data directory and each engine's models; a drive that
// holds them warns when it has less than 20 GB or 10% free.
var listDrives = platform.Drives

func drives(data string, models []ModelUsage) []DriveUsage {
	all := listDrives()
	type place struct{ role, path string }
	places := []place{{"OmniBox AI", data}}
	for i := range models {
		if models[i].Path == "" {
			continue
		}
		places = append(places, place{"модели " + models[i].Engine, models[i].Path})
		if d := owner(all, models[i].Path); d != nil {
			models[i].Drive = d.Name
		}
	}
	out := []DriveUsage{}
	for _, d := range all {
		u := DriveUsage{Drive: d, Used: d.Total - d.Free, Roles: []string{}}
		for _, p := range places {
			if o := owner(all, p.path); o != nil && o.Root == d.Root {
				u.Roles = append(u.Roles, p.role)
			}
		}
		u.Low = len(u.Roles) > 0 && (d.Free < 20<<30 || d.Free < d.Total/10)
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FreeSpace is the free space of the drive that holds path.
func FreeSpace(path string) (int64, bool) {
	if d := owner(listDrives(), path); d != nil {
		return d.Free, true
	}
	return 0, false
}

// staleParts are partial model downloads nobody resumed for a week.
const staleParts = 7 * 24 * time.Hour

// Cleanup removes logs untouched longer than the retention period, engine staging directories
// older than a day and partial downloads not resumed for a week. Files a running engine or
// download writes are recent, so they stay.
func Cleanup(data string, cfg *config.Config, now time.Time) Result {
	var items []file
	if keep := cfg.Storage.LogRetention(); keep > 0 {
		limit := now.Add(-keep)
		for _, f := range logFiles(data) {
			if f.mod.Before(limit) {
				items = append(items, f)
			}
		}
	}
	for _, f := range matching(EnginesDir(data), staging, true) {
		if f.mod.Before(now.Add(-24 * time.Hour)) {
			items = append(items, f)
		}
	}
	if e, ok := cfg.Engines[config.PrismML]; ok {
		dirs := []string{e.ModelsDirectory}
		if d := engine.ModelDirectory(e); d != e.ModelsDirectory {
			dirs = append(dirs, d)
		}
		for _, dir := range dirs {
			for _, f := range matching(dir, catalog.PartPattern, false) {
				if f.mod.Before(now.Add(-staleParts)) {
					items = append(items, f)
				}
			}
		}
	}
	var r Result
	for _, f := range items {
		var err error
		if f.dir {
			err = os.RemoveAll(f.path)
		} else {
			err = os.Remove(f.path)
		}
		// A file still open by its writer stays until the next cleanup.
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			r.Removed++
			r.Freed += f.size
		}
	}
	return r
}

// RemoveLogs deletes every log the owner can see in the panel: engine and panel logs and the
// request journals, rotated copies included. A file its writer holds open (Windows) stays and
// is counted as busy; the writer starts a fresh one at its next rotation.
func RemoveLogs(data string) Result {
	var r Result
	for _, f := range logFiles(data) {
		if err := os.Remove(f.path); err == nil || errors.Is(err, fs.ErrNotExist) {
			r.Removed++
			r.Freed += f.size
		} else {
			r.Busy++
		}
	}
	return r
}

// RemoveContent deletes the stored request text when the owner turns the content journal off.
func RemoveContent(data string) int {
	n := 0
	for _, f := range matching(requestsDir(data), contentLog, false) {
		if os.Remove(f.path) == nil {
			n++
		}
	}
	return n
}

// Save changes the log settings from the panel form. Turning the content journal off also
// deletes the request text already stored, after the gateway has stopped writing it.
func Save(data string, store *config.Store, body map[string]any) error {
	err := store.Update(func(c *config.Config) error {
		for key, value := range body {
			switch key {
			case "action":
			case "log_retention_days", "log_retention_hours", "log_warn_mb":
				n, ok := value.(float64)
				if !ok || n != float64(int(n)) {
					return errors.New("Срок хранения и порог объёма — целые числа.")
				}
				// Days and hours replace each other: the form sends one of them.
				switch key {
				case "log_retention_days":
					c.Storage.LogRetentionDays, c.Storage.LogRetentionHours = int(n), 0
				case "log_retention_hours":
					c.Storage.LogRetentionHours = int(n)
					if n > 0 {
						c.Storage.LogRetentionDays = 0
					}
				default:
					c.Storage.LogWarnMB = int(n)
				}
			case "request_log":
				s, _ := value.(string)
				c.Storage.RequestLog = s
			default:
				return errors.New("Неизвестная настройка хранения: " + key)
			}
		}
		return nil
	})
	if err == nil && store.Get().Storage.RequestLog == "metadata" {
		RemoveContent(data)
	}
	return err
}
