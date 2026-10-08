// Package reasoning describes how a model thinks before it answers and how a depth chosen in the
// panel is written into an OpenAI-compatible chat request.
//
// A Profile comes from the model's source: ForCloud (OpenRouter), ForLocal (a GGUF chat template
// run by llama-server) or ForOllama. Steps lists the positions of the depth slider; Apply* writes
// the chosen step into a request body for that engine, and Sanitize drops the parameters the
// model rejects.
package reasoning

import (
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Modes.
const (
	ModeNone   = "none"   // the model does not reason
	ModeToggle = "toggle" // reasoning can be switched off
	ModeAlways = "always" // reasoning cannot be disabled
)

// What the chat can display of the reasoning.
const (
	ShowFull    = "full"
	ShowSummary = "summary"
	ShowHidden  = "hidden"
)

// Steps that are not effort levels.
const (
	StepOff  = "off"  // reasoning switched off (toggle models)
	StepOn   = "on"   // reasoning on, the model picks the depth (toggle models without levels)
	StepAuto = "auto" // the only step of a model that always reasons without levels
)

// Efforts are all known effort levels, from the lightest.
var Efforts = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// budgetSteps are the slider steps of models whose depth is a token budget.
var budgetSteps = []string{"low", "medium", "high", "max"}

// Profile is what a model can do with reasoning.
type Profile struct {
	// Mode is ModeNone, ModeToggle or ModeAlways.
	Mode string `json:"mode"`
	// Efforts is the subset of Efforts the model accepts, in the same order.
	Efforts []string `json:"efforts,omitempty"`
	// Budget means the depth is set as a token budget (StepTokens gives it).
	Budget bool `json:"budget,omitempty"`
	// Default is the step matching what the model does when the request says nothing.
	Default string `json:"default,omitempty"`
	// Show is ShowFull, ShowSummary or ShowHidden.
	Show string `json:"show,omitempty"`
	// NoSampling: temperature, top_p, top_k and the like are rejected.
	NoSampling bool `json:"no_sampling,omitempty"`
	// NoForcedTools: tool_choice "required" or a named function is rejected.
	NoForcedTools bool `json:"no_forced_tools,omitempty"`
}

// Steps returns the depth slider positions for p, from "off" (or the lightest) to the deepest.
func Steps(p Profile) []string {
	steps := []string{}
	if p.Mode != ModeToggle && p.Mode != ModeAlways {
		return steps
	}
	if p.Mode == ModeToggle {
		steps = append(steps, StepOff)
	}
	switch {
	case len(p.Efforts) > 0:
		steps = append(steps, p.Efforts...)
	case p.Budget:
		steps = append(steps, budgetSteps...)
	case p.Mode == ModeToggle:
		steps = append(steps, StepOn)
	default:
		steps = append(steps, StepAuto)
	}
	return steps
}

// Choose returns step when it is one of Steps(p), otherwise p.Default ("" for ModeNone).
func Choose(p Profile, step string) string {
	if slices.Contains(Steps(p), step) {
		return step
	}
	return p.Default
}

// StepTokens is a rough range of reasoning tokens for a step: shown next to the slider and used
// as the budget (the upper bound) for Budget models. "on" and "auto" have no range (0, 0).
func StepTokens(step string) (min, max int) {
	switch step {
	case "minimal":
		return 200, 1000
	case "low":
		return 1000, 3000
	case "medium":
		return 3000, 8000
	case "high":
		return 8000, 20000
	case "xhigh":
		return 20000, 48000
	case "max":
		return 48000, 64000
	}
	return 0, 0
}

// cloudRule is one known model family on OpenRouter.
type cloudRule struct {
	match *regexp.Regexp // against the lower-case model id
	not   *regexp.Regexp // optional exclusion
	p     Profile
}

var (
	claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}
	openaiEfforts = []string{"minimal", "low", "medium", "high"}
	ossEfforts    = []string{"low", "medium", "high"}
)

