// Package library is the panel's single list of models: the local ones of PrismML and Ollama
// (read from disk, so a stopped engine still shows its models), OpenRouter's and the ChatGPT
// subscription's, with the owner's favourites, saved settings and each model's capabilities and
// reasoning profile.
package library

import (
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/aafnx/omnibox-ai/internal/chatgpt"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/openrouter"
	"github.com/aafnx/omnibox-ai/internal/reasoning"
)

// Caps are what a model understands and does.
type Caps struct {
	Vision bool `json:"vision"`
	// VisionMissing means the model sees, but its projector is not downloaded.
	VisionMissing bool `json:"vision_missing,omitempty"`
	Video         bool `json:"video"`
	Audio         bool `json:"audio"`
	Reasoning     bool `json:"reasoning"`
	Tools         bool `json:"tools"`
	Draws         bool `json:"draws"`
	MoE           bool `json:"moe,omitempty"`
}

// Model is one entry of the list.
type Model struct {
	Key      string `json:"key"`    // prismml:<file>, ollama:<name>, cloud:<id>, chatgpt:<id>
	Source   string `json:"source"` // local, cloud or chatgpt
	Engine   string `json:"engine"` // prismml, ollama, openrouter or chatgpt
	ID       string `json:"id"`     // file path, Ollama name, OpenRouter or ChatGPT id
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	File     string `json:"file,omitempty"`
	Quant    string `json:"quant,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
	Params   string `json:"params,omitempty"`
	Context  int    `json:"context,omitempty"`
	Caps     Caps   `json:"caps"`
	// Reasoning tells whether thinking can be switched off, its levels and what is shown.
	Reasoning     reasoning.Profile `json:"reasoning"`
	PromptUSD     float64           `json:"prompt_usd,omitempty"`
	CompletionUSD float64           `json:"completion_usd,omitempty"`
	Running       bool              `json:"running"`
	Favorite      bool              `json:"favorite"`
	// Alias is the name /v1 clients use for a local model.
	Alias     string            `json:"alias,omitempty"`
	Settings  map[string]string `json:"settings,omitempty"`
	Advanced  bool              `json:"advanced,omitempty"`
	Memory    *engine.Memory    `json:"memory,omitempty"`
	Projector string            `json:"projector,omitempty"`
	Page      string            `json:"page,omitempty"` // the model's page
	InUse     bool              `json:"in_use,omitempty"`
}

// Running is the local model that runs now, by key, and its /v1 name.
type Running struct {
	Key   string
	Alias string
}

// RunningOf finds the running local model from the engine states.
func RunningOf(cfg *config.Config, running func(engine string) bool) Running {
	for _, name := range config.EngineNames {
		e, ok := cfg.Engines[name]
		if !ok || !running(name) {
			continue
		}
		if name == config.PrismML {
			l := e.RunningLaunch()
			if m := l.Settings["model"]; m != "" {
				alias, _ := config.View(name, l.Settings)["alias"].(string)
				return Running{Key: config.LocalKey(name, m), Alias: alias}
			}
			continue
		}
		if e.Model != "" {
			return Running{Key: config.LocalKey(name, e.Model), Alias: e.Model}
		}
	}
	return Running{}
}

// Local turns an engine's installed models into list entries.
func Local(cfg *config.Config, name string, models []engine.Details, run Running) []Model {
	out := make([]Model, 0, len(models))
	for _, d := range models {
		key := config.LocalKey(name, d.ID)
		m := Model{Key: key, Source: "local", Engine: name, ID: d.ID, Name: DisplayName(name, d.Name), Quant: d.Quant, Bytes: d.Bytes,
			Params: d.Params, Context: d.Context, Running: key == run.Key, Favorite: slices.Contains(cfg.Favorites, key),
			Memory: d.Memory, Projector: d.Projector, Page: d.Source, InUse: d.InUse}
		if name == config.PrismML {
			m.File = filepath.Base(d.ID)
			m.Alias = config.AutoAlias(d.ID)
		} else {
			m.File, m.Alias = d.ID, d.ID
		}
		if key == run.Key && run.Alias != "" {
			m.Alias = run.Alias
		}
		for _, c := range d.Capabilities {
			switch c {
			case "vision":
				m.Caps.Vision = true
			case "reasoning":
				m.Caps.Reasoning = true
			case "tools":
				m.Caps.Tools = true
			case "moe":
				m.Caps.MoE = true
			}
		}
		if p := cfg.Models[key]; p != nil {
			m.Settings, m.Advanced = p.Settings, p.Advanced
			if a := p.Settings["alias"]; a != "" && name == config.PrismML {
				m.Alias = a
			}
		}
		if name == config.Ollama {
			caps := []string{}
			if m.Caps.Reasoning {
				caps = append(caps, "thinking")
			}
			m.Reasoning = reasoning.ForOllama(caps, d.Family)
		} else {
			m.Reasoning = reasoning.ForLocal(m.Caps.Reasoning, d.ThinkToggle)
		}
		out = append(out, m)
	}
	return out
}

// Cloud turns OpenRouter's models into list entries.
func Cloud(cfg *config.Config, models []openrouter.Model) []Model {
	out := make([]Model, 0, len(models))
	for _, c := range models {
		key := "cloud:" + c.ID
		if !config.ValidCloudModel(c.ID) {
			continue
		}
		name := c.Name
		if _, after, ok := strings.Cut(name, ":"); ok && strings.TrimSpace(after) != "" {
			name = strings.TrimSpace(after)
		}
		out = append(out, Model{Key: key, Source: "cloud", Engine: "openrouter", ID: c.ID, Name: name, Provider: c.Provider,
			Context: c.Context, PromptUSD: c.PromptUSD, CompletionUSD: c.CompletionUSD, Favorite: slices.Contains(cfg.Favorites, key),
			Caps:      Caps{Vision: c.Caps.Vision, Video: c.Caps.Video, Audio: c.Caps.Audio, Reasoning: c.Caps.Reasoning, Tools: c.Caps.Tools, Draws: c.Caps.Draws},
			Reasoning: c.Reasoning, Page: "https://openrouter.ai/" + c.ID})
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// ChatGPT turns the subscription's models into list entries. They always reason; the depth
// is one of the model's levels, and the chat shows the summary of the reasoning.
func ChatGPT(cfg *config.Config, models []chatgpt.Model) []Model {
	out := make([]Model, 0, len(models))
	for _, c := range models {
		if !config.ValidChatGPTModel(c.ID) {
			continue
		}
		key := "chatgpt:" + c.ID
		p := reasoning.Profile{Mode: reasoning.ModeAlways, Default: c.Default, Show: reasoning.ShowSummary}
		for _, e := range reasoning.Efforts {
			if slices.Contains(c.Efforts, e) {
				p.Efforts = append(p.Efforts, e)
			}
		}
		if !slices.Contains(p.Efforts, p.Default) {
			p.Default = ""
			if len(p.Efforts) > 0 {
				p.Default = p.Efforts[0]
			}
		}
		if len(p.Efforts) == 0 {
			p.Default = reasoning.StepAuto
		}
		out = append(out, Model{Key: key, Source: "chatgpt", Engine: "chatgpt", ID: c.ID, Name: c.Name, Provider: "OpenAI",
			Favorite: slices.Contains(cfg.Favorites, key), Caps: Caps{Vision: c.Images, Reasoning: true}, Reasoning: p,
			Alias: chatgpt.Prefix + c.ID, Page: "https://chatgpt.com/"})
	}
	return out
}

var (
	splitSuffix = regexp.MustCompile(`(?i)-\d{5}-of-\d{5}$`)
	quantSuffix = regexp.MustCompile(`(?i)[-_.](?:UD-)?(?:I?Q\d_[A-Z0-9_]+|Q\d_\d|T?Q\d_\d|PQ\d_\d|PTQ\d(?:_\d)?|MXFP4|NVFP4|BF16|F16|F32)$`)
)

// DisplayName is a model's name for people: a GGUF file without its repository prefix,
// quantization and extension; an Ollama name as is.
func DisplayName(engineName, name string) string {
	if engineName != config.PrismML {
		return name
	}
	stem := strings.TrimSuffix(strings.TrimSuffix(name, ".gguf"), ".GGUF")
	if i := strings.LastIndex(strings.ToLower(stem), "-gguf-"); i >= 0 && i+6 < len(stem) {
		stem = stem[i+6:]
	}
	stem = splitSuffix.ReplaceAllString(stem, "")
	stem = quantSuffix.ReplaceAllString(stem, "")
	if stem == "" {
		return name
	}
	return dropRepeat(stem)
}

// dropRepeat removes a tail that repeats the words before it: a file saved as the repository's
// name followed by the model's ("Org-Model-X-Model-X") reads "Org-Model-X".
func dropRepeat(stem string) string {
	words := strings.Split(stem, "-")
	for k := len(words) / 2; k >= 2; k-- {
		n := len(words)
		if strings.EqualFold(strings.Join(words[n-k:], "-"), strings.Join(words[n-2*k:n-k], "-")) {
			return strings.Join(words[:n-k], "-")
		}
	}
	return stem
}

// Find returns a model of the list by key.
func Find(list []Model, key string) (Model, bool) {
	for _, m := range list {
		if m.Key == key {
			return m, true
		}
	}
	return Model{}, false
}
