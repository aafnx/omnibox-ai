package config

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Option is an optional llama-server setting: empty means the engine's own default applies,
// so older builds without the flag still start.
type Option struct {
	Key     string
	Flags   []string // flags[0] is written; every form is recognised when importing
	Values  []string // allowed words, or nil for numbers
	Min     float64
	Max     float64
	Decimal bool
	Bare    []string // bare flags that mean the same setting (removed on import)
}

// PrismOptions are the optional llama-server settings in panel order.
var PrismOptions = []Option{
	{Key: "reasoning", Flags: []string{"--reasoning", "-rea"}, Values: []string{"on", "off", "auto"}},
	{Key: "reasoning_effort", Flags: []string{"--reasoning-effort"}, Values: []string{"minimal", "low", "medium", "high", "xhigh", "max"}},
	{Key: "reasoning_budget", Flags: []string{"--reasoning-budget"}, Min: -1, Max: 1000000},
	{Key: "temperature", Flags: []string{"--temp", "--temperature"}, Min: 0, Max: 2, Decimal: true},
	{Key: "top_k", Flags: []string{"--top-k"}, Min: 0, Max: 1000},
	{Key: "top_p", Flags: []string{"--top-p"}, Min: 0, Max: 1, Decimal: true},
	{Key: "min_p", Flags: []string{"--min-p"}, Min: 0, Max: 1, Decimal: true},
	{Key: "repeat_penalty", Flags: []string{"--repeat-penalty"}, Min: 0, Max: 2, Decimal: true},
	{Key: "presence_penalty", Flags: []string{"--presence-penalty"}, Min: -2, Max: 2, Decimal: true},
	{Key: "frequency_penalty", Flags: []string{"--frequency-penalty"}, Min: -2, Max: 2, Decimal: true},
	{Key: "max_tokens", Flags: []string{"-n", "--predict", "--n-predict"}, Min: -1, Max: 1000000},
	{Key: "seed", Flags: []string{"-s", "--seed"}, Min: -1, Max: 4294967295},
	{Key: "threads", Flags: []string{"-t", "--threads"}, Min: 1, Max: 256},
	{Key: "load_mode", Flags: []string{"--load-mode", "-lm"}, Values: []string{"auto", "none", "mmap", "mlock", "mmap+mlock"}, Bare: []string{"--mlock", "--mmap", "--no-mmap"}},
}

// CacheTypes are the KV cache quantizations llama-server accepts.
var CacheTypes = []string{"f16", "bf16", "q8_0", "q5_1", "q5_0", "q4_1", "iq4_nl", "q4_0"}

// Core PrismML settings and their llama-server flags.
var PrismCore = map[string][]string{
	"model":           {"-m", "--model"},
	"alias":           {"--alias"},
	"context":         {"-c", "--ctx-size"},
	"gpu_layers":      {"-ngl", "--gpu-layers", "--n-gpu-layers"},
	"n_cpu_moe":       {"--n-cpu-moe", "-ncmoe"},
	"cache_key":       {"-ctk", "--cache-type-k"},
	"cache_value":     {"-ctv", "--cache-type-v"},
	"flash_attention": {"-fa", "--flash-attn"},
	"batch":           {"-b", "--batch-size"},
	"ubatch":          {"-ub", "--ubatch-size"},
}

