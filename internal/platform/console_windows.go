//go:build windows

package platform

import "golang.org/x/sys/windows"

// ConsoleUTF8 (code page 65001) makes the console show Russian text: Go writes UTF-8, while a Windows console
// starts in an OEM code page. Without a console (a service) it does nothing.
func ConsoleUTF8() {
	_ = windows.SetConsoleOutputCP(65001)
	_ = windows.SetConsoleCP(65001)
}
