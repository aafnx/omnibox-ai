//go:build !windows

package host

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// Serve runs fn until systemd (or the console) asks to stop.
func Serve(_ string, fn func(ctx context.Context, running func()) error) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return fn(ctx, func() {})
}
