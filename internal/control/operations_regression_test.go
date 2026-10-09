package control

import (
	"context"
	"testing"
)

func TestNewControlActionsRespectGenerationGate(t *testing.T) {
	for _, action := range []string{"model-start", "poweroff"} {
		t.Run(action, func(t *testing.T) {
			g := &gate{busy: true}
			o := &Operations{Gate: g, Run: func(context.Context, Request) (string, error) {
				t.Error("a refused operation ran")
				return "", nil
			}}
			if _, err := o.Start(Request{Action: action}); err != ErrGenerating {
				t.Fatalf("generation did not refuse %s: %v", action, err)
			}
		})
	}
}

func TestPoweroffRefusesActiveWork(t *testing.T) {
	for _, action := range []string{"download", "engine-install", "self-update"} {
		t.Run(action, func(t *testing.T) {
			release := make(chan struct{})
			finished := make(chan struct{})
			o := &Operations{Run: func(context.Context, Request) (string, error) {
				defer close(finished)
				<-release
				return "ok", nil
			}}
			t.Cleanup(func() { close(release); <-finished })
			if _, err := o.Start(Request{Action: action}); err != nil {
				t.Fatal(err)
			}
			if _, err := o.Start(Request{Action: "poweroff"}); err == nil {
				t.Fatal("poweroff would interrupt active work")
			}
		})
	}
}

func TestScheduledPowerActionBlocksNewDownloads(t *testing.T) {
	o := &Operations{Run: func(context.Context, Request) (string, error) { return "ok", nil }}
	if _, err := o.Start(Request{Action: "poweroff"}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Start(Request{Action: "download"}); err != ErrBusy {
		t.Fatalf("download started while poweroff was scheduled: %v", err)
	}
}
