//go:build linux

package platform

import (
	"bufio"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// ReadSecret reads a line from the terminal without showing it.
func ReadSecret() (string, error) {
	fd := int(os.Stdin.Fd())
	if t, err := unix.IoctlGetTermios(fd, unix.TCGETS); err == nil {
		quiet := *t
		quiet.Lflag &^= unix.ECHO
		_ = unix.IoctlSetTermios(fd, unix.TCSETS, &quiet)
		defer unix.IoctlSetTermios(fd, unix.TCSETS, t)
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	os.Stdout.WriteString("\n")
	return strings.TrimSpace(line), err
}
