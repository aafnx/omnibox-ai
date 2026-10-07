//go:build !windows

package platform

// ConsoleUTF8 does nothing: Linux terminals use UTF-8.
func ConsoleUTF8() {}
