// Package cli is the ai command: a client of the OmniBox AI service through its local socket.
// It never edits files itself, so it cannot race the panel.
package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	omnibox "github.com/aafnx/omnibox-ai"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/local"
)

// Service is what the command needs from the service.
type Service interface {
	Do(method, path string, body any, out any) error
}

// Wait is the pause between operation checks.
var Wait = 500 * time.Millisecond

const usage = `ai — управление OmniBox AI на этом компьютере (от имени администратора)

  ai status                  движки, модели и адрес панели
  ai start <движок>          запустить prismml или ollama (другой остановится)
  ai stop [<движок>]         остановить движок, без имени — все
  ai restart <движок>        перезапустить
  ai logs [<движок>]         последние строки журналов
  ai password reset          новый пароль панели (все входы завершаются)
  ai key new                 один новый API key вместо всех прежних (они сразу перестают действовать)
  ai update --check          проверить, есть ли новая версия
  ai update --list           список версий: установленная отмечена *
  ai update [версия]         скачать выпуск, проверить SHA256 и обновить программу;
                             без версии — последняя, более старая версия — откат
                             (нужен gh или GitHub token)
  ai on [движок]             включить OmniBox: службу панели и API, затем модель (движок
                             из аргумента или выбранный «при старте»)
  ai off                     выключить всё: движки, панель и API
  ai autostart [on|off]      запуск OmniBox вместе с системой; без аргумента — текущее
  ai autostart model <prismml|ollama|off>   какая модель запускается при старте системы
  ai uninstall               удалить OmniBox полностью: службы, программу, данные и модели
  ai version                 версия`

// Run executes one command and returns the process exit code.
func Run(args []string, svc Service, out, errw io.Writer) int {
	if err := run(args, svc, out); err != nil {
		fmt.Fprintln(errw, err)
		return 1
	}
	return 0
}

func run(args []string, svc Service, out io.Writer) error {
	if len(args) == 0 {
		fmt.Fprintln(out, usage)
		return nil
	}
	engineArg := func() (string, error) {
		if len(args) < 2 {
			return "", fmt.Errorf("Укажите движок: prismml или ollama.")
		}
		name := strings.ToLower(args[1])
		if name != config.PrismML && name != config.Ollama {
			return "", fmt.Errorf("Неизвестный движок %q: prismml или ollama.", args[1])
		}
		return name, nil
	}
	switch args[0] {
	case "help", "-h", "--help":
		fmt.Fprintln(out, usage)
	case "version":
		fmt.Fprintf(out, "OmniBox AI %s\n", omnibox.Version)
	case "status":
		return status(svc, out)
	case "start", "restart":
		name, err := engineArg()
		if err != nil {
			return err
		}
		return operation(svc, out, map[string]any{"action": args[0], "backend": name})
	case "stop":
		if len(args) == 1 {
			return operation(svc, out, map[string]any{"action": "stopall"})
		}
		name, err := engineArg()
		if err != nil {
			return err
		}
		return operation(svc, out, map[string]any{"action": "stop", "backend": name})
	case "logs":
		path := "/logs"
		if len(args) > 1 {
			name, err := engineArg()
			if err != nil {
				return err
			}
			path += "?engine=" + name
		}
		var logs []map[string]string
		if err := svc.Do("GET", path, nil, &logs); err != nil {
			return err
		}
		for _, l := range logs {
			fmt.Fprintln(out, l["text"])
		}
	case "password":
		if len(args) != 2 || args[1] != "reset" {
			return fmt.Errorf("Используйте: ai password reset")
		}
		var r struct{ Password string }
		if err := svc.Do("POST", "/password-reset", nil, &r); err != nil {
			return err
		}
		fmt.Fprintf(out, "Новый пароль панели: %s\nОн показан один раз и нигде не сохранён; все входы в панель завершены.\n", r.Password)
	case "key":
		if len(args) != 2 || args[1] != "new" {
			return fmt.Errorf("Используйте: ai key new")
		}
		var r struct{ Key string }
		if err := svc.Do("POST", "/api-key", nil, &r); err != nil {
			return err
		}
		fmt.Fprintf(out, "Новый API key: %s\nВсе прежние ключи больше не действуют. Ключ показан один раз; сохраните его в менеджере паролей.\nОтдельные ключи для клиентов создаются в панели: Подключение → API-ключи клиентов.\n", r.Key)
	default:
		return fmt.Errorf("Неизвестная команда %q. Справка: ai help", args[0])
	}
	return nil
}

