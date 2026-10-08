// Command omnibox is the OmniBox AI service, engine host and CLI in one binary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"time"

	omnibox "github.com/aafnx/omnibox-ai"
	"github.com/aafnx/omnibox-ai/internal/catalog"
	"github.com/aafnx/omnibox-ai/internal/cli"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/control"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/enginemgr"
	"github.com/aafnx/omnibox-ai/internal/gateway"
	"github.com/aafnx/omnibox-ai/internal/host"
	"github.com/aafnx/omnibox-ai/internal/install"
	"github.com/aafnx/omnibox-ai/internal/local"
	"github.com/aafnx/omnibox-ai/internal/panel"
	"github.com/aafnx/omnibox-ai/internal/platform"
	"github.com/aafnx/omnibox-ai/internal/selfupdate"
	"github.com/aafnx/omnibox-ai/internal/status"
	"github.com/aafnx/omnibox-ai/internal/storage"
	"github.com/aafnx/omnibox-ai/internal/telemetry"
)

func main() {
	platform.ConsoleUTF8()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintf(stdout, "OmniBox AI %s\n", omnibox.Version)
		return 0
	case "serve":
		if err := serve(args[1:], stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "ai":
		// The update runs here, as the administrator who typed it: it replaces the service.
		if len(args) > 1 && args[1] == "update" {
			return runUpdate(args[2:], defaultUpdater(), stdout, stderr)
		}
		if len(args) > 1 && (args[1] == "on" || args[1] == "off" || args[1] == "autostart") {
			if err := runPower(args[1], args[2:], stdout); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return 0
		}
		if len(args) > 1 && args[1] == "uninstall" {
			if err := runUninstall(args[2:], os.Stdin, stdout); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			return 0
		}
		return cli.Run(args[1:], local.Dial(platform.DataDir()), stdout, stderr)
	case "install":
		if err := runInstall(args[1:], os.Stdin, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "update-service":
		if err := runUpdateService(args[1:]); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "engine":
		if err := runEngine(args[1:]); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	}
	fmt.Fprintf(stderr, "Неизвестная команда: %s\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `OmniBox AI
  omnibox version
  omnibox ai <команда>        управление на этом компьютере (ai help)
  omnibox serve [--data <каталог данных>]
  omnibox engine <prismml|ollama> [--data <каталог данных>]
  omnibox install [--yes] [--data <каталог данных>] [--program <каталог программы>]`)
}

// runInstall installs this binary, or updates an existing installation keeping its settings.
func runInstall(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "принять все ответы по умолчанию")
	data := fs.String("data", platform.DataDir(), "каталог данных")
	program := fs.String("program", platform.ProgramDir(), "каталог программы")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !platform.IsAdmin() {
		return errors.New("Установка требует прав администратора (Windows) или root (Linux).")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	i := &install.Installer{ProgramDir: *program, DataDir: *data, Version: omnibox.Version, Source: exe,
		Services: platform.NewServices(), Manager: &enginemgr.Manager{DataDir: *data}, In: in, Out: out, Yes: *yes,
		AllowPort: platform.AllowPort, RemoveRule: platform.RemovePortRule, Health: install.PanelHealth,
		Restrict: platform.RestrictToAdmins, AddToPath: platform.AddToPath, RemoveFromPath: platform.RemoveFromPath, ShimDir: shimDir(*program)}
	return i.Run(context.Background())
}

// runEngine is the engine service: it runs one engine with its process tree until stopped.
func runEngine(args []string) error {
	if len(args) == 0 || !slices.Contains(config.EngineNames, args[0]) {
		return errors.New("Укажите движок: prismml или ollama.")
	}
	name := args[0]
	fs := flag.NewFlagSet("engine", flag.ContinueOnError)
	data := fs.String("data", platform.DataDir(), "каталог данных")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	return host.Serve(config.ServiceName(name), func(ctx context.Context, running func()) error {
		h := &host.Host{DataDir: *data, Engine: name, Running: running}
		return h.Run(ctx)
	})
}

// serve is the panel service. On Windows it reports to the SCM like the engine services do;
// without that the SCM would stop it 30 seconds after the start.
func serve(args []string, stdout io.Writer) error {
	return host.Serve(config.PanelService, func(ctx context.Context, running func()) error {
		return serveContext(ctx, args, stdout, platform.NewServices(), running)
	})
}

func serveContext(ctx context.Context, args []string, stdout io.Writer, services platform.Services, running func()) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	data := fs.String("data", platform.DataDir(), "каталог данных (config.json, журналы, модели)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := config.Open(filepath.Join(*data, "config.json"))
	if err != nil {
		return err
	}
	cfg := store.Get()
	current := store.Get
	// Housekeeping runs once per start, never on a timer.
	storage.Cleanup(*data, cfg, time.Now())
	api := gateway.New(&gateway.Gateway{Config: current, Services: services, Dir: filepath.Join(*data, "logs", "requests")})
	builder := &status.Builder{Config: current, Services: services, DataDir: *data, Activity: api}
	sampler := &telemetry.Sampler{}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	manager := &enginemgr.Manager{DataDir: *data}
	hub := &catalog.Hub{}
	controller := &control.Controller{Store: store, Services: services, Manager: manager, DataDir: *data, Executable: exe, Gate: api}
	updates := &panelUpdate{Services: services, Data: *data, Program: programDirOf(exe), Current: omnibox.Version,
		NewUpdater: func(token string) *selfupdate.Updater { return &selfupdate.Updater{Token: token} }}
	controller.SelfUpdate = updates.Start
	controller.Sleep = platform.Sleep
	controller.Test = func(ctx context.Context) (string, error) {
		r, err := api.Test(ctx)
		if err != nil {
			return "", err
		}
		return r.String(), nil
	}
	cleanupUpdate(services, *data)
	operations := &control.Operations{Run: controller.Do, Gate: api}
	handler, err := panel.New(panel.Options{
		Config:       current,
		LogPath:      filepath.Join(*data, "logs", "panel.log"),
		Estimate:     estimator{Config: current, GPUs: telemetry.GPUs, RAM: telemetry.RAMTotal}.Estimate,
		SessionsPath: filepath.Join(*data, "run", "sessions.bin"),
		Status: func(ctx context.Context, gpu bool) any {
			d := builder.Build(ctx, gpu)
			d.Operations = operations.List()
			if r := lastUpdate(*data); r != nil {
				d.LastUpdate = r
			}
			d.Network = platform.Adapters()
			system := map[string]any{"autostart": false, "start_engine": current().Panel.StartEngine}
			if s, err := services.Query(config.PanelService); err == nil {
				system["autostart"] = s.StartType == "Automatic"
			}
			d.System = system
			return d
		},
		Action: func(body map[string]any) (any, error) {
			if body["action"] == "cancel" {
				id, _ := body["id"].(string)
				if len(body) != 2 || id == "" {
					return nil, errors.New("Нужен id операции.")
				}
				return map[string]any{"ok": true}, operations.Cancel(id)
			}
			r, err := control.ParseRequest(body)
			if err != nil {
				return nil, err
			}
			id, err := operations.Start(r)
			if err != nil {
				return nil, err
			}
			return map[string]any{"id": id}, nil
		},
		Telemetry: func(ctx context.Context, once bool) any {
			observeSlots(ctx, current(), api)
			return telemetryView(ctx, current(), services, sampler, once)
		},
		Engines: func() any { return controller.Engines() },
		Releases: func(ctx context.Context, engine string) (any, error) {
			releases, err := manager.Releases(ctx, engine)
			if len(releases) > 20 {
				releases = releases[:20]
			}
			return releases, err
		},
		Catalog: func(ctx context.Context, query, repo string) (any, error) {
			// Hugging Face is asked only when the owner searches.
			if repo != "" {
				files, err := hub.Files(ctx, repo)
				return map[string]any{"repository": repo, "files": files, "url": "https://huggingface.co/" + repo}, err
			}
			models, err := hub.Search(ctx, query)
			return map[string]any{"models": models, "source": "Hugging Face", "query": query}, err
		},
		Gateway: func() any { return gateway.SettingsView(current(), api.KeyUsage()) },
		GatewayAction: func(body map[string]any) (any, error) {
			key, err := gateway.UpdateSettings(store, body)
			if err != nil {
				return nil, err
			}
			// A new key is shown once and never stored.
			return map[string]any{"gateway": gateway.SettingsView(current(), api.KeyUsage()), "key": key}, nil
		},
		SetPassword: func(p config.Password) error {
			return store.Update(func(c *config.Config) error { c.Panel.Password = p; return nil })
		},
		Storage: func() any { return storage.GetUsage(*data, current()) },
		StorageAction: func(body map[string]any) (any, error) {
			var result *storage.Result
			switch body["action"] {
			case "save":
				if err := storage.Save(*data, store, body); err != nil {
					return nil, err
				}
			case "cleanup":
				r := storage.Cleanup(*data, current(), time.Now())
				result = &r
			default:
				return nil, errors.New("Неизвестное действие с журналами.")
			}
			u := storage.GetUsage(*data, current())
			u.Cleanup = result
			return u, nil
		},
		API:            api,
		Activity:       func() any { return api.Snapshot() },
		RequestContent: api.FindContent,
		SearchContent:  func(q, session string) any { return api.SearchContent(q, session) },
		ExportSession:  api.ExportSession,
		Logs:           func(engine string) any { return status.Logs(*data, current(), engine) },
	})
	if err != nil {
		return err
	}
	// The ai command reaches the service through a local socket only administrators can open.
	localLn, err := local.Listen(*data)
	if err != nil {
		return err
	}
	localServer := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: local.Handler(local.Options{
		Status: func(ctx context.Context) any {
			c := current()
			d := builder.Build(ctx, false)
			return struct {
				status.Dashboard
				Panel string `json:"panel"`
				API   string `json:"api"`
			}{d, "http://" + c.Panel.Hosts[0] + ":" + strconv.Itoa(c.Panel.Port), c.API.PublicURL}
		},
		Logs: func(engine string) any { return status.Logs(*data, current(), engine) },
		Action: func(body map[string]any) (string, error) {
			r, err := control.ParseRequest(body)
			if err != nil {
				return "", err
			}
			return operations.Start(r)
		},
		Operation: func(id string) (any, bool) { return operations.Get(id) },
		ResetPassword: func() (string, error) {
			password := panel.GeneratePassword()
			hash, err := panel.NewPassword(password)
			if err != nil {
				return "", err
			}
			if err := store.Update(func(c *config.Config) error { c.Panel.Password = hash; return nil }); err != nil {
				return "", err
			}
			handler.EndSessions()
			return password, nil
		},
		NewKey: func() (string, error) { return gateway.UpdateSettings(store, map[string]any{"action": "generate"}) },
	})}
	go func() { _ = localServer.Serve(localLn) }()
	defer localServer.Close()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(cfg.Panel.Port))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "OmniBox AI %s: панель на порту %d\n", omnibox.Version, cfg.Panel.Port)
	running()
	bootStart(*data, current(), platform.BootTime(), func(engine string) {
		_, _ = operations.Start(control.Request{Action: "start", Engine: engine})
	})
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// telemetryView polls resources only while an engine works; with every engine stopped the
// panel gets a sample only when the user asked for one (login, returning to the tab, refresh).
func telemetryView(ctx context.Context, cfg *config.Config, services platform.Services, sampler *telemetry.Sampler, once bool) map[string]any {
	working := false
	for name := range cfg.Engines {
		if info, err := services.Query(config.ServiceName(name)); err == nil && (info.State == platform.Running || info.State == platform.StartPending) {
			working = true
		}
	}
	if !working && !once {
		return map[string]any{"enabled": false, "timestamp": time.Now().UTC(), "reason": "Серверы остановлены; опрос ресурсов выключен."}
	}
	return map[string]any{"enabled": true, "sample": sampler.Get(ctx), "collecting": false}
}

// observeSlots reads llama-server's decoding progress while the panel watches a generation;
// nothing is asked when no request runs.
func observeSlots(ctx context.Context, cfg *config.Config, api *gateway.Gateway) {
	e, ok := cfg.Engines[config.PrismML]
	id := api.ActiveID(config.PrismML)
	if !ok || id == "" {
		return
	}
	var slots []struct {
		IsProcessing bool `json:"is_processing"`
		NCtx         int  `json:"n_ctx"`
		NextToken    *struct {
			NDecoded int64 `json:"n_decoded"`
		} `json:"next_token"`
	}
	if engine.NewClient(e).Get(ctx, "/slots", &slots) != nil {
		return
	}
	for _, slot := range slots {
		if slot.IsProcessing && slot.NextToken != nil {
			api.ObserveSlot(config.PrismML, id, slot.NextToken.NDecoded, slot.NCtx)
		}
	}
}

// runUninstall is "ai uninstall": it removes the services and the program; data and models stay
// unless the owner answers otherwise.
func runUninstall(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "не спрашивать: удалить программу, оставить данные и модели")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !platform.IsAdmin() {
		return errors.New("Удаление требует прав администратора (Windows) или root (Linux).")
	}
	program := platform.ProgramDir()
	i := &install.Installer{ProgramDir: program, DataDir: platform.DataDir(), Version: omnibox.Version,
		Services: platform.NewServices(), In: in, Out: out, Yes: *yes, RemoveRule: platform.RemovePortRule,
		ShimDir: shimDir(program), RemoveFromPath: platform.RemoveFromPath,
		RemoveProgram: func(dir string) error {
			if exe, err := os.Executable(); err == nil {
				if rel, err := filepath.Rel(dir, exe); err == nil && filepath.IsLocal(rel) {
					return platform.RemoveAfterExit(dir)
				}
			}
			return os.RemoveAll(dir)
		}}
	return i.Uninstall(context.Background())
}

// programDirOf is the program directory of an installed binary (<program>/bin/<version>/omnibox);
// a binary run from elsewhere (tests, a download) uses the default one.
func programDirOf(exe string) string {
	if bin := filepath.Dir(filepath.Dir(exe)); filepath.Base(bin) == "bin" {
		return filepath.Dir(bin)
	}
	return platform.ProgramDir()
}

// runPower is "ai on", "ai off" and "ai autostart": they work also while the service is stopped,
// so they talk to the operating system's services directly, as the administrator who typed them.
func runPower(command string, args []string, out io.Writer) error {
	if !platform.IsAdmin() {
		return errors.New("Нужны права администратора (Windows) или root (Linux).")
	}
	data := platform.DataDir()
	p := power{Services: platform.NewServices(), Service: local.Dial(data), Out: out, Wait: 500 * time.Millisecond, Timeout: 90 * time.Second,
		Config: func() (*config.Config, error) { return config.Load(filepath.Join(data, "config.json")) }}
	switch command {
	case "on":
		return p.On(args)
	case "off":
		return p.Off(args)
	}
	return p.Autostart(args)
}

// shimDir is where the ai command is placed.
func shimDir(program string) string {
	if runtime.GOOS == "windows" {
		return program
	}
	return "/usr/local/bin"
}
