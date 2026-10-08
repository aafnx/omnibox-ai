//go:build !windows

package platform

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Sleep suspends the computer through systemd.
func Sleep() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "systemctl", "suspend").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl suspend: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// PowerOff shuts the computer down through systemd.
func PowerOff() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "systemctl", "poweroff").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl poweroff: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
