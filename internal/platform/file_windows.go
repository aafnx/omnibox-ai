//go:build windows

package platform

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procReplaceFileW = kernel32.NewProc("ReplaceFileW")
)

// Readers share Delete so ReplaceFile can swap the file under them; os.Open does not.
func openShared(path string) (*os.File, error) {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	share := uint32(syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE)
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ, share, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// MoveFileEx with overwrite fails with access denied while a reader holds the target;
// ReplaceFile succeeds under readers that share Delete.
func replaceFile(source, target string) error {
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return os.Rename(source, target)
	}
	from, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r, _, callErr := procReplaceFileW.Call(uintptr(unsafe.Pointer(to)), uintptr(unsafe.Pointer(from)), 0, 0, 0, 0)
	if r == 0 {
		return &os.LinkError{Op: "replace", Old: source, New: target, Err: callErr}
	}
	return nil
}

const (
	errorFileNotFound     = syscall.Errno(2)
	errorPathNotFound     = syscall.Errno(3)
	errorAccessDenied     = syscall.Errno(5)
	errorSharingViolation = syscall.Errno(32)
	errorLockViolation    = syscall.Errno(33)
)

// Errors a reader sees while another process replaces the file; anything else is final.
func transient(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case errorFileNotFound, errorPathNotFound, errorAccessDenied, errorSharingViolation, errorLockViolation:
		return true
	}
	return false
}

func defaultDataDir() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return base + `\OmniBox AI`
}
