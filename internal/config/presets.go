package config

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Preset is a named set of launch settings applied in one action, with the usual rollback.
// Its settings are changes of the settings form: a preset never picks the model.
type Preset struct {
	Name     string            `json:"name"`
	Builtin  bool              `json:"builtin"`
	Settings map[string]string `json:"settings"`
	// Auto presets are fitted to the selected model and the GPU when applied.
	Auto bool `json:"auto,omitempty"`
}

// BasePreset is the starting point on the owner's PC (RTX 5080, 16 GB VRAM): Flash Attention,
// a q8_0 KV cache and full batches, with the context fitted to the model when applied.
const BasePreset = "Базовый"

// BaseSettings are the base preset's form values before fitting. A dense model's layers all go
// to the GPU and no MoE experts to RAM, so values left by an earlier preset do not linger.
func BaseSettings(engine string) map[string]string {
	if engine == Ollama {
		return map[string]string{"context": "32768", "placement": "auto"}
	}
	return map[string]string{"placement": "gpu", "context": "32768", "flash_attention": "on", "cache_key": "q8_0", "cache_value": "q8_0",
		"batch": "2048", "ubatch": "512", "n_cpu_moe": "0"}
}

// MaxPresets is how many own presets an engine keeps.
const MaxPresets = 20

var presetName = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._+-]{0,39}$`)

// BuiltinPresets are the presets every engine has: the base one, long code sessions, long chats
// and RAM only.
func BuiltinPresets(engine string) []Preset {
	out := []Preset{
		{Name: BasePreset, Builtin: true, Auto: engine == PrismML, Settings: BaseSettings(engine)},
		{Name: "Код 32k", Builtin: true, Settings: map[string]string{"context": "32768"}},
		{Name: "Чат 128k", Builtin: true, Settings: map[string]string{"context": "131072"}},
		{Name: "CPU", Builtin: true, Settings: map[string]string{"placement": "cpu"}},
	}
	if engine == PrismML {
		out[1].Settings["placement"] = "gpu"
	}
	return out
}

// PresetList lists the built-in presets and the engine's own, in name order.
func (e *Engine) PresetList(engine string) []Preset {
	out := BuiltinPresets(engine)
	names := slices.Collect(maps.Keys(e.Presets))
	sort.Strings(names)
	for _, n := range names {
		out = append(out, Preset{Name: n, Settings: maps.Clone(e.Presets[n])})
	}
	return out
}

// FindPreset returns a preset by name.
func (e *Engine) FindPreset(engine, name string) (Preset, bool) {
	for _, p := range e.PresetList(engine) {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}

// PresetFrom turns stored settings into form changes that Apply accepts, without the model.
func PresetFrom(engine string, s map[string]string) map[string]string {
	out := map[string]string{}
	if engine == Ollama {
		for _, key := range []string{"context", "placement"} {
			if v := s[key]; v != "" {
				out[key] = v
			}
		}
		if m, ok := keepAliveMinutes(s["keep_alive"]).(int); ok && m >= 1 {
			out["idle_minutes"] = strconv.Itoa(m)
		}
		return out
	}
	for key, v := range s {
		if key != "model" && key != "alias" && key != "mmproj" {
			out[key] = v
		}
	}
	return out
}

// SavePreset stores the current launch settings under name.
func (e *Engine) SavePreset(engine, name string) error {
	name = strings.TrimSpace(name)
	if !presetName.MatchString(name) {
		return errors.New("Имя пресета: буквы, цифры, пробел, точка, дефис, до 40 символов.")
	}
	for _, b := range BuiltinPresets(engine) {
		if b.Name == name {
			return fmt.Errorf("«%s» — встроенный пресет; выберите другое имя.", name)
		}
	}
	if _, ok := e.Presets[name]; !ok && len(e.Presets) >= MaxPresets {
		return fmt.Errorf("Не больше %d своих пресетов; удалите ненужный.", MaxPresets)
	}
	if e.Presets == nil {
		e.Presets = map[string]map[string]string{}
	}
	e.Presets[name] = PresetFrom(engine, e.Settings)
	return nil
}

// DeletePreset removes an own preset.
func (e *Engine) DeletePreset(engine, name string) error {
	if _, ok := e.Presets[name]; !ok {
		for _, b := range BuiltinPresets(engine) {
			if b.Name == name {
				return errors.New("Встроенный пресет удалить нельзя.")
			}
		}
		return fmt.Errorf("Пресета «%s» нет.", name)
	}
	delete(e.Presets, name)
	return nil
}
