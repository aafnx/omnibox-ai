//go:build !windows

package platform

import "os"

// RestrictToAdmins leaves the directory to root: it holds settings, logs with request text, and
// the socket of the ai command.
func RestrictToAdmins(path string) error { return os.Chmod(path, 0o700) }

// AddToPath does nothing on Linux: the ai command goes to /usr/local/bin, already on PATH.
func AddToPath(string) (func() error, error) { return func() error { return nil }, nil }
