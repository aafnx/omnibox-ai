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
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	omnibox "github.com/aafnx/omnibox-ai"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/panel"
	"github.com/aafnx/omnibox-ai/internal/platform"
	"github.com/aafnx/omnibox-ai/internal/status"
	"github.com/aafnx/omnibox-ai/internal/telemetry"
)

func main() {
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
  omnibox serve [--data <каталог данных>]`)
}

func serve(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	data := fs.String("data", platform.DataDir(), "каталог данных (config.json, журналы, модели)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(filepath.Join(*data, "config.json"))
	if err != nil {
		return err
	}
	current := func() *config.Config { return cfg }
	services := platform.NewServices()
	builder := &status.Builder{Config: current, Services: services, DataDir: *data}
	sampler := &telemetry.Sampler{}
	handler, err := panel.New(panel.Options{
		Config: current,
		Status: func(ctx context.Context, gpu bool) any { return builder.Build(ctx, gpu) },
		Telemetry: func(ctx context.Context, once bool) any {
			return telemetryView(ctx, current(), services, sampler, once)
		},
		Logs: func(engine string) any { return status.Logs(*data, current(), engine) },
	})
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(cfg.Panel.Port))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "OmniBox AI %s: панель на порту %d\n", omnibox.Version, cfg.Panel.Port)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
