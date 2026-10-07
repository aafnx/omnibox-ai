//go:build windows

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ProgramDir is where the program is installed.
func ProgramDir() string {
	base := os.Getenv("ProgramFiles")
	if base == "" {
		base = `C:\Program Files`
	}
	return base + `\OmniBox AI`
}

func netsh(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "netsh", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AllowPort opens a TCP port for the local subnet under a named Windows Firewall rule.
func AllowPort(rule string, port int) error {
	_ = RemovePortRule(rule)
	return netsh("advfirewall", "firewall", "add", "rule", "name="+rule, "dir=in", "action=allow", "protocol=TCP",
		"localport="+strconv.Itoa(port), "remoteip=localsubnet", "profile=any")
}

// RemovePortRule deletes the named rule; a missing rule is not an error.
func RemovePortRule(rule string) error {
	_ = netsh("advfirewall", "firewall", "delete", "rule", "name="+rule)
	return nil
}