type engineRow struct {
	Name          string `json:"name"`
	Label         string `json:"label"`
	Service       string `json:"service"`
	API           string `json:"api"`
	LoadedModel   string `json:"loaded_model"`
	SelectedModel string `json:"selected_model"`
	ActiveContext *int   `json:"active_context"`
	Pending       bool   `json:"pending"`
	Restarts      int    `json:"restarts"`
	GaveUp        string `json:"gave_up"`
	Errors        []string
}

func status(svc Service, out io.Writer) error {
	var d struct {
		Version  string      `json:"version"`
		Computer string      `json:"computer"`
		Panel    string      `json:"panel"`
		API      string      `json:"api"`
		Engines  []engineRow `json:"backends"`
	}
	if err := svc.Do("GET", "/status", nil, &d); err != nil {
		return err
	}
	fmt.Fprintf(out, "OmniBox AI %s · %s\n", d.Version, d.Computer)
	if d.Panel != "" {
		fmt.Fprintf(out, "Панель: %s\n", d.Panel)
	}
	if d.API != "" {
		fmt.Fprintf(out, "API для клиентов: %s\n", d.API)
	}
	if len(d.Engines) == 0 {
		fmt.Fprintln(out, "Движки не установлены. Установите их в панели: Обновления → ИИ-движки.")
	}
	for _, e := range d.Engines {
		line := fmt.Sprintf("%s: %s", e.Label, serviceText(e.Service))
		if e.Service == "Running" {
			line += " · API " + map[bool]string{true: "готов", false: "не отвечает"}[e.API == "Ready"]
			if e.LoadedModel != "" && e.LoadedModel != "-" {
				line += " · модель " + e.LoadedModel
			}
			if e.ActiveContext != nil {
				line += fmt.Sprintf(" · контекст %d", *e.ActiveContext)
			}
		} else if e.SelectedModel != "" {
			line += " · выбрана модель " + e.SelectedModel
		}
		if e.Pending {
			line += " · новые настройки применятся при запуске"
		}
		if e.Restarts > 0 {
			line += fmt.Sprintf(" · автоперезапусков: %d", e.Restarts)
		}
		if e.GaveUp != "" {
			line += " · автоперезапуск прекращён: " + e.GaveUp
		}
		fmt.Fprintln(out, line)
		for _, problem := range e.Errors {
			fmt.Fprintln(out, "  "+problem)
		}
	}
	return nil
}

func serviceText(state string) string {
	switch state {
	case "Running":
		return "работает"
	case "Stopped":
		return "остановлен"
	case "StartPending":
		return "запускается"
	case "StopPending":
		return "останавливается"
	case "Missing":
		return "служба не найдена"
	}
	return state
}

// operation starts a panel operation and waits for its result, as the panel does.
func operation(svc Service, out io.Writer, body map[string]any) error {
	var started struct{ ID string }
	if err := svc.Do("POST", "/actions", body, &started); err != nil {
		return err
	}
	for {
		var op struct {
			State   string `json:"state"`
			Message string `json:"message"`
		}
		if err := svc.Do("GET", "/operations/"+started.ID, nil, &op); err != nil {
			return err
		}
		switch op.State {
		case "done":
			fmt.Fprintln(out, op.Message)
			return nil
		case "failed":
			return fmt.Errorf("%s", op.Message)
		}
		time.Sleep(Wait)
	}
}

var _ Service = (*local.Client)(nil)
