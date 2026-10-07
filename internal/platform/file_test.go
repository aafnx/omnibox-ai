package platform

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteAtomicReplacesWhileReaderHoldsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := WriteAtomic(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := openShared(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := WriteAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("replace under an open reader: %v", err)
	}
	data, err := ReadShared(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("got %q, %v", data, err)
	}
	matches, _ := filepath.Glob(path + ".tmp-*")
	if len(matches) != 0 {
		t.Fatalf("temporary files left: %v", matches)
	}
}

func TestReadSharedFailsFastForMissingFile(t *testing.T) {
	start := time.Now()
	_, err := ReadShared(filepath.Join(t.TempDir(), "missing.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want not-exist, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("missing file took %v", time.Since(start))
	}
}

func TestConcurrentReadersNeverFailDuringReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := WriteAtomic(path, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	failed := make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				close(failed)
				return
			default:
			}
			if _, err := ReadShared(path); err != nil {
				failed <- err
				return
			}
		}
	}()
	for i := 0; i < 300; i++ {
		if err := WriteAtomic(path, []byte(`{"a":1}`), 0o600); err != nil {
			close(stop)
			t.Fatal(err)
		}
	}
	close(stop)
	if err := <-failed; err != nil {
		t.Fatalf("reader failed during replacement: %v", err)
	}
}
