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
	handler, err := panel.New(panel.Options{Config: func() *config.Config { return cfg }})
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
