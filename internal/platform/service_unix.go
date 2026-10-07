//go:build !windows

package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SystemServices is systemd.
type SystemServices struct{ UnitDir string }

// NewServices returns the system service manager.
func NewServices() Services { return SystemServices{UnitDir: "/etc/systemd/system"} }

func systemctl(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("systemctl %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (SystemServices) Query(name string) (ServiceInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := systemctl(ctx, "show", name+".service", "--property=LoadState,ActiveState,UnitFileState")
	if err != nil {
		return ServiceInfo{}, err
	}
	props := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	if props["LoadState"] == "not-found" {
		return ServiceInfo{State: Missing}, nil
	}
	info := ServiceInfo{State: Stopped, StartType: "Manual"}
	switch props["ActiveState"] {
	case "active", "reloading":
		info.State = Running
	case "activating":
		info.State = StartPending
	case "deactivating":
		info.State = StopPending
	}
	switch props["UnitFileState"] {
	case "enabled", "enabled-runtime":
		info.StartType = "Automatic"
	case "masked", "masked-runtime":
		info.StartType = "Disabled"
	}
	return info, nil
}

func (SystemServices) Start(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := systemctl(ctx, "start", name+".service")
	return err
}

func (SystemServices) Stop(name string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout+5*time.Second)
	defer cancel()
	_, err := systemctl(ctx, "stop", name+".service")
	return err
}

func quoteArg(a string) string {
	if a != "" && !strings.ContainsAny(a, " \t\"'\\$%") {
		return a
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `$$`, `%`, `%%`).Replace(a) + `"`
}

// Unit renders the systemd unit for a service. Engines run in their own control group,
// so stopping the unit stops every child process (Ollama runners included).
func Unit(spec ServiceSpec) string {
	exec := []string{quoteArg(spec.Executable)}
	for _, a := range spec.Args {
		exec = append(exec, quoteArg(a))
	}
	restart := "no"
	if spec.Automatic {
		restart = "on-failure"
	}
	return fmt.Sprintf(`[Unit]
Description=%s
After=network-online.target

[Service]
ExecStart=%s
KillMode=control-group
Restart=%s
RestartSec=5

[Install]
WantedBy=multi-user.target
`, spec.DisplayName, strings.Join(exec, " "), restart)
}

func (s SystemServices) Install(spec ServiceSpec) error {
	path := filepath.Join(s.UnitDir, spec.Name+".service")
	if err := WriteAtomic(path, []byte(Unit(spec)), 0o644); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	if spec.Automatic {
		_, err := systemctl(ctx, "enable", spec.Name+".service")
		return err
	}
	return nil
}

func (s SystemServices) Remove(name string, timeout time.Duration) error {
	_ = s.Stop(name, timeout)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = systemctl(ctx, "disable", name+".service")
	if err := os.Remove(filepath.Join(s.UnitDir, name+".service")); err != nil && !os.IsNotExist(err) {
		return err
	}
	_, err := systemctl(ctx, "daemon-reload")
	return err
}

// IsAdmin reports whether the process runs as root.
func IsAdmin() bool { return os.Geteuid() == 0 }

func (s SystemServices) Change(spec ServiceSpec) error {
	if _, err := os.Stat(filepath.Join(s.UnitDir, spec.Name+".service")); err != nil {
		return err
	}
	return s.Install(spec)
}

func (s SystemServices) Executable(name string) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.UnitDir, name+".service"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "ExecStart="); ok {
			if strings.HasPrefix(rest, `"`) {
				if end := strings.Index(rest[1:], `"`); end >= 0 {
					return strings.NewReplacer(`\\`, `\`, `\"`, `"`, `$$`, `$`, `%%`, `%`).Replace(rest[1 : end+1]), nil
				}
			}
			first, _, _ := strings.Cut(rest, " ")
			return first, nil
		}
	}
	return "", fmt.Errorf("в службе %s нет ExecStart", name)
}
