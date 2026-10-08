package engine

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aafnx/omnibox-ai/internal/gguf"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Memory is what the panel needs to estimate a model's memory for any context and cache type:
// the weights (with the projector) and the cached keys and values per token over every KV layer.
type Memory struct {
	Weights int64 `json:"weights_bytes"`
	// KeyElems and ValueElems are cached values per token; multiplied by the cache type's bytes.
	KeyElems   int64 `json:"kv_key_per_token"`
	ValueElems int64 `json:"kv_value_per_token"`
	Layers     int   `json:"layers"`
	Experts    int   `json:"experts,omitempty"`
}

// MemoryOf describes a GGUF header's memory needs with the weights given.
func MemoryOf(m gguf.Meta, weights int64) *Memory {
	if m.Blocks <= 0 {
		return nil
	}
	kv := m.KVLayers
	if kv <= 0 || kv > m.Blocks {
		kv = m.Blocks
	}
	return &Memory{Weights: weights, KeyElems: int64(m.HeadsKV) * int64(m.KeyLength) * int64(kv),
		ValueElems: int64(m.HeadsKV) * int64(m.ValueLength) * int64(kv), Layers: m.Blocks, Experts: m.Experts}
}

// Ollama's manifest layers.
const (
	ollamaModelLayer     = "application/vnd.ollama.image.model"
	ollamaProjectorLayer = "application/vnd.ollama.image.projector"
	ollamaTemplateLayer  = "application/vnd.ollama.image.template"
)

type ollamaManifest struct {
	Config struct {
		Digest string `json:"digest"`
	} `json:"config"`
	Layers []struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	} `json:"layers"`
}

// OllamaDisk lists Ollama's downloaded models from its models directory, without Ollama running:
// the manifests name the models, the blobs are GGUF files whose headers give the details.
func OllamaDisk(dir string) []Details {
	root := filepath.Join(dir, "manifests")
	var out []Details
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 4 {
			return nil
		}
		name := ollamaName(parts)
		data, err := platform.ReadShared(path)
		if err != nil || len(data) > 1<<20 {
			return nil
		}
		var m ollamaManifest
		if json.Unmarshal(data, &m) != nil {
			return nil
		}
		det := Details{Model: Model{ID: name, Name: name}, Capabilities: []string{}, Source: OllamaSource(name)}
		var weights int64
		blob := ""
		vision := false
		for _, l := range m.Layers {
			switch l.MediaType {
			case ollamaModelLayer:
				blob, weights = blobPath(dir, l.Digest), weights+l.Size
				det.Bytes += l.Size
			case ollamaProjectorLayer:
				vision, weights = true, weights+l.Size
				det.Bytes += l.Size
			case ollamaTemplateLayer:
				if t := readSmall(blobPath(dir, l.Digest), 256<<10); t != "" {
					thinking, tools := gguf.TemplateFeatures(t)
					if thinking {
						det.Capabilities = append(det.Capabilities, "reasoning")
					}
					if tools {
						det.Capabilities = append(det.Capabilities, "tools")
					}
				}
			}
		}
		if blob == "" {
			return nil
		}
		if cfg := readSmall(blobPath(dir, m.Config.Digest), 64<<10); cfg != "" {
			var c struct {
				ModelType string `json:"model_type"`
				FileType  string `json:"file_type"`
				Family    string `json:"model_family"`
			}
			if json.Unmarshal([]byte(cfg), &c) == nil {
				det.Params, det.Quant, det.Family = c.ModelType, c.FileType, c.Family
			}
		}
		if meta, err := header(blob); err == nil {
			det.Context = meta.Context
			if det.Params == "" {
				det.Params = meta.SizeLabel
			}
			if meta.Experts > 1 {
				det.Capabilities = append(det.Capabilities, "moe")
			}
			// A template inside the GGUF counts when Ollama's own layer is missing.
			if len(det.Capabilities) == 0 || (len(det.Capabilities) == 1 && det.Capabilities[0] == "moe") {
				if meta.Thinking {
					det.Capabilities = append(det.Capabilities, "reasoning")
				}
				if meta.Tools {
					det.Capabilities = append(det.Capabilities, "tools")
				}
			}
			det.Memory = MemoryOf(meta, weights)
		}
		if vision {
			det.Capabilities = append(det.Capabilities, "vision")
		}
		out = append(out, det)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ollamaName turns a manifest path (registry/namespace/model/tag) into the name Ollama shows.
func ollamaName(p []string) string {
	switch {
	case p[0] == "registry.ollama.ai" && p[1] == "library":
		return p[2] + ":" + p[3]
	case p[0] == "registry.ollama.ai":
		return p[1] + "/" + p[2] + ":" + p[3]
	}
	return p[0] + "/" + p[1] + "/" + p[2] + ":" + p[3]
}

// blobPath is where Ollama keeps a layer: blobs/sha256-<hex>.
func blobPath(dir, digest string) string {
	hex, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || len(hex) != 64 || strings.ContainsAny(hex, `/\.`) {
		return ""
	}
	return filepath.Join(dir, "blobs", "sha256-"+hex)
}

func readSmall(path string, limit int64) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return ""
	}
	return string(data)
}
