//go:build windows

package platform

import "golang.org/x/sys/windows"

// Sleep puts the computer to sleep (not hibernation); it wakes by Wake-on-LAN or the power key.
func Sleep() error {
	powrprof := windows.NewLazySystemDLL("powrprof.dll")
	r, _, err := powrprof.NewProc("SetSuspendState").Call(0, 0, 0)
	if r == 0 {
		return err
	}
	return nil
}
