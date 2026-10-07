//go:build !windows

package platform

import "os"

// RestrictToAdmins leaves the directory to root: it holds settings, logs with request text, and
// the socket of the ai command.
func RestrictToAdmins(path string) error { return os.Chmod(path, 0o700) }

// AddToPath does nothing on Linux: the ai command goes to /usr/local/bin, already on PATH.
func AddToPath(string) (func() error, error) { return func() error { return nil }, nil }

// RemoveFromPath does nothing on Linux, for the same reason.
func RemoveFromPath(string) error { return nil }

// RemoveAfterExit deletes the directory now: Linux removes a running program's file.
func RemoveAfterExit(dir string) error { return os.RemoveAll(dir) }
