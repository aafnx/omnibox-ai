package install

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/control"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Uninstall removes the program from this computer: the services, the firewall rule, the ai
// command and its PATH entry, and the program directory. Settings, logs and models stay unless
// the owner asks; models are asked about separately, since they take the longest to get back.
func (i *Installer) Uninstall(ctx context.Context) error {
	i.in = bufio.NewReader(i.In)
	if !i.Yes && !i.yes("Удалить OmniBox AI с этого компьютера?", false) {
		i.say("Отменено, ничего не изменено.")
		return nil
	}
	removeData := !i.Yes && i.yes("Удалить и данные: настройки, пароль, API key, журналы?", false)
	removeModels := removeData && i.yes("Удалить и скачанные модели?", false)
	models := i.modelDirs()

	var problems []string
	note := func(what string, err error) {
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", what, err))
		}
	}
	// Engines first: the panel would otherwise see them stop as a failure.
	for _, name := range []string{config.ServiceName(config.PrismML), config.ServiceName(config.Ollama), config.PanelService} {
		if s, err := i.Services.Query(name); err == nil && s.State != platform.Missing {
			note("служба "+name, i.Services.Remove(name, control.StopTimeout))
		}
	}
	note("правило Firewall", i.RemoveRule(FirewallRule))
	shim, _ := i.shim()
	if err := os.Remove(shim); err != nil && !errors.Is(err, os.ErrNotExist) {
		note("команда ai", err)
	}
	if i.RemoveFromPath != nil && samePath(i.ShimDir, i.ProgramDir) {
		note("PATH", i.RemoveFromPath(i.ShimDir))
	}
	remove := i.RemoveProgram
	if remove == nil {
		remove = os.RemoveAll
	}
	note("каталог программы", remove(i.ProgramDir))

	switch {
	case removeModels:
		for _, dir := range models {
			note("модели "+dir, os.RemoveAll(dir))
		}
		note("каталог данных", os.RemoveAll(i.DataDir))
	case removeData:
		note("каталог данных", i.removeDataKeeping(models))
	}

	i.say("")
	i.say("OmniBox AI удалён: службы, правило Firewall, команда ai и каталог программы %s.", i.ProgramDir)
	switch {
	case removeModels:
		i.say("Данные и модели удалены.")
	case removeData:
		i.say("Данные удалены; модели оставлены: %s", strings.Join(models, ", "))
	default:
		i.say("Данные и модели оставлены в %s: повторная установка их подхватит.", i.DataDir)
	}
	if len(problems) > 0 {
		i.say("Не всё удалось удалить:")
		for _, p := range problems {
			i.say("  %s", p)
		}
		return errors.New("Удаление завершено с ошибками.")
	}
	return nil
}

// modelDirs are the engines' model directories from config.json, and the default one.
func (i *Installer) modelDirs() []string {
	dirs := []string{filepath.Join(i.DataDir, "models")}
	if cfg, err := config.Load(i.configPath()); err == nil {
		for _, name := range config.EngineNames {
			if e, ok := cfg.Engines[name]; ok && e.ModelsDirectory != "" && !slices.ContainsFunc(dirs, func(d string) bool { return samePath(d, e.ModelsDirectory) }) {
				dirs = append(dirs, e.ModelsDirectory)
			}
		}
	}
	var out []string
	for _, d := range dirs {
		if _, err := os.Stat(d); err == nil {
			out = append(out, d)
		}
	}
	return out
}

// removeDataKeeping empties the data directory except the model directories inside it.
func (i *Installer) removeDataKeeping(models []string) error {
	var keep []string
	for _, m := range models {
		if rel, err := filepath.Rel(i.DataDir, m); err == nil && filepath.IsLocal(rel) {
			keep = append(keep, strings.Split(filepath.ToSlash(rel), "/")[0])
		}
	}
	entries, err := os.ReadDir(i.DataDir)
	if err != nil {
		return err
	}
	var failed error
	for _, e := range entries {
		if slices.ContainsFunc(keep, func(k string) bool { return strings.EqualFold(k, e.Name()) }) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(i.DataDir, e.Name())); err != nil {
			failed = err
		}
	}
	return failed
}
