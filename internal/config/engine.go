package config

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Launch is everything needed to start an engine. The command line is built from it at start,
// never edited as text.
type Launch struct {
	Executable     string            `json:"executable"`
	Settings       map[string]string `json:"settings"`
	ExtraArguments []string          `json:"extra_arguments,omitempty"`
	Environment    map[string]string `json:"environment,omitempty"`
}

// Equal reports whether two launches start the engine identically.
func (l Launch) Equal(o Launch) bool {
	return l.Executable == o.Executable && maps.Equal(l.Settings, o.Settings) &&
		slices.Equal(l.ExtraArguments, o.ExtraArguments) && maps.Equal(l.Environment, o.Environment)
}

// Clone returns a deep copy.
func (l Launch) Clone() Launch {
	l.Settings = maps.Clone(l.Settings)
	if l.Settings == nil {
		l.Settings = map[string]string{}
	}
	l.ExtraArguments = slices.Clone(l.ExtraArguments)
	l.Environment = maps.Clone(l.Environment)
	return l
}

// Engine is one engine in config.json.
type Engine struct {
	Port            int    `json:"port"`
	ModelsDirectory string `json:"models_directory,omitempty"`
	// Model is the Ollama model loaded through the panel; PrismML keeps its model in Settings.
	Model string `json:"model,omitempty"`
	Launch
	// Previous is the last configuration that started successfully; Pending means the current
	// launch has not started yet. A failed start of a pending change returns to Previous.
	Previous *Launch `json:"previous,omitempty"`
	Pending  bool    `json:"pending,omitempty"`
}

// BaseURL is the engine's loopback address.
func (e *Engine) BaseURL() string { return fmt.Sprintf("http://127.0.0.1:%d", e.Port) }

// Stage replaces the launch with one that has not started yet. Consecutive unapplied
// changes keep the same last working configuration as the rollback point.
func (e *Engine) Stage(next Launch) {
	if !e.Pending {
		prev := e.Launch.Clone()
		e.Previous = &prev
	}
	e.Launch = next.Clone()
	e.Pending = true
}

// Confirm marks the current launch as working after a successful start.
func (e *Engine) Confirm() { e.Pending = false }

// CanRestore reports whether a previous configuration exists.
func (e *Engine) CanRestore() bool { return e.Previous != nil }

// Restored reports that the previous configuration is already current.
func (e *Engine) Restored() bool {
	return e.Previous != nil && !e.Pending && e.Launch.Equal(*e.Previous)
}

// Restore makes the previous configuration current again.
func (e *Engine) Restore() error {
	if e.Previous == nil {
		return errors.New("Нет предыдущих настроек для восстановления.")
	}
	e.Launch = e.Previous.Clone()
	e.Pending = false
	return nil
}

// RunningLaunch is what a running engine was started with: while a change is pending,
// the engine still runs the previous configuration.
func (e *Engine) RunningLaunch() Launch {
	if e.Pending && e.Previous != nil {
		return *e.Previous
	}
	return e.Launch
}

var ollamaModel = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_./:-]*$`)

// ValidOllamaModel accepts an Ollama model name such as qwen3:8b or hf.co/org/repo:Q4_K_M.
func ValidOllamaModel(name string) bool {
	return len(name) <= 200 && ollamaModel.MatchString(name) && !strings.Contains(name, "..")
}

func (e *Engine) validate(name string) error {
	label := Label(name)
	if e.Port < 1024 || e.Port > 65535 {
		return fmt.Errorf("%s: порт от 1024 до 65535.", label)
	}
	if e.ModelsDirectory != "" && !FullyQualified(e.ModelsDirectory) {
		return fmt.Errorf("%s: каталог моделей должен быть полным путём.", label)
	}
	if e.Model != "" && (name != Ollama || !ValidOllamaModel(e.Model)) {
		return fmt.Errorf("%s: некорректное имя выбранной модели.", label)
	}
	for _, l := range append([]*Launch{&e.Launch}, e.Previous) {
		if l == nil {
			continue
		}
		if l.Executable != "" && !FullyQualified(l.Executable) {
			return fmt.Errorf("%s: путь к программе движка должен быть полным.", label)
		}
		if err := CheckSettings(name, l.Settings); err != nil {
			return fmt.Errorf("%s: %v", label, err)
		}
		for _, a := range l.ExtraArguments {
			if strings.ContainsAny(a, "\r\n\x00") {
				return fmt.Errorf("%s: переносы строк в аргументах запрещены.", label)
			}
		}
		for k, v := range l.Environment {
			if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsAny(v, "\x00") {
				return fmt.Errorf("%s: некорректная переменная окружения %q.", label, k)
			}
		}
	}
	return nil
}

// FullyQualified accepts absolute paths of either system, so a config written on Windows
// validates in tests on Linux and the reverse.
func FullyQualified(p string) bool {
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 3 && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) && p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}
