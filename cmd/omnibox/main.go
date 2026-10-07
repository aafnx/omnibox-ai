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
	fmt.Fprintln(w, `OmniBox AI (Go)
  omnibox version
  omnibox serve --root <каталог установки> --preview [--port <порт>]`)
}

func serve(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	root := fs.String("root", defaultRoot(), "каталог установки")
	preview := fs.Bool("preview", false, "только чтение рядом с PowerShell-версией")
	port := fs.Int("port", 0, "порт (в предпросмотре по умолчанию — порт панели + 1)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*preview {
		// Control, /v1 and the service mode arrive in later stages; until then only the preview runs.
		return errors.New("Пока доступен только режим предпросмотра: omnibox serve --preview.")
	}
	panelCfg, err := config.LoadPanel(filepath.Join(*root, "panel.json"))
	if err != nil {
		return fmt.Errorf("panel.json: %w", err)
	}
	gateway, err := config.LoadGateway(filepath.Join(*root, "gateway.json"))
	if err != nil {
		return fmt.Errorf("gateway.json: %w", err)
	}
	listen := *port
	if listen == 0 {
		listen = panelCfg.Port + 1
	}
	if listen == panelCfg.Port || (gateway.Enabled && listen == gateway.Port) {
		return errors.New("Предпросмотр должен работать на свободном порту, отличном от порта панели и API.")
	}
	handler, err := panel.New(panel.Options{Panel: panelCfg, Gateway: gateway, Preview: true})
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(listen))
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "OmniBox AI %s: предпросмотр панели на порту %d (только чтение)\n", omnibox.Version, listen)
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

func defaultRoot() string {
	if v := os.Getenv("OMNIBOX_AI_ROOT"); v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}
