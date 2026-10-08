package reasoning

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

// openRouterParams is a typical supported_parameters list of a reasoning model.
var openRouterParams = []string{"max_tokens", "temperature", "top_p", "tools", "tool_choice", "reasoning", "include_reasoning"}

func TestStepsAndTokens(t *testing.T) {
	cases := []struct {
		p    Profile
		want []string
	}{
		{Profile{Mode: ModeNone}, []string{}},
		{Profile{}, []string{}},
		{Profile{Mode: ModeToggle}, []string{"off", "on"}},
		{Profile{Mode: ModeAlways}, []string{"auto"}},
		{Profile{Mode: ModeToggle, Budget: true}, []string{"off", "low", "medium", "high", "max"}},
		{Profile{Mode: ModeAlways, Budget: true}, []string{"low", "medium", "high", "max"}},
		{Profile{Mode: ModeAlways, Efforts: []string{"minimal", "low", "medium", "high"}}, []string{"minimal", "low", "medium", "high"}},
		{Profile{Mode: ModeToggle, Efforts: []string{"low", "high"}}, []string{"off", "low", "high"}},
	}
	for _, c := range cases {
		if got := Steps(c.p); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Steps(%+v) = %v, want %v", c.p, got, c.want)
		}
	}
	tokens := map[string][2]int{"off": {0, 0}, "on": {0, 0}, "auto": {0, 0}, "minimal": {200, 1000}, "low": {1000, 3000},
		"medium": {3000, 8000}, "high": {8000, 20000}, "xhigh": {20000, 48000}, "max": {48000, 64000}}
	for step, want := range tokens {
		if lo, hi := StepTokens(step); lo != want[0] || hi != want[1] {
			t.Errorf("StepTokens(%s) = %d..%d, want %v", step, lo, hi, want)
		}
	}
	p := Profile{Mode: ModeAlways, Efforts: []string{"low", "medium"}, Default: "medium"}
	if Choose(p, "low") != "low" || Choose(p, "off") != "medium" || Choose(p, "") != "medium" {
		t.Error("Choose must keep a valid step and fall back to Default")
	}
	if Choose(Profile{Mode: ModeNone}, "on") != "" {
		t.Error("a model without reasoning has no step")
	}
}

func TestForCloudKnownFamilies(t *testing.T) {
	adaptive := Profile{Mode: ModeAlways, Efforts: []string{"low", "medium", "high", "xhigh", "max"}, Default: "medium", Show: ShowSummary, NoSampling: true, NoForcedTools: true}
	extended := Profile{Mode: ModeToggle, Budget: true, Default: "off", Show: ShowSummary}
	openai := Profile{Mode: ModeAlways, Efforts: []string{"minimal", "low", "medium", "high"}, Default: "medium", Show: ShowSummary, NoSampling: true}
	cases := map[string]Profile{
		"anthropic/claude-fable-5":           adaptive,
		"anthropic/claude-fable-5.1":         adaptive,
		"anthropic/claude-mythos-preview":    adaptive,
		"anthropic/claude-opus-5":            adaptive,
		"anthropic/claude-opus-5.5":          adaptive,
		"anthropic/claude-sonnet-5":          adaptive,
		"anthropic/claude-sonnet-5.5":        adaptive,
		"anthropic/claude-haiku-5.5":         adaptive,
		"anthropic/claude-3.7-sonnet":        extended,
		"anthropic/claude-sonnet-4":          extended,
		"anthropic/claude-sonnet-4.5":        extended,
		"anthropic/claude-opus-4.1":          extended,
		"openai/gpt-5":                       openai,
		"openai/gpt-5-mini":                  openai,
		"openai/o3":                          openai,
		"openai/o4-mini-high":                openai,
		"openai/gpt-oss-120b":                {Mode: ModeAlways, Efforts: []string{"low", "medium", "high"}, Default: "medium", Show: ShowFull},
		"google/gemini-2.5-pro":              {Mode: ModeAlways, Budget: true, Default: "medium", Show: ShowSummary},
		"google/gemini-3-pro-preview":        {Mode: ModeAlways, Budget: true, Default: "medium", Show: ShowSummary},
		"google/gemini-2.5-flash":            {Mode: ModeToggle, Budget: true, Default: "medium", Show: ShowSummary},
		"deepseek/deepseek-r1":               {Mode: ModeAlways, Default: "auto", Show: ShowFull},
		"deepseek/deepseek-r1-0528:free":     {Mode: ModeAlways, Default: "auto", Show: ShowFull},
		"qwen/qwen3-235b-a22b-thinking-2507": {Mode: ModeAlways, Default: "auto", Show: ShowFull},
		"deepseek/deepseek-chat-v3.1":        {Mode: ModeToggle, Default: "off", Show: ShowFull},
		"deepseek/deepseek-v3.2-exp":         {Mode: ModeToggle, Default: "off", Show: ShowFull},
		"qwen/qwen3-32b":                     {Mode: ModeToggle, Default: "on", Show: ShowFull},
		"z-ai/glm-4.5":                       {Mode: ModeToggle, Default: "on", Show: ShowFull},
		"z-ai/glm-4.6":                       {Mode: ModeToggle, Default: "on", Show: ShowFull},
	}
	for id, want := range cases {
		// With full supported_parameters the table alone decides.
		if got := ForCloud(id, openRouterParams); !reflect.DeepEqual(got, want) {
			t.Errorf("ForCloud(%s) = %+v, want %+v", id, got, want)
		}
	}
	// Neither family rules nor reasoning: plain chat models.
	for _, id := range []string{"openai/gpt-5-chat", "openai/gpt-4o", "qwen/qwen3-coder", "qwen/qwen3-235b-a22b-instruct-2507",
		"deepseek/deepseek-chat", "deepseek/deepseek-chat-v3-0324", "google/gemini-2.5-flash-image", "z-ai/glm-4-32b", "anthropic/claude-3.5-sonnet"} {
		if got := ForCloud(id, []string{"temperature", "tool_choice"}); got.Mode != ModeNone {
			t.Errorf("ForCloud(%s) = %+v, want none", id, got)
		}
	}
}