// cloudRules are checked in order; the first match wins. OpenRouter lists "reasoning" in
// supported_parameters but does not say whether reasoning can be switched off, which effort levels
// exist or whether the depth is a budget, so the known families are described here.
var cloudRules = []cloudRule{
	// Claude Fable, Mythos and the 5.x generation: adaptive thinking that cannot be disabled,
	// effort levels, summarized thinking, fixed sampling and no forced tool use.
	{match: regexp.MustCompile(`claude-(fable|mythos|opus-5|sonnet-5|haiku-5)`),
		p: Profile{Mode: ModeAlways, Efforts: claudeEfforts, Default: "medium", Show: ShowSummary, NoSampling: true, NoForcedTools: true}},
	// Claude 3.7 and 4.x: extended thinking with a token budget, off unless asked for.
	{match: regexp.MustCompile(`claude-(3\.7-sonnet|sonnet-4|opus-4|haiku-4\.5)`),
		p: Profile{Mode: ModeToggle, Budget: true, Default: StepOff, Show: ShowSummary}},
	// gpt-oss always reasons, with three levels, and shows its raw reasoning.
	{match: regexp.MustCompile(`gpt-oss`),
		p: Profile{Mode: ModeAlways, Efforts: ossEfforts, Default: "medium", Show: ShowFull}},
	// OpenAI reasoning models; gpt-5-chat is the non-reasoning ChatGPT model.
	{match: regexp.MustCompile(`^openai/(gpt-5|o1|o3|o4)`), not: regexp.MustCompile(`gpt-5[\w.]*-chat`),
		p: Profile{Mode: ModeAlways, Efforts: openaiEfforts, Default: "medium", Show: ShowSummary, NoSampling: true}},
	{match: regexp.MustCompile(`gemini-(2\.5-pro|3[\w.]*-pro)`), not: regexp.MustCompile(`-image`),
		p: Profile{Mode: ModeAlways, Budget: true, Default: "medium", Show: ShowSummary}},
	{match: regexp.MustCompile(`gemini-2\.5-flash`), not: regexp.MustCompile(`-image`),
		p: Profile{Mode: ModeToggle, Budget: true, Default: "medium", Show: ShowSummary}},
	// Reasoning-only releases: DeepSeek R1 and its distills, "-thinking" and "-reasoner" variants.
	{match: regexp.MustCompile(`deepseek-r1|-thinking|-reasoner`),
		p: Profile{Mode: ModeAlways, Default: StepAuto, Show: ShowFull}},
	// Hybrid models with a thinking switch.
	{match: regexp.MustCompile(`deepseek-(chat-)?v(3\.[1-9]|[4-9])`),
		p: Profile{Mode: ModeToggle, Default: StepOff, Show: ShowFull}},
	{match: regexp.MustCompile(`qwen3`), not: regexp.MustCompile(`coder|instruct`),
		p: Profile{Mode: ModeToggle, Default: StepOn, Show: ShowFull}},
	{match: regexp.MustCompile(`glm-(4\.[5-9]|[5-9])`),
		p: Profile{Mode: ModeToggle, Default: StepOn, Show: ShowFull}},
}

// ForCloud returns the profile of an OpenRouter model by its id ("anthropic/claude-sonnet-4.5")
// and supported_parameters.
func ForCloud(id string, supported []string) Profile {
	low := strings.ToLower(id)
	p := Profile{Mode: ModeNone}
	found := false
	for _, r := range cloudRules {
		if r.match.MatchString(low) && (r.not == nil || !r.not.MatchString(low)) {
			p = r.p
			p.Efforts = slices.Clone(r.p.Efforts)
			found = true
			break
		}
	}
	if !found && (slices.Contains(supported, "reasoning") || slices.Contains(supported, "include_reasoning")) {
		p = Profile{Mode: ModeToggle, Default: StepOn, Show: ShowFull}
	}
	if len(supported) > 0 {
		if !slices.Contains(supported, "temperature") {
			p.NoSampling = true
		}
		if !slices.Contains(supported, "tool_choice") {
			p.NoForcedTools = true
		}
	}
	return p
}

// ForLocal returns the profile of a GGUF model run by llama-server: reasoning is whether its chat
// template produces reasoning, toggle whether the template reads enable_thinking.
func ForLocal(reasoning, toggle bool) Profile {
	switch {
	case !reasoning:
		return Profile{Mode: ModeNone}
	case toggle:
		return Profile{Mode: ModeToggle, Default: StepOn, Show: ShowFull}
	default:
		return Profile{Mode: ModeAlways, Default: StepAuto, Show: ShowFull}
	}
}

