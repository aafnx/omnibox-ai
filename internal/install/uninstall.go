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

	"github.com/aafnx/omnibox-ai/internal/catalog"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/control"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Uninstall removes OmniBox AI from this computer without a trace: the services (with the update
// service an interrupted panel update leaves), the firewall rule, the ai command and its PATH
// entry, the program and data directories, the models and leftover update downloads. One
// question comes first and names what goes; --yes skips it.
func (i *Installer) Uninstall(ctx context.Context) error {
	i.in = bufio.NewReader(i.In)
	var outside []string // model directories chosen outside the data directory
	for _, dir := range i.modelDirs() {
		if !i.inData(dir) {
			outside = append(outside, dir)
		}
	}
	i.say("Будут удалены службы, правило Firewall, команда ai, программа (%s) и данные: настройки, пароль, API key, журналы, модели (%s).", i.ProgramDir, i.DataDir)
	if len(outside) > 0 {
		i.say("Из каталогов моделей %s удаляются только модели OmniBox; другие файлы там останутся.", strings.Join(outside, ", "))
	}
	if !i.Yes && !i.yes("Удалить OmniBox AI полностью?", false) {
		i.say("Отменено, ничего не изменено.")
		return nil
	}

	var problems []string
	note := func(what string, err error) {
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", what, err))
		}
	}
	// Engines first: the panel would otherwise see them stop as a failure.
	for _, name := range []string{config.ServiceName(config.PrismML), config.ServiceName(config.Ollama), config.PanelService, config.UpdateService} {
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

	// Deepest first, so an emptied Ollama directory inside another models directory goes before it.
	slices.SortFunc(outside, func(a, b string) int { return len(b) - len(a) })
	var foreign []string
	for _, dir := range outside {
		kept, err := removeModels(dir)
		note("модели "+dir, err)
		foreign = append(foreign, kept...)
	}
	note("каталог данных", os.RemoveAll(i.DataDir))
	if i.TempDir != "" {
		// "ai update" downloads into the system temporary directory; a cut-off run leaves it there.
		left, _ := filepath.Glob(filepath.Join(i.TempDir, "omnibox-update-*"))
		for _, dir := range left {
			note("временные файлы", os.RemoveAll(dir))
		}
	}

	i.say("")
	i.say("OmniBox AI удалён полностью: службы, правило Firewall, команда ai, программа, данные и модели.")
	if len(foreign) > 0 {
		i.say("Оставлены файлы, которые OmniBox не создавал: %s", strings.Join(foreign, ", "))
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

func (i *Installer) inData(dir string) bool {
	rel, err := filepath.Rel(i.DataDir, dir)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

// removeModels empties a models directory of what OmniBox puts there (GGUF files, partial
// downloads, the Ollama store) and removes it once nothing else is left: it may be a shared
// folder the owner also keeps other files in. It returns the paths it left.
func removeModels(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var kept []string
	var failed error
	for _, e := range entries {
		name := e.Name()
		ours := catalog.PartPattern.MatchString(name) ||
			(!e.IsDir() && strings.EqualFold(filepath.Ext(name), ".gguf")) ||
			(e.IsDir() && (name == "blobs" || name == "manifests"))
		if !ours {
			kept = append(kept, filepath.Join(dir, name))
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			failed = err
		}
	}
	if len(kept) == 0 && failed == nil {
		failed = os.Remove(dir)
	}
	return kept, failed
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