func TestForCloudFable(t *testing.T) {
	p := ForCloud("anthropic/claude-fable-5", openRouterParams)
	steps := Steps(p)
	if slices.Contains(steps, "off") || !reflect.DeepEqual(steps, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("Fable steps = %v: thinking cannot be switched off", steps)
	}
	// The table must not be changed through a returned profile.
	p.Efforts[0] = "broken"
	if ForCloud("anthropic/claude-fable-5", nil).Efforts[0] != "low" {
		t.Fatal("ForCloud returned the table's own slice")
	}
}

func TestForCloudBySupportedParameters(t *testing.T) {
	if p := ForCloud("someone/new-model", []string{"temperature", "tool_choice", "reasoning"}); !reflect.DeepEqual(p, Profile{Mode: ModeToggle, Default: "on", Show: ShowFull}) {
		t.Errorf("reasoning parameter: %+v", p)
	}
	if p := ForCloud("someone/new-model", []string{"temperature", "tool_choice", "include_reasoning"}); p.Mode != ModeToggle {
		t.Errorf("include_reasoning parameter: %+v", p)
	}
	if p := ForCloud("someone/new-model", []string{"temperature", "tool_choice"}); !reflect.DeepEqual(p, Profile{Mode: ModeNone}) {
		t.Errorf("no reasoning: %+v", p)
	}
	// An unknown list says nothing about sampling or tools.
	if p := ForCloud("someone/new-model", nil); !reflect.DeepEqual(p, Profile{Mode: ModeNone}) {
		t.Errorf("no parameters: %+v", p)
	}
	p := ForCloud("someone/new-model", []string{"max_tokens", "reasoning"})
	if !p.NoSampling || !p.NoForcedTools {
		t.Errorf("missing temperature and tool_choice must set NoSampling and NoForcedTools: %+v", p)
	}
	// The parameters add restrictions to a known family, never remove them.
	if p := ForCloud("anthropic/claude-opus-5", []string{"temperature", "tool_choice", "reasoning"}); !p.NoSampling || !p.NoForcedTools {
		t.Errorf("Opus 5 restrictions lost: %+v", p)
	}
	if p := ForCloud("anthropic/claude-sonnet-4.5", []string{"reasoning", "tool_choice"}); !p.NoSampling || p.NoForcedTools || p.Mode != ModeToggle {
		t.Errorf("Sonnet 4.5 without temperature: %+v", p)
	}
}

