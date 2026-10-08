package engine

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aafnx/omnibox-ai/internal/gguf"
)

// Details is what the installed models catalog shows about a model.
type Details struct {
	Model
	Params  string `json:"params,omitempty"`  // 27B
	Context int    `json:"context,omitempty"` // trained context, tokens
	// Capabilities are reasoning, tools, vision and moe, like on openrouter.ai.
	Capabilities []string `json:"capabilities"`
	Source       string   `json:"source,omitempty"` // the model's page
	// SourceSearch means the page is a search: the model was not downloaded through the panel.
	SourceSearch bool `json:"source_search,omitempty"`
	Selected     bool `json:"selected"`
	// InUse models are selected or needed for rollback: the panel does not delete them.
	InUse bool `json:"in_use"`
}

var paramsPattern = regexp.MustCompile(`(?i)(?:^|[-_.])(\d+(?:\.\d+)?[BM])(?:[-_.]|$)`)

// headers caches GGUF headers by file, size and time: reading tokenizer arrays takes a moment,
// and the catalog is opened again and again. Entries of removed files are dropped on each listing.
var headers = struct {
	sync.Mutex
	m map[string]headerEntry
}{m: map[string]headerEntry{}}

type headerEntry struct {
	size int64
	mod  time.Time
	meta gguf.Meta
	err  error
}

func header(path string) (gguf.Meta, error) {
	info, err := os.Stat(path)
	if err != nil {
		return gguf.Meta{}, err
	}
	headers.Lock()
	e, ok := headers.m[path]
	headers.Unlock()
	if ok && e.size == info.Size() && e.mod.Equal(info.ModTime()) {
		return e.meta, e.err
	}
	meta, err := gguf.Read(path)
	headers.Lock()
	headers.m[path] = headerEntry{info.Size(), info.ModTime(), meta, err}
	headers.Unlock()
	return meta, err
}

// PrismDetails describes GGUF models of a directory. sources maps a model path to its page;
// selected and inUse are the model paths of the settings and of the rollback point.
func PrismDetails(models []Model, sources map[string]string, selected string, inUse []string) []Details {
	var projectors []string
	if len(models) > 0 {
		if entries, err := os.ReadDir(filepath.Dir(models[0].ID)); err == nil {
			for _, e := range entries {
				if !e.IsDir() && projector.MatchString(e.Name()) && strings.EqualFold(filepath.Ext(e.Name()), ".gguf") {
					projectors = append(projectors, e.Name())
				}
			}
		}
	}
	keep := map[string]bool{}
	out := make([]Details, 0, len(models))
	for _, m := range models {
		keep[m.ID] = true
		d := Details{Model: m, Capabilities: []string{}, Selected: m.ID == selected}
		for _, u := range inUse {
			d.InUse = d.InUse || (u != "" && u == m.ID)
		}
		if meta, err := header(m.ID); err == nil {
			d.Context, d.Params = meta.Context, meta.SizeLabel
			if meta.Thinking {
				d.Capabilities = append(d.Capabilities, "reasoning")
			}
			if meta.Tools {
				d.Capabilities = append(d.Capabilities, "tools")
			}
			if meta.Experts > 1 {
				d.Capabilities = append(d.Capabilities, "moe")
			}
		}
		if d.Params == "" {
			if p := paramsPattern.FindStringSubmatch(m.Name); p != nil {
				d.Params = strings.ToUpper(p[1])
			}
		}
		if hasProjector(m.Name, projectors) {
			d.Capabilities = append(d.Capabilities, "vision")
		}
		if s, ok := sources[m.ID]; ok {
			d.Source = s
		} else {
			d.Source, d.SourceSearch = "https://huggingface.co/models?library=gguf&search="+url.QueryEscape(family(m.Name)), true
		}
		out = append(out, d)
	}
	headers.Lock()
	for path := range headers.m {
		if !keep[path] && (len(models) == 0 || filepath.Dir(path) == filepath.Dir(models[0].ID)) {
			delete(headers.m, path)
		}
	}
	headers.Unlock()
	return out
}

// family is a model file name without its quantization, split numbering and extension: what a
// search on Hugging Face and a matching projector have in common.
func family(name string) string {
	name = strings.TrimSuffix(strings.TrimSuffix(name, ".gguf"), ".GGUF")
	if m := splitPart.FindStringSubmatch(name + ".gguf"); m != nil {
		name = m[1]
	}
	if q := Quant(name + ".gguf"); q != "" {
		if i := strings.LastIndex(strings.ToUpper(name), q); i > 0 {
			name = name[:i]
		}
	}
	return strings.Trim(name, "-_. ")
}

// hasProjector tells whether a vision projector (mmproj) in the directory belongs to the model.
func hasProjector(model string, projectors []string) bool {
	base := strings.ToLower(family(model))
	for _, p := range projectors {
		f := strings.ToLower(family(p))
		f = strings.Trim(regexp.MustCompile(`(?i)mmproj|projector`).ReplaceAllString(f, ""), "-_. ")
		if len(f) >= 6 && (strings.Contains(base, f) || strings.HasPrefix(base, f)) {
			return true
		}
		if n := commonPrefix(base, f); n >= 16 {
			return true
		}
	}
	return false
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// OllamaDetails describes Ollama's models with what /api/show reports for each.
func (c *Client) OllamaDetails(ctx context.Context, models []Model, selected string) []Details {
	out := make([]Details, 0, len(models))
	for _, m := range models {
		d := Details{Model: m, Capabilities: []string{}, Selected: m.ID == selected, InUse: m.ID == selected}
		var show struct {
			Capabilities []string       `json:"capabilities"`
			Details      map[string]any `json:"details"`
			ModelInfo    map[string]any `json:"model_info"`
		}
		if err := c.Post(ctx, "/api/show", map[string]string{"model": m.ID}, &show, 10*time.Second); err == nil {
			for _, cap := range show.Capabilities {
				switch cap {
				case "thinking":
					d.Capabilities = append(d.Capabilities, "reasoning")
				case "tools", "vision":
					d.Capabilities = append(d.Capabilities, cap)
				}
			}
			if p, ok := show.Details["parameter_size"].(string); ok {
				d.Params = p
			}
			if q, ok := show.Details["quantization_level"].(string); ok {
				d.Quant = q
			}
			for k, v := range show.ModelInfo {
				if n, ok := v.(float64); ok && strings.HasSuffix(k, ".context_length") {
					d.Context = int(n)
				}
				if n, ok := v.(float64); ok && strings.HasSuffix(k, ".expert_count") && n > 1 {
					d.Capabilities = append(d.Capabilities, "moe")
				}
			}
		}
		d.Source = OllamaSource(m.ID)
		out = append(out, d)
	}
	return out
}

// OllamaSource is the page of an Ollama model: Hugging Face for hf.co/author/repo:tag, the
// Ollama library otherwise.
func OllamaSource(name string) string {
	base, _, _ := strings.Cut(name, ":")
	if repo, ok := strings.CutPrefix(base, "hf.co/"); ok {
		return "https://huggingface.co/" + repo
	}
	if repo, ok := strings.CutPrefix(base, "huggingface.co/"); ok {
		return "https://huggingface.co/" + repo
	}
	if strings.Contains(base, "/") {
		return "https://ollama.com/" + base
	}
	return "https://ollama.com/library/" + base
}
