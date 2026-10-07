package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aafnx/omnibox-ai/internal/control"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Undo is how to reverse one installation step. It is plain data, so the same undo runs after a
// failed step and, from the journal on disk, after an installation that was cut off (power loss,
// a closed window): the next run of the installer first puts things back.
type Undo struct {
	Op      string                `json:"op"`
	Path    string                `json:"path,omitempty"`
	Data    []byte                `json:"data,omitempty"`
	Service string                `json:"service,omitempty"`
	Spec    *platform.ServiceSpec `json:"spec,omitempty"`
}

// Undo operations.
const (
	undoRemove        = "remove"         // delete Path (a file or a directory)
	undoWrite         = "write"          // put Data back into Path
	undoServiceRemove = "service-remove" // delete Service
	undoServiceChange = "service-change" // point the service back at Spec
	undoServiceStop   = "service-stop"   // stop Service
	undoServiceStart  = "service-restart"
	undoRuleRemove    = "rule-remove" // delete the firewall rule
	undoPathRemove    = "path-remove" // take Path out of PATH
)

type journal struct {
	Version string `json:"version"`
	Mode    string `json:"mode"` // fresh or update
	Steps   []step `json:"steps"`
}

func (i *Installer) journalPath() string { return filepath.Join(i.ProgramDir, "install-journal.json") }

// record writes the journal after every step; without it a cut-off installation could not be
// undone.
func (i *Installer) record() error {
	data, err := json.Marshal(journal{Version: i.Version, Mode: i.mode, Steps: i.undo})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(i.ProgramDir, 0o755); err != nil {
		return err
	}
	return platform.WriteAtomic(i.journalPath(), data, 0o644)
}

func (i *Installer) apply(u Undo) error {
	switch u.Op {
	case undoRemove:
		if err := os.RemoveAll(u.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	case undoWrite:
		return os.WriteFile(u.Path, u.Data, 0o755)
	case undoServiceRemove:
		if s, err := i.Services.Query(u.Service); err == nil && s.State == platform.Missing {
			return nil
		}
		return i.Services.Remove(u.Service, control.StopTimeout)
	case undoServiceChange:
		if u.Spec == nil {
			return errors.New("нет описания службы")
		}
		return i.Services.Change(*u.Spec)
	case undoServiceStop:
		if s, err := i.Services.Query(u.Service); err == nil && s.State != platform.Running {
			return nil
		}
		return i.Services.Stop(u.Service, control.StopTimeout)
	case undoServiceStart:
		_ = i.Services.Stop(u.Service, control.StopTimeout)
		return i.Services.Start(u.Service)
	case undoRuleRemove:
		// A rule that was never added cannot be removed: nothing to undo.
		_ = i.RemoveRule(FirewallRule)
		return nil
	case undoPathRemove:
		if i.RemoveFromPath == nil {
			return nil
		}
		return i.RemoveFromPath(u.Path)
	}
	return fmt.Errorf("неизвестная операция отката %q", u.Op)
}

// undoAll reverses the recorded steps, newest first; the journal goes only when everything was
// undone, so a failed undo is tried again on the next run.
func (i *Installer) undoAll() []string {
	var failed []string
	for k := len(i.undo) - 1; k >= 0; k-- {
		if err := i.apply(i.undo[k].Undo); err != nil {
			failed = append(failed, i.undo[k].Name+": "+err.Error())
		}
	}
	if len(failed) == 0 {
		_ = os.Remove(i.journalPath())
		// Empty directories are what a cut-off new installation leaves; nothing else is removed.
		_ = os.Remove(filepath.Join(i.ProgramDir, "bin"))
		_ = os.Remove(i.ProgramDir)
	}
	i.undo = nil
	return failed
}

// recover undoes an installation that was cut off before it finished or rolled back.
func (i *Installer) recover() error {
	data, err := os.ReadFile(i.journalPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		return fmt.Errorf("журнал прерванной установки %s не прочитан: %v", i.journalPath(), err)
	}
	i.say("Найдена прерванная установка %s (%s). Возвращаю прежнее состояние…", j.Version, map[string]string{"fresh": "новая установка", "update": "обновление"}[j.Mode])
	i.undo = j.Steps
	if failed := i.undoAll(); len(failed) > 0 {
		return fmt.Errorf("Прерванную установку не удалось отменить полностью: %s. Повторите команду.", strings.Join(failed, "; "))
	}
	if j.Mode == "update" {
		i.say("Прежняя версия восстановлена и запущена.")
	} else {
		i.say("Следы прерванной установки удалены.")
	}
	return nil
}
