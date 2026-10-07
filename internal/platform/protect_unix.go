//go:build !windows

package platform

// Protect returns data as is: on Linux the file is readable by root only, which is what DPAPI
// adds on Windows.
func Protect(data []byte) ([]byte, error) { return data, nil }

// Unprotect reverses Protect.
func Unprotect(data []byte) ([]byte, error) { return data, nil }
