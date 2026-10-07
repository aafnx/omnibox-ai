package engine

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/aafnx/omnibox-ai/internal/config"
)

// Model is one model the engine can load.
type Model struct {
	ID    string `json:"id"` // full path for PrismML, name for Ollama
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
	Quant string `json:"quant,omitempty"`
	Parts int    `json:"parts,omitempty"`
}

var (
	quantPattern = regexp.MustCompile(`(?i)(?:^|[-_.])((?:UD-)?(?:I?Q\d_[A-Z0-9_]+?|Q\d_\d|T?Q\d_\d|PQ\d_\d|PTQ\d(?:_\d)?|MXFP4|NVFP4|BF16|F16|F32))(?:[-_.](?:\d{5}-of-\d{5}\.)?gguf$|\.gguf$)`)
	splitPart    = regexp.MustCompile(`^(.+)-(\d{5})-of-(\d{5})\.gguf$`)
	projector    = regexp.MustCompile(`(?i)mmproj|projector`)
)

// Quant extracts the quantization from a GGUF file name, such as Q4_K_M or PQ2_0.
func Quant(name string) string {
	m := quantPattern.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}

// ModelDirectory is where PrismML's GGUF files are: next to the selected model, or the
// configured directory for an engine without one.
func ModelDirectory(e *config.Engine) string {
	if m := e.Settings["model"]; m != "" {
		return filepath.Dir(m)
	}
	return e.ModelsDirectory
}

// PrismModels lists GGUF models; a split model (name-00001-of-00003.gguf …) is one entry with
// its parts' total size, loaded through the first part. Projectors are not models.
func PrismModels(dir string) ([]Model, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type file struct {
		name string
		size int64
	}
	var files []file
	for _, e := range entries {
		if e.IsDir() || e.Type()&os.ModeSymlink != 0 || !strings.EqualFold(filepath.Ext(e.Name()), ".gguf") || projector.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, file{e.Name(), info.Size()})
	}
	var models []Model
	for _, f := range files {
		m := splitPart.FindStringSubmatch(f.name)
		if m != nil && m[2] != "00001" {
			continue
		}
		model := Model{ID: filepath.Join(dir, f.name), Name: f.name, Bytes: f.size, Quant: Quant(f.name), Parts: 1}
		if m != nil {
			model.Parts, model.Bytes = 0, 0
			for _, g := range files {
				if p := splitPart.FindStringSubmatch(g.name); p != nil && p[1] == m[1] && p[3] == m[3] {
					model.Parts++
					model.Bytes += g.size
				}
			}
		}
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Name < models[j].Name })
	return models, nil
}

// GGUFParts returns every file of a split model, or just the path for a single file.
func GGUFParts(path string) []string {
	dir, name := filepath.Dir(path), filepath.Base(path)
	m := splitPart.FindStringSubmatch(name)
	if m == nil {
		return []string{path}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{path}
	}
	var parts []string
	for _, e := range entries {
		if p := splitPart.FindStringSubmatch(e.Name()); p != nil && p[1] == m[1] && p[3] == m[3] {
			parts = append(parts, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(parts)
	return parts
}
