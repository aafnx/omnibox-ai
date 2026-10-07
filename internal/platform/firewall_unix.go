//go:build !windows

package platform

// ProgramDir is where the program is installed.
func ProgramDir() string { return "/opt/omnibox" }

// AllowPort does nothing on Linux: distributions use different firewalls, and the installer
// tells the owner which port to open.
func AllowPort(string, int) error { return nil }

// RemovePortRule does nothing on Linux.
func RemovePortRule(string) error { return nil }
