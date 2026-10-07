//go:build windows

package host

import (
	"context"
	"os"
	"os/signal"

	"golang.org/x/sys/windows/svc"
)

// Serve runs fn as the Windows service name, or in the console when started by hand.
func Serve(name string, fn func(ctx context.Context, running func()) error) error {
	inService, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if !inService {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		return fn(ctx, func() {})
	}
	h := &handler{fn: fn}
	if err := svc.Run(name, h); err != nil {
		return err
	}
	return h.err
}

type handler struct {
	fn  func(ctx context.Context, running func()) error
	err error
}

func (h *handler) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending, WaitHint: 30000}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	accepts := svc.AcceptStop | svc.AcceptShutdown
	go func() {
		done <- h.fn(ctx, func() { changes <- svc.Status{State: svc.Running, Accepts: accepts} })
	}()
	for {
		select {
		case h.err = <-done:
			if h.err != nil && ctx.Err() == nil {
				// The engine failed by itself: report it so the SCM shows the service as failed.
				return false, 1066
			}
			return false, 0
		case r := <-requests:
			switch r.Cmd {
			case svc.Interrogate:
				changes <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending, WaitHint: 30000}
				cancel()
			}
		}
	}
}
