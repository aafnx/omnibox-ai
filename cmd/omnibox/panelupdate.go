package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	omnibox "github.com/aafnx/omnibox-ai"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/control"
	"github.com/aafnx/omnibox-ai/internal/enginemgr"
	"github.com/aafnx/omnibox-ai/internal/host"
	"github.com/aafnx/omnibox-ai/internal/install"
	"github.com/aafnx/omnibox-ai/internal/platform"
	"github.com/aafnx/omnibox-ai/internal/selfupdate"
)

const updateService = config.UpdateService

// updateResult is what the panel shows after it comes back from an update.
type updateResult struct {
	Version string    `json:"version"`
	From    string    `json:"from"`
	OK      bool      `json:"ok"`
	Message string    `json:"message"`
	Time    time.Time `json:"time"`
}

func updateResultPath(data string) string { return filepath.Join(data, "run", "update-result.json") }
func updatesDir(data string) string       { return filepath.Join(data, "updates") }

// panelUpdate downloads and checks a release for the panel's "update" button and starts the
// temporary installer service. The token is used for this download only.
type panelUpdate struct {
	Services   platform.Services
	Data       string
	Program    string
	NewUpdater func(token string) *selfupdate.Updater
	Current    string
}

func (p *panelUpdate) Start(ctx context.Context, token, version string) (string, error) {
	if token == "" {
		return "", errors.New("Нужен GitHub token с правом чтения репозитория aafnx/omnibox-ai; он используется для этого скачивания и не сохраняется.")
	}
	if s, err := p.Services.Query(updateService); err == nil && s.State == platform.Running {
		return "", errors.New("Обновление уже выполняется.")
	}
	u := p.NewUpdater(token)
	release, err := u.Find(ctx, version)
	if err != nil {
		return "", err
	}
	if version == "" && release.Version() == p.Current {
		return fmt.Sprintf("Установлена последняя версия %s.", p.Current), nil
	}
	dir := filepath.Join(updatesDir(p.Data), release.Version())
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	exe, err := u.Fetch(ctx, release, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if s, err := p.Services.Query(updateService); err == nil && s.State != platform.Missing {
		if err := p.Services.Remove(updateService, control.StopTimeout); err != nil {
			return "", fmt.Errorf("прежняя служба обновления: %v", err)
		}
	}
	spec := platform.ServiceSpec{Name: updateService, DisplayName: "OmniBox AI: обновление",
		Description: "Временная служба: устанавливает новую версию OmniBox AI и удаляется после.",
		Executable:  exe, Args: []string{"update-service", "--data", p.Data, "--program", p.Program}}
	if err := p.Services.Install(spec); err != nil {
		return "", err
	}
	_ = os.Remove(updateResultPath(p.Data))
	if err := p.Services.Start(updateService); err != nil {
		_ = p.Services.Remove(updateService, control.StopTimeout)
		return "", err
	}
	return fmt.Sprintf("Выпуск %s скачан и проверен по SHA256. Установка идёт во временной службе: панель перезапустится примерно через минуту — затем обновите страницу. При сбое вернётся %s.", release.Version(), p.Current), nil
}

// cleanupUpdate removes the temporary service and the downloaded files once the update ended.
// It runs at the start of the panel only when an update service exists, and waits for it at most
// 15 minutes: the update restarts this panel before it finishes.
func cleanupUpdate(services platform.Services, data string) {
	s, err := services.Query(updateService)
	if err != nil || s.State == platform.Missing {
		_ = os.RemoveAll(updatesDir(data))
		return
	}
	go func() {
		for deadline := time.Now().Add(15 * time.Minute); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
			if s, err := services.Query(updateService); err != nil || s.State == platform.Stopped || s.State == platform.Missing {
				break
			}
		}
		if s, err := services.Query(updateService); err == nil && s.State == platform.Stopped {
			if services.Remove(updateService, control.StopTimeout) == nil {
				_ = os.RemoveAll(updatesDir(data))
			}
		}
	}()
}

// lastUpdate is the result of the last update from the panel, if any.
func lastUpdate(data string) *updateResult {
	b, err := platform.ReadShared(updateResultPath(data))
	if err != nil {
		return nil
	}
	var r updateResult
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// runUpdateService is "omnibox update-service": the temporary service runs the installer of the
// new version, which updates with rollback, and records the result for the panel.
func runUpdateService(args []string) error {
	fs := flag.NewFlagSet("update-service", flag.ContinueOnError)
	data := fs.String("data", platform.DataDir(), "каталог данных")
	program := fs.String("program", platform.ProgramDir(), "каталог программы")
	if err := fs.Parse(args); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return host.Serve(updateService, func(_ context.Context, running func()) error {
		running()
		i := &install.Installer{ProgramDir: *program, DataDir: *data, Version: omnibox.Version, Source: exe,
			Services: platform.NewServices(), Manager: &enginemgr.Manager{DataDir: *data}, Yes: true,
			AllowPort: platform.AllowPort, RemoveRule: platform.RemovePortRule, Health: install.PanelHealth,
			Restrict: platform.RestrictToAdmins, AddToPath: platform.AddToPath, RemoveFromPath: platform.RemoveFromPath, ShimDir: shimDir(*program)}
		return performUpdate(i)
	})
}

// performUpdate runs the installer of the new version (an update: settings stay, a failure rolls
// back) with its output in logs/update.log, and records the result for the panel.
func performUpdate(i *install.Installer) error {
	from := ""
	if old, err := i.Services.Executable(config.PanelService); err == nil {
		from = filepath.Base(filepath.Dir(old))
	}
	logPath := filepath.Join(i.DataDir, "logs", "update.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	log, err := os.Create(logPath)
	if err != nil {
		return err
	}
	defer log.Close()
	i.In, i.Out = strings.NewReader(""), log
	runErr := i.Run(context.Background())
	data := i.DataDir
	result := updateResult{Version: omnibox.Version, From: from, OK: runErr == nil, Time: time.Now().UTC(),
		Message: fmt.Sprintf("Обновлено до %s.", omnibox.Version)}
	if runErr != nil {
		result.Message = runErr.Error()
	}
	b, _ := json.Marshal(result)
	_ = os.MkdirAll(filepath.Dir(updateResultPath(data)), 0o755)
	return platform.WriteAtomic(updateResultPath(data), b, 0o644)
}
