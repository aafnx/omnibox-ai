// Package engine knows how to start and talk to each engine: the command line built from
// structured settings, readiness probes and the model list.
package engine

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/aafnx/omnibox-ai/internal/config"
)

// Command is how an engine process is started.
type Command struct {
	Path string
	Args []string // without the executable
	Env  map[string]string
	Dir  string
}

// gpuMasks hide every GPU from Ollama when the owner chose CPU placement.
var gpuMasks = []string{"CUDA_VISIBLE_DEVICES", "ROCR_VISIBLE_DEVICES", "HIP_VISIBLE_DEVICES", "GGML_VK_VISIBLE_DEVICES"}

// Build turns an engine's launch into a command. The engine is limited to one model and one
// request at a time and listens on loopback only.
func Build(name string, e *config.Engine, l config.Launch) (Command, error) {
	if l.Executable == "" {
		return Command{}, fmt.Errorf("%s не установлен.", config.Label(name))
	}
	cmd := Command{Path: l.Executable, Dir: filepath.Dir(l.Executable), Env: maps.Clone(l.Environment)}
	if cmd.Env == nil {
		cmd.Env = map[string]string{}
	}
	switch name {
	case config.PrismML:
		cmd.Args = prismArgs(e, l.Settings)
	case config.Ollama:
		cmd.Args = []string{"serve"}
		ollamaEnv(e, l.Settings, cmd.Env)
	default:
		return Command{}, fmt.Errorf("Неизвестный движок: %s", name)
	}
	cmd.Args = append(cmd.Args, l.ExtraArguments...)
	return cmd, nil
}

func prismArgs(e *config.Engine, s map[string]string) []string {
	args := []string{"--host", "127.0.0.1", "--port", strconv.Itoa(e.Port)}
	add := func(flag, key string) {
		if v, ok := s[key]; ok && v != "" {
			args = append(args, flag, v)
		}
	}
	add("-m", "model")
	add("--alias", "alias")
	add("-c", "context")
	add("-ngl", "gpu_layers")
	add("--n-cpu-moe", "n_cpu_moe")
	add("-ctk", "cache_key")
	add("-ctv", "cache_value")
	add("-fa", "flash_attention")
	add("-b", "batch")
	add("-ub", "ubatch")
	if v, ok := s["idle_minutes"]; ok {
		n, _ := strconv.Atoi(v)
		args = append(args, "--sleep-idle-seconds", strconv.Itoa(n*60))
	}
	for _, o := range config.PrismOptions {
		add(o.Flags[0], o.Key)
	}
	if s["kv_offload"] == "off" {
		args = append(args, "--no-kv-offload")
	}
	// One slot: one generation at a time, the full context for it.
	return append(args, "-np", "1")
}

func ollamaEnv(e *config.Engine, s map[string]string, env map[string]string) {
	env["OLLAMA_HOST"] = "127.0.0.1:" + strconv.Itoa(e.Port)
	env["OLLAMA_MAX_LOADED_MODELS"] = "1"
	env["OLLAMA_NUM_PARALLEL"] = "1"
	if e.ModelsDirectory != "" {
		env["OLLAMA_MODELS"] = e.ModelsDirectory
	}
	if v := s["context"]; v != "" {
		env["OLLAMA_CONTEXT_LENGTH"] = v
	}
	if v := s["keep_alive"]; v != "" {
		env["OLLAMA_KEEP_ALIVE"] = v
	}
	if s["placement"] == "cpu" {
		for _, k := range gpuMasks {
			env[k] = "-1"
		}
	}
}

// EnvList returns the environment as sorted KEY=VALUE pairs for the process.
func (c Command) EnvList() []string {
	keys := slices.Sorted(maps.Keys(c.Env))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+c.Env[k])
	}
	return out
}
