//go:build windows

package platform

import (
	"bufio"
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// ReadSecret reads a line from the console without showing it.
func ReadSecret() (string, error) {
	h := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err == nil {
		_ = windows.SetConsoleMode(h, mode&^windows.ENABLE_ECHO_INPUT)
		defer windows.SetConsoleMode(h, mode)
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	os.Stdout.WriteString("\n")
	return strings.TrimSpace(line), err
}
