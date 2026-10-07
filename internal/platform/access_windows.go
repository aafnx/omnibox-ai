//go:build windows

package platform

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// RestrictToAdmins leaves only SYSTEM and Administrators access to a directory and everything
// in it: it holds settings, logs with request text, and the socket of the ai command.
func RestrictToAdmins(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "icacls", path, "/inheritance:r", "/grant:r", "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("icacls: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

const environmentKey = `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`

// AddToPath adds a directory to the machine PATH (new terminals see it) and returns how to undo it.
func AddToPath(dir string) (func() error, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, environmentKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	old, kind, err := k.GetStringValue("Path")
	if err != nil {
		return nil, err
	}
	for _, p := range strings.Split(old, ";") {
		if strings.EqualFold(strings.TrimRight(p, `\`), strings.TrimRight(dir, `\`)) {
			return func() error { return nil }, nil
		}
	}
	set := func(value string) error {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, environmentKey, registry.SET_VALUE)
		if err != nil {
			return err
		}
		defer k.Close()
		if kind == registry.EXPAND_SZ {
			err = k.SetExpandStringValue("Path", value)
		} else {
			err = k.SetStringValue("Path", value)
		}
		broadcastEnvironment()
		return err
	}
	if err := set(strings.TrimRight(old, ";") + ";" + dir); err != nil {
		return nil, err
	}
	return func() error { return set(old) }, nil
}

// RemoveFromPath takes a directory out of the machine PATH.
func RemoveFromPath(dir string) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, environmentKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	old, kind, err := k.GetStringValue("Path")
	if err != nil {
		return err
	}
	var kept []string
	for _, p := range strings.Split(old, ";") {
		if p != "" && !strings.EqualFold(strings.TrimRight(p, `\`), strings.TrimRight(dir, `\`)) {
			kept = append(kept, p)
		}
	}
	value := strings.Join(kept, ";")
	if value == strings.Trim(old, ";") {
		return nil
	}
	if kind == registry.EXPAND_SZ {
		err = k.SetExpandStringValue("Path", value)
	} else {
		err = k.SetStringValue("Path", value)
	}
	broadcastEnvironment()
	return err
}

// RemoveAfterExit deletes a directory a few seconds after this process ends: Windows does not
// delete a running program, and the uninstaller runs from the directory it removes.
func RemoveAfterExit(dir string) error {
	if strings.ContainsAny(dir, "\"&|<>^%!") {
		return fmt.Errorf("необычный путь каталога программы: %s", dir)
	}
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `cmd.exe /c ping -n 4 127.0.0.1 >nul & rmdir /s /q "` + dir + `"`,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.DETACHED_PROCESS,
	}
	return cmd.Start()
}

// broadcastEnvironment tells Explorer that the environment changed, so new terminals get PATH.
func broadcastEnvironment() {
	env, _ := windows.UTF16PtrFromString("Environment")
	user32 := windows.NewLazySystemDLL("user32.dll")
	send := user32.NewProc("SendMessageTimeoutW")
	const hwndBroadcast, wmSettingChange, abortIfHung = 0xffff, 0x1a, 0x2
	_, _, _ = send.Call(hwndBroadcast, wmSettingChange, 0, uintptr(unsafe.Pointer(env)), abortIfHung, 5000, 0)
}
