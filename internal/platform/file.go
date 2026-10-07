// Package platform hides operating system differences behind small functions.
package platform

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Measured on Windows runners: a file being replaced stays locked for up to half a second,
// then its path briefly disappears. A path missing for longer than missingLimit is really missing.
const (
	lockLimit    = 5 * time.Second
	missingLimit = 200 * time.Millisecond
)

// ReadShared reads a whole file without blocking a writer that atomically replaces it.
func ReadShared(path string) ([]byte, error) {
	start := time.Now()
	var missingSince time.Time
	delay := 5 * time.Millisecond
	for {
		data, err := readOnce(path)
		if err == nil {
			return data, nil
		}
		hidden := errors.Is(err, os.ErrNotExist)
		if hidden {
			if missingSince.IsZero() {
				missingSince = time.Now()
			}
		} else {
			missingSince = time.Time{}
		}
		retry := transient(err) && time.Since(start) < lockLimit && (!hidden || time.Since(missingSince) < missingLimit)
		if !retry {
			return nil, err
		}
		time.Sleep(delay)
		delay = min(delay*2, 50*time.Millisecond)
	}
}

func readOnce(path string) ([]byte, error) {
	f, err := openShared(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// WriteAtomic writes data to a temporary file next to path and puts it in place in one step,
// so readers see either the old or the new content, never a partial file.
func WriteAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return replaceFile(name, path)
}

// DataDir is where config.json, secrets, logs, engines and models live by default.
func DataDir() string {
	if v := os.Getenv("OMNIBOX_DATA"); v != "" {
		return v
	}
	return defaultDataDir()
}