func TestForLocalAndOllama(t *testing.T) {
	if p := ForLocal(false, false); p.Mode != ModeNone || len(Steps(p)) != 0 {
		t.Errorf("no reasoning: %+v", p)
	}
	if p := ForLocal(true, true); p.Mode != ModeToggle || p.Show != ShowFull || !reflect.DeepEqual(Steps(p), []string{"off", "on"}) {
		t.Errorf("toggle template: %+v", p)
	}
	if p := ForLocal(true, false); p.Mode != ModeAlways || p.Show != ShowFull || !reflect.DeepEqual(Steps(p), []string{"auto"}) {
		t.Errorf("always template: %+v", p)
	}
	if p := ForOllama([]string{"completion", "tools", "thinking"}, "qwen3"); p.Mode != ModeToggle || p.Show != ShowFull {
		t.Errorf("thinking capability: %+v", p)
	}
	for _, family := range []string{"gptoss", "gpt-oss"} {
		p := ForOllama([]string{"completion", "tools", "thinking"}, family)
		if p.Mode != ModeAlways || !reflect.DeepEqual(Steps(p), []string{"low", "medium", "high"}) || p.Default != "medium" || p.Show != ShowFull {
			t.Errorf("%s: %+v", family, p)
		}
	}
	if p := ForOllama([]string{"completion", "vision"}, "gemma3"); p.Mode != ModeNone {
		t.Errorf("no thinking: %+v", p)
	}
}

func reasoningOf(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	r, ok := req["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("no reasoning object in %v", req)
	}
	return r
}

func TestApplyOpenRouter(t *testing.T) {
	sonnet4 := ForCloud("anthropic/claude-sonnet-4.5", openRouterParams)
	gpt5 := ForCloud("openai/gpt-5", openRouterParams)
	fable := ForCloud("anthropic/claude-fable-5", openRouterParams)
	r1 := ForCloud("deepseek/deepseek-r1", openRouterParams)
	glm := ForCloud("z-ai/glm-4.6", openRouterParams)

	req := map[string]any{"reasoning": map[string]any{"exclude": true, "effort": "low"}}
	ApplyOpenRouter(sonnet4, "high", req)
	if r := reasoningOf(t, req); !reflect.DeepEqual(r, map[string]any{"max_tokens": 20000, "exclude": true}) {
		t.Errorf("budget step: %v", r)
	}
	req = map[string]any{}
	ApplyOpenRouter(sonnet4, "off", req)
	if r := reasoningOf(t, req); !reflect.DeepEqual(r, map[string]any{"enabled": false}) {
		t.Errorf("off: %v", r)
	}
	req = map[string]any{}
	ApplyOpenRouter(gpt5, "minimal", req)
	if r := reasoningOf(t, req); !reflect.DeepEqual(r, map[string]any{"effort": "minimal"}) {
		t.Errorf("effort: %v", r)
	}
	req = map[string]any{}
	ApplyOpenRouter(fable, "max", req)
	if r := reasoningOf(t, req); !reflect.DeepEqual(r, map[string]any{"effort": "max"}) {
		t.Errorf("Fable max: %v", r)
	}
	// "off" cannot switch off a model that always reasons.
	req = map[string]any{}
	ApplyOpenRouter(fable, "off", req)
	if _, ok := req["reasoning"]; ok {
		t.Errorf("off on Fable: %v", req)
	}
	req = map[string]any{}
	ApplyOpenRouter(r1, "auto", req)
	if r := reasoningOf(t, req); !reflect.DeepEqual(r, map[string]any{"enabled": true}) {
		t.Errorf("auto: %v", r)
	}
	req = map[string]any{}
	ApplyOpenRouter(glm, "on", req)
	if r := reasoningOf(t, req); !reflect.DeepEqual(r, map[string]any{"enabled": true}) {
		t.Errorf("on: %v", r)
	}
	// A step from another model or none at all leaves the client's choice.
	req = map[string]any{"reasoning": map[string]any{"effort": "high"}}
	ApplyOpenRouter(gpt5, "xhigh", req)
	ApplyOpenRouter(gpt5, "", req)
	if r := reasoningOf(t, req); !reflect.DeepEqual(r, map[string]any{"effort": "high"}) {
		t.Errorf("foreign step: %v", r)
	}
	// A model without reasoning gets no reasoning object.
	req = map[string]any{"reasoning": map[string]any{"effort": "high"}, "temperature": 0.5}
	if removed := ApplyOpenRouter(Profile{Mode: ModeNone}, "high", req); len(removed) != 0 || req["reasoning"] != nil || req["temperature"] != 0.5 {
		t.Errorf("none: %v, removed %v", req, removed)
	}
	// Sanitize runs after the step is written.
	req = map[string]any{"temperature": 0.7, "top_k": 40, "tool_choice": "required"}
	removed := ApplyOpenRouter(fable, "low", req)
	if !reflect.DeepEqual(removed, []string{"temperature", "tool_choice", "top_k"}) || req["tool_choice"] != "auto" {
		t.Errorf("Fable sanitize: %v, removed %v", req, removed)
	}
	// The result is valid request JSON.
	if _, err := json.Marshal(req); err != nil {
		t.Fatal(err)
	}
}

