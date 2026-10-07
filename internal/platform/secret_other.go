//go:build !windows && !linux

package platform

import (
	"bufio"
	"os"
	"strings"
)

// ReadSecret reads a line; this system is not a target, so the input is not hidden.
func ReadSecret() (string, error) {
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(line), err
}
