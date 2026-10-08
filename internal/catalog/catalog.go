// Package catalog finds GGUF models on Hugging Face and downloads them with resume. It talks to
// the network only when the owner searches or downloads.
package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	repoPattern  = regexp.MustCompile(`^[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+$`)
	revision     = regexp.MustCompile(`^[a-f0-9]{40}$`)
	splitPattern = regexp.MustCompile(`-\d{5}-of-\d{5}\.gguf$`)
	projector    = regexp.MustCompile(`(?i)mmproj|projector`)
	prismOnly    = regexp.MustCompile(`(?i)PQ2|PTQ1|Bonsai-2`)
	unsafeName   = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)
	// FileName is the form of a model file name on disk.
	FileName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,159}\.gguf$`)
)

// Hub is the Hugging Face API.
type Hub struct {
	Base string // https://huggingface.co
	HTTP *http.Client
}

func (h *Hub) base() string {
	if h.Base != "" {
		return h.Base
	}
	return "https://huggingface.co"
}

func (h *Hub) get(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.base()+"/api/models"+path, nil)
	req.Header.Set("User-Agent", "omnibox")
	client := http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if h.HTTP != nil {
		client.Transport = h.HTTP.Transport
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Hugging Face недоступен: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errors.New("Hugging Face: репозиторий не найден.")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Hugging Face ответил %d.", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// Model is one search result.
type Model struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Downloads int64  `json:"downloads"`
	Gated     bool   `json:"gated"`
	// Capabilities are what the repository's tags and task say: reasoning, tools, vision, moe.
	Capabilities []string `json:"capabilities"`
}

var moeName = regexp.MustCompile(`(?i)-A\d+(?:\.\d+)?B\b|\bMoE\b`)

// searchLimit is how many repositories a search shows.
const searchLimit = 12

// Search finds GGUF repositories, most downloaded first. Hugging Face's GGUF filter can come back
// empty for a common name, so an empty answer is asked again by library, then without a filter,
// keeping the repositories tagged or named GGUF.
func (h *Hub) Search(ctx context.Context, query string) ([]Model, error) {
	if len([]rune(query)) < 2 || len([]rune(query)) > 100 || strings.ContainsFunc(query, func(r rune) bool { return r < 0x20 }) {
		return nil, errors.New("Запрос поиска: от 2 до 100 символов.")
	}
	attempts := []url.Values{
		{"search": {query}, "filter": {"gguf"}, "limit": {"20"}, "sort": {"downloads"}, "direction": {"-1"}},
		{"search": {query}, "library": {"gguf"}, "limit": {"20"}, "sort": {"downloads"}, "direction": {"-1"}},
		{"search": {query}, "limit": {"100"}, "sort": {"downloads"}, "direction": {"-1"}},
	}
	out := []Model{}
	for i, q := range attempts {
		var items []struct {
			ID          string   `json:"id"`
			Downloads   int64    `json:"downloads"`
			Gated       any      `json:"gated"`
			Tags        []string `json:"tags"`
			PipelineTag string   `json:"pipeline_tag"`
		}
		if err := h.get(ctx, "?"+q.Encode(), &items); err != nil {
			if i == 0 {
				return nil, err
			}
			break
		}
		for _, it := range items {
			if !repoPattern.MatchString(it.ID) || strings.Contains(it.ID, "..") || len(out) >= searchLimit {
				continue
			}
			if i == len(attempts)-1 && !strings.Contains(strings.ToLower(it.ID), "gguf") && !slices.Contains(it.Tags, "gguf") {
				continue
			}
			gated := it.Gated != nil && it.Gated != false
			out = append(out, Model{ID: it.ID, Name: it.ID, URL: h.base() + "/" + it.ID, Downloads: it.Downloads, Gated: gated,
				Capabilities: capabilities(it.ID, it.PipelineTag, it.Tags)})
		}
		if len(out) > 0 {
			break
		}
	}
	return out, nil
}

// capabilities reads a repository's task and tags; names say MoE (35B-A3B) and reasoning (R1).
func capabilities(id, task string, tags []string) []string {
	has := func(names ...string) bool {
		for _, t := range tags {
			for _, n := range names {
				if strings.EqualFold(t, n) {
					return true
				}
			}
		}
		return false
	}
	l := strings.ToLower(id)
	out := []string{}
	if has("reasoning", "thinking", "chain-of-thought") || strings.Contains(l, "thinking") || strings.Contains(l, "-r1") || strings.Contains(l, "reason") {
		out = append(out, "reasoning")
	}
	if has("tool-use", "tools", "function-calling", "function_calling", "agent") {
		out = append(out, "tools")
	}
	if task == "image-text-to-text" || task == "visual-question-answering" || has("vision", "multimodal", "image-text-to-text") {
		out = append(out, "vision")
	}
	if has("moe", "mixture-of-experts") || moeName.MatchString(id) {
		out = append(out, "moe")
	}
	return out
}

// File is one GGUF file of a repository.
type File struct {
	Name      string `json:"name"`
	Bytes     *int64 `json:"bytes"`
	SHA256    string `json:"sha256"`
	URL       string `json:"url"`
	Filename  string `json:"filename"`
	Split     bool   `json:"split"`
	PrismOnly bool   `json:"prism_only"`
	// Projector files (mmproj) are not models: they give a model of the same repository vision.
	Projector bool `json:"projector"`
}

// Files lists a repository's GGUF files with download links pinned to the current revision,
// so a resumed download continues the same file.
func (h *Hub) Files(ctx context.Context, repo string) ([]File, error) {
	if !repoPattern.MatchString(repo) || strings.Contains(repo, "..") {
		return nil, errors.New("Нужен Hugging Face repo в формате author/model.")
	}
	var info struct {
		SHA      string `json:"sha"`
		Siblings []struct {
			Name string `json:"rfilename"`
			Size *int64 `json:"size"`
			LFS  *struct {
				SHA256 string `json:"sha256"`
			} `json:"lfs"`
		} `json:"siblings"`
	}
	if err := h.get(ctx, "/"+repo+"?blobs=true", &info); err != nil {
		return nil, err
	}
	rev := "main"
	if revision.MatchString(info.SHA) {
		rev = info.SHA
	}
	out := []File{}
	for _, s := range info.Siblings {
		if !strings.HasSuffix(s.Name, ".gguf") || strings.Contains(s.Name, "..") || strings.HasPrefix(s.Name, "/") {
			continue
		}
		f := File{Name: s.Name, Bytes: s.Size, Split: splitPattern.MatchString(s.Name), PrismOnly: prismOnly.MatchString(s.Name), Projector: projector.MatchString(s.Name)}
		if s.LFS != nil && regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(s.LFS.SHA256) {
			f.SHA256 = s.LFS.SHA256
		}
		var parts []string
		for _, p := range strings.Split(s.Name, "/") {
			parts = append(parts, url.PathEscape(p))
		}
		f.URL = "https://huggingface.co/" + repo + "/resolve/" + rev + "/" + strings.Join(parts, "/")
		f.Filename = unsafeName.ReplaceAllString(strings.ReplaceAll(repo, "/", "-")+"-"+path.Base(s.Name), "-")
		if len(f.Filename) > 165 {
			f.Filename = f.Filename[:150] + ".gguf"
		}
		out = append(out, f)
		if len(out) >= 80 {
			break
		}
	}
	return out, nil
}

// ModelURL checks a direct Hugging Face file link.
func ModelURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "huggingface.co" || u.User != nil || u.Fragment != "" || u.RawQuery != "" ||
		!regexp.MustCompile(`^/[^/]+/[^/]+/resolve/[^/]+/.+\.gguf$`).MatchString(u.Path) || strings.Contains(u.Path, "..") {
		return nil, errors.New("Нужна прямая HTTPS-ссылка huggingface.co/.../resolve/.../model.gguf.")
	}
	return u, nil
}

// Pinned tells whether the link names a fixed revision (a commit), whose content cannot change.
func Pinned(u *url.URL) bool {
	parts := strings.Split(u.Path, "/")
	return len(parts) > 4 && revision.MatchString(parts[4])
}