func TestApplyLlama(t *testing.T) {
	toggle := ForLocal(true, true)
	req := map[string]any{"chat_template_kwargs": map[string]any{"custom": 1}}
	ApplyLlama(toggle, "off", req)
	if kw := req["chat_template_kwargs"]; !reflect.DeepEqual(kw, map[string]any{"custom": 1, "enable_thinking": false}) {
		t.Errorf("off: %v", kw)
	}
	ApplyLlama(toggle, "on", req)
	if kw := req["chat_template_kwargs"]; !reflect.DeepEqual(kw, map[string]any{"custom": 1, "enable_thinking": true}) {
		t.Errorf("on: %v", kw)
	}
	for _, p := range []Profile{ForLocal(true, false), ForLocal(false, false)} {
		req := map[string]any{}
		ApplyLlama(p, "auto", req)
		ApplyLlama(p, "off", req)
		if len(req) != 0 {
			t.Errorf("%s: %v", p.Mode, req)
		}
	}
}

func TestApplyOllama(t *testing.T) {
	thinking := ForOllama([]string{"thinking"}, "qwen3")
	req := map[string]any{}
	ApplyOllama(thinking, "off", req)
	if req["think"] != false {
		t.Errorf("off: %v", req)
	}
	ApplyOllama(thinking, "on", req)
	if req["think"] != true {
		t.Errorf("on: %v", req)
	}
	oss := ForOllama(nil, "gptoss")
	req = map[string]any{}
	ApplyOllama(oss, "high", req)
	if !reflect.DeepEqual(req, map[string]any{"reasoning_effort": "high"}) {
		t.Errorf("gpt-oss: %v", req)
	}
	req = map[string]any{}
	ApplyOllama(ForOllama(nil, "llama"), "on", req)
	ApplyOllama(oss, "off", req)
	if len(req) != 0 {
		t.Errorf("foreign steps: %v", req)
	}
}

func TestSanitize(t *testing.T) {
	all := map[string]any{"model": "m", "messages": []any{}, "temperature": 1, "top_p": 1, "top_k": 1, "min_p": 1, "repeat_penalty": 1,
		"presence_penalty": 1, "frequency_penalty": 1, "seed": 1, "logit_bias": map[string]any{}, "max_tokens": 100,
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": "f"}}}
	req := map[string]any{}
	for k, v := range all {
		req[k] = v
	}
	if removed := Sanitize(Profile{Mode: ModeToggle}, req); removed != nil || len(req) != len(all) {
		t.Errorf("unrestricted model changed: %v", removed)
	}
	removed := Sanitize(Profile{NoSampling: true, NoForcedTools: true}, req)
	want := []string{"frequency_penalty", "logit_bias", "min_p", "presence_penalty", "repeat_penalty", "seed", "temperature", "tool_choice", "top_k", "top_p"}
	if !reflect.DeepEqual(removed, want) {
		t.Errorf("removed %v, want %v", removed, want)
	}
	if !reflect.DeepEqual(req, map[string]any{"model": "m", "messages": []any{}, "max_tokens": 100, "tool_choice": "auto"}) {
		t.Errorf("sanitized request: %v", req)
	}
	for _, choice := range []any{"auto", "none"} {
		req := map[string]any{"tool_choice": choice}
		if removed := Sanitize(Profile{NoForcedTools: true}, req); removed != nil || req["tool_choice"] != choice {
			t.Errorf("tool_choice %v changed: %v", choice, removed)
		}
	}
}