var (
	digitsOnly    = regexp.MustCompile(`^\d+$`)
	aliasPattern  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,79}$`)
	intPattern    = regexp.MustCompile(`^-?\d{1,10}$`)
	decimalNumber = regexp.MustCompile(`^-?\d{1,7}(\.\d{1,4})?$`)
	contextValue  = regexp.MustCompile(`^([0-9]+)([kKmM]?)$`)
	keepAlive     = regexp.MustCompile(`^(-1|0|\d+[smh]?)$`)
)

func optionByKey(key string) (Option, bool) {
	for _, o := range PrismOptions {
		if o.Key == key {
			return o, true
		}
	}
	return Option{}, false
}

// ParseContext accepts tokens or 32k/128k/1m and returns the token count.
func ParseContext(value string) (int, error) {
	m := contextValue.FindStringSubmatch(strings.TrimSpace(value))
	bad := errors.New("Контекст должен быть от 1024 до 1048576 токенов.")
	if m == nil {
		return 0, errors.New("Контекст: целое число токенов или 32k/128k/192k.")
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n < 1 || n > 1048576 {
		return 0, bad
	}
	switch strings.ToLower(m[2]) {
	case "k":
		n *= 1024
	case "m":
		n *= 1048576
	}
	if n < 1024 || n > 1048576 {
		return 0, bad
	}
	return int(n), nil
}

func checkRange(key, value string, min, max int64) error {
	if !digitsOnly.MatchString(value) {
		return fmt.Errorf("%s должен быть от %d до %d.", key, min, max)
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < min || n > max {
		return fmt.Errorf("%s должен быть от %d до %d.", key, min, max)
	}
	return nil
}

func checkOption(o Option, value string) error {
	if o.Values != nil {
		if !slices.Contains(o.Values, value) {
			return fmt.Errorf("%s: допустимо %s или пусто.", o.Key, strings.Join(o.Values, ", "))
		}
		return nil
	}
	pattern := intPattern
	if o.Decimal {
		pattern = decimalNumber
	}
	n, err := strconv.ParseFloat(value, 64)
	if !pattern.MatchString(value) || err != nil || n < o.Min || n > o.Max {
		return fmt.Errorf("%s: от %s до %s или пусто.", o.Key, formatNumber(o.Min), formatNumber(o.Max))
	}
	return nil
}

func formatNumber(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// CheckSettings validates stored settings of an engine (config.json is edited only by the
// service, but a damaged file must not start an engine with bad flags).
func CheckSettings(engine string, s map[string]string) error {
	for key, value := range s {
		if err := checkStored(engine, key, value); err != nil {
			return err
		}
	}
	return nil
}

func checkStored(engine, key, value string) error {
	if value == "" {
		return fmt.Errorf("пустое значение %s.", key)
	}
	if key == "context" {
		_, err := ParseContext(value)
		return err
	}
	if engine == Ollama {
		switch key {
		case "keep_alive":
			if !keepAlive.MatchString(value) {
				return errors.New("keep_alive: например 30m, 1h или -1.")
			}
			return nil
		case "placement":
			if value != "cpu" && value != "auto" {
				return errors.New("Недопустимое размещение модели.")
			}
			return nil
		}
		return fmt.Errorf("Неизвестная настройка: %s", key)
	}
	switch key {
	case "model":
		if strings.ContainsAny(value, "\"\r\n") || !FullyQualified(value) {
			return errors.New("model: полный путь к GGUF.")
		}
		return nil
	case "alias":
		if !aliasPattern.MatchString(value) {
			return errors.New("Имя API-модели: латиница, цифры, точка, дефис или подчёркивание, до 80 символов.")
		}
		return nil
	case "gpu_layers":
		return checkRange(key, value, 0, 999)
	case "batch", "ubatch":
		return checkRange(key, value, 1, 8192)
	case "n_cpu_moe":
		return checkRange(key, value, 1, 999)
	case "idle_minutes":
		return checkRange(key, value, 1, 1440)
	case "cache_key", "cache_value":
		if !slices.Contains(CacheTypes, value) {
			return fmt.Errorf("Кеш: %s.", strings.Join(CacheTypes, ", "))
		}
		return nil
	case "flash_attention":
		if value != "on" && value != "off" && value != "auto" {
			return errors.New("Flash Attention: on, off или auto.")
		}
		return nil
	case "kv_offload":
		if value != "on" && value != "off" {
			return errors.New("KV-кеш на GPU: on или off.")
		}
		return nil
	}
	if o, ok := optionByKey(key); ok {
		return checkOption(o, value)
	}
	return fmt.Errorf("Неизвестная настройка: %s", key)
}

// Apply validates the panel's settings form and returns the new stored settings.
// knownModels are the GGUF files the panel offered; arbitrary paths are refused.
func Apply(engine string, current map[string]string, changes map[string]string, knownModels []string) (map[string]string, error) {
	allowed := []string{"context", "idle_minutes", "placement"}
	if engine == PrismML {
		allowed = append(allowed, "model", "alias", "gpu_layers", "n_cpu_moe", "cache_key", "cache_value", "flash_attention", "batch", "ubatch", "kv_offload")
		for _, o := range PrismOptions {
			allowed = append(allowed, o.Key)
		}
	}
	for key := range changes {
		if !slices.Contains(allowed, key) {
			return nil, fmt.Errorf("Неизвестная настройка: %s", key)
		}
	}
	next := maps.Clone(current)
	if next == nil {
		next = map[string]string{}
	}
	if v, ok := changes["context"]; ok {
		n, err := ParseContext(v)
		if err != nil {
			return nil, err
		}
		next["context"] = strconv.Itoa(n)
	}
	placement, hasPlacement := changes["placement"]
	if hasPlacement {
		values := []string{"cpu", "auto"}
		if engine == PrismML {
			values = []string{"cpu", "gpu", "hybrid", "auto"}
		}
		if !slices.Contains(values, placement) {
			return nil, errors.New("Недопустимое размещение модели.")
		}
		if engine == Ollama {
			next["placement"] = placement
		}
	}
	if v, ok := changes["idle_minutes"]; ok {
		if err := checkRange("idle_minutes", v, 1, 1440); err != nil {
			return nil, errors.New("Простой должен быть от 1 до 1440 минут.")
		}
		n, _ := strconv.Atoi(v)
		if engine == PrismML {
			next["idle_minutes"] = strconv.Itoa(n)
		} else {
			next["keep_alive"] = strconv.Itoa(n) + "m"
		}
	}
	if engine == Ollama {
		return next, nil
	}
	if v, ok := changes["model"]; ok {
		if !slices.Contains(knownModels, v) {
			return nil, errors.New("Выбери существующий GGUF из каталога моделей; произвольные пути запрещены.")
		}
		next["model"] = v
	}
	if v, ok := changes["alias"]; ok {
		if !aliasPattern.MatchString(v) {
			return nil, errors.New("Имя API-модели: латиница, цифры, точка, дефис или подчёркивание, до 80 символов.")
		}
		next["alias"] = v
	}
	for _, key := range []string{"gpu_layers", "batch", "ubatch"} {
		if v, ok := changes[key]; ok {
			min, max := int64(1), int64(8192)
			if key == "gpu_layers" {
				min, max = 0, 999
			}
			if err := checkRange(key, v, min, max); err != nil {
				return nil, err
			}
			n, _ := strconv.Atoi(v)
			next[key] = strconv.Itoa(n)
		}
	}
	if v, ok := changes["n_cpu_moe"]; ok {
		if !digitsOnly.MatchString(v) || len(v) > 4 {
			return nil, errors.New("Слои MoE в RAM: от 0 до 999.")
		}
		n, _ := strconv.Atoi(v)
		if n > 999 {
			return nil, errors.New("Слои MoE в RAM: от 0 до 999.")
		}
		delete(next, "n_cpu_moe")
		if n > 0 {
			next["n_cpu_moe"] = strconv.Itoa(n)
		}
	}
	for _, key := range []string{"cache_key", "cache_value"} {
		if v, ok := changes[key]; ok {
			if !slices.Contains(CacheTypes, v) {
				return nil, fmt.Errorf("Кеш: %s.", strings.Join(CacheTypes, ", "))
			}
			next[key] = v
		}
	}
	if v, ok := changes["flash_attention"]; ok {
		if v != "on" && v != "off" && v != "auto" {
			return nil, errors.New("Flash Attention: on, off или auto.")
		}
		next["flash_attention"] = v
	}
	if hasPlacement {
		switch placement {
		case "cpu":
			// CPU keeps every layer, every expert and the KV cache in RAM.
			next["gpu_layers"] = "0"
			delete(next, "n_cpu_moe")
			next["kv_offload"] = "off"
		case "gpu", "auto":
			next["gpu_layers"] = "999"
		case "hybrid":
			n, err := strconv.Atoi(changes["gpu_layers"])
			if err != nil || n < 1 || n >= 99 {
				return nil, errors.New("Для RAM + VRAM укажи от 1 до 98 слоёв GPU.")
			}
		}
		if placement != "cpu" {
			delete(next, "kv_offload")
		}
	}
	for _, o := range PrismOptions {
		v, ok := changes[o.Key]
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" {
			delete(next, o.Key)
			continue
		}
		if err := checkOption(o, v); err != nil {
			return nil, err
		}
		next[o.Key] = v
	}
	if v, ok := changes["kv_offload"]; ok && next["gpu_layers"] != "0" {
		if v != "on" && v != "off" {
			return nil, errors.New("KV-кеш на GPU: on или off.")
		}
		if v == "off" {
			next["kv_offload"] = "off"
		} else {
			delete(next, "kv_offload")
		}
	}
	batch, ubatch := 512, 128
	if v, ok := next["batch"]; ok {
		batch, _ = strconv.Atoi(v)
	}
	if v, ok := next["ubatch"]; ok {
		ubatch, _ = strconv.Atoi(v)
	}
	if ubatch > batch {
		return nil, errors.New("ubatch не может быть больше batch.")
	}
	return next, nil
}

// View is the settings form the panel shows: every field present, defaults filled in.
func View(engine string, s map[string]string) map[string]any {
	v := map[string]any{"context": s["context"], "placement": "auto"}
	if engine == Ollama {
		if v["context"] == "" {
			v["context"] = "(runtime default)"
		}
		if p := s["placement"]; p != "" {
			v["placement"] = p
		}
		keep := s["keep_alive"]
		if keep == "" {
			keep = "5m"
		}
		v["keep_alive"] = keep
		v["idle_minutes"] = keepAliveMinutes(keep)
		return v
	}
	if v["context"] == "" {
		v["context"] = "(model default)"
	}
	get := func(key, def string) string {
		if x, ok := s[key]; ok {
			return x
		}
		return def
	}
	model := s["model"]
	v["model"] = model
	v["alias"] = get("alias", modelStem(model))
	gpu := get("gpu_layers", "99")
	v["gpu_layers"] = gpu
	if n, _ := strconv.Atoi(gpu); n == 0 {
		v["placement"] = "cpu"
	} else if n >= 99 {
		v["placement"] = "gpu"
	} else {
		v["placement"] = "hybrid"
	}
	v["cache_key"] = get("cache_key", "f16")
	v["cache_value"] = get("cache_value", "f16")
	v["flash_attention"] = get("flash_attention", "auto")
	v["batch"] = get("batch", "512")
	v["ubatch"] = get("ubatch", "128")
	v["n_cpu_moe"] = get("n_cpu_moe", "0")
	if idle, ok := s["idle_minutes"]; ok {
		n, _ := strconv.Atoi(idle)
		v["idle_minutes"] = n
	} else {
		v["idle_minutes"] = nil
	}
	for _, o := range PrismOptions {
		v[o.Key] = s[o.Key]
	}
	v["kv_offload"] = get("kv_offload", "on")
	return v
}

func keepAliveMinutes(keep string) any {
	if strings.HasSuffix(keep, "m") {
		if n, err := strconv.Atoi(strings.TrimSuffix(keep, "m")); err == nil {
			return n
		}
	}
	if n, err := strconv.ParseFloat(strings.TrimSuffix(keep, "s"), 64); err == nil && n >= 0 {
		return math.Round(n/60*100) / 100
	}
	return nil
}

// modelStem is the file name without directory and .gguf, for both path separators.
func modelStem(path string) string {
	if i := strings.LastIndexAny(path, `\/`); i >= 0 {
		path = path[i+1:]
	}
	return strings.TrimSuffix(path, ".gguf")
}
