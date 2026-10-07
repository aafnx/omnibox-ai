package status

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/aafnx/omnibox-ai/internal/config"
)

// LogStreams are the files the engine host writes for each engine.
var LogStreams = []string{"err", "out", "host"}

// LogDir is where an engine's logs are.
func LogDir(dataDir, engine string) string { return filepath.Join(dataDir, "logs", "engines", engine) }

var stamped = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\S+) (.*)$`)

// tail returns the last n lines of a file without reading more than its last 1 MiB.
func tail(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > 1<<20 {
		_, _ = f.Seek(info.Size()-1<<20, 0)
	}
	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return lines
}

// Logs merges the last lines of every engine's streams in time order, as the panel shows them.
func Logs(dataDir string, cfg *config.Config, only string) []map[string]string {
	var lines []string
	for _, name := range config.EngineNames {
		if _, ok := cfg.Engines[name]; !ok || (only != "" && only != name) {
			continue
		}
		for _, stream := range LogStreams {
			for _, line := range tail(filepath.Join(LogDir(dataDir, name), stream+".log"), 80) {
				if m := stamped.FindStringSubmatch(line); m != nil {
					lines = append(lines, m[1]+" ["+name+"/"+stream+"] "+m[2])
				}
			}
		}
	}
	sort.Strings(lines)
	text := "Журнал пока отсутствует."
	if len(lines) > 0 {
		text = strings.Join(lines, "\n")
	}
	return []map[string]string{{"stream": "События сервера", "text": text}}
}