// ForOllama returns the profile of an Ollama model from /api/show: its capabilities and
// details.family.
func ForOllama(capabilities []string, family string) Profile {
	switch f := strings.ToLower(family); {
	case f == "gptoss" || f == "gpt-oss":
		return Profile{Mode: ModeAlways, Efforts: slices.Clone(ossEfforts), Default: "medium", Show: ShowFull}
	case slices.Contains(capabilities, "thinking"):
		return Profile{Mode: ModeToggle, Default: StepOn, Show: ShowFull}
	}
	return Profile{Mode: ModeNone}
}

// ApplyOpenRouter writes step into an OpenRouter chat request as the "reasoning" object and then
// sanitizes the request (see Sanitize, whose result it returns). An empty step or one that is not
// in Steps(p) leaves "reasoning" as the client sent it; "off" on a model that always reasons is
// ignored. OpenRouter returns the reasoning by default (reasoning.exclude is false), so nothing is
// added for display.
func ApplyOpenRouter(p Profile, step string, req map[string]any) (removed []string) {
	if p.Mode != ModeToggle && p.Mode != ModeAlways {
		delete(req, "reasoning")
		return Sanitize(p, req)
	}
	if !slices.Contains(Steps(p), step) || (step == StepOff && p.Mode != ModeToggle) {
		return Sanitize(p, req)
	}
	// Keep what else the client asked for, such as "exclude".
	r := map[string]any{}
	if old, ok := req["reasoning"].(map[string]any); ok {
		for k, v := range old {
			r[k] = v
		}
	}
	delete(r, "enabled")
	delete(r, "effort")
	delete(r, "max_tokens")
	_, budget := StepTokens(step)
	switch {
	case step == StepOff:
		r["enabled"] = false
	case slices.Contains(p.Efforts, step):
		r["effort"] = step
	case p.Budget && budget > 0:
		r["max_tokens"] = budget
	default:
		r["enabled"] = true
	}
	req["reasoning"] = r
	return Sanitize(p, req)
}

// ApplyLlama writes step into a llama-server request: for toggle models it sets
// chat_template_kwargs.enable_thinking, keeping the other template arguments.
func ApplyLlama(p Profile, step string, req map[string]any) {
	if p.Mode != ModeToggle || !slices.Contains(Steps(p), step) {
		return
	}
	kw := map[string]any{}
	if old, ok := req["chat_template_kwargs"].(map[string]any); ok {
		for k, v := range old {
			kw[k] = v
		}
	}
	kw["enable_thinking"] = step != StepOff
	req["chat_template_kwargs"] = kw
}

// ApplyOllama writes step into an Ollama request: "think" for toggle models, "reasoning_effort"
// for models with levels.
func ApplyOllama(p Profile, step string, req map[string]any) {
	if !slices.Contains(Steps(p), step) {
		return
	}
	if p.Mode == ModeToggle {
		req["think"] = step != StepOff
	}
	if slices.Contains(p.Efforts, step) {
		req["reasoning_effort"] = step
	}
}

// samplingKeys are dropped for NoSampling models.
var samplingKeys = []string{"temperature", "top_p", "top_k", "min_p", "repeat_penalty", "presence_penalty", "frequency_penalty", "seed", "logit_bias"}

// Sanitize removes the parameters the model rejects and returns their names, sorted, for the
// log: sampling parameters for NoSampling models; for NoForcedTools models a tool_choice of
// "required" or a named function becomes "auto" and "tool_choice" is listed too.
func Sanitize(p Profile, req map[string]any) (removed []string) {
	if p.NoSampling {
		for _, k := range samplingKeys {
			if _, ok := req[k]; ok {
				delete(req, k)
				removed = append(removed, k)
			}
		}
	}
	if p.NoForcedTools {
		switch v := req["tool_choice"].(type) {
		case string:
			if v == "required" {
				req["tool_choice"] = "auto"
				removed = append(removed, "tool_choice")
			}
		case map[string]any:
			req["tool_choice"] = "auto"
			removed = append(removed, "tool_choice")
		}
	}
	sort.Strings(removed)
	return removed
}
