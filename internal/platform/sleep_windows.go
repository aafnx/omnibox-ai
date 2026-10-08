//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Sleep puts the computer to sleep (not hibernation); it wakes by Wake-on-LAN or the power key.
func Sleep() error {
	powrprof := windows.NewLazySystemDLL("powrprof.dll")
	r, _, err := powrprof.NewProc("SetSuspendState").Call(0, 0, 0)
	if r == 0 {
		return err
	}
	return nil
}

// PowerOff shuts the computer down with the system's own shutdown.exe from System32: a planned
// shutdown, so Windows closes programs normally and Wake-on-LAN stays armed.
func PowerOff() error {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	exe := filepath.Join(root, "System32", "shutdown.exe")
	if out, err := exec.Command(exe, "/s", "/t", "0", "/d", "p:0:0").CombinedOutput(); err != nil {
		return fmt.Errorf("shutdown.exe: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
