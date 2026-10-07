//go:build !windows

package platform

import "os"

func openShared(path string) (*os.File, error) { return os.Open(path) }

// rename(2) replaces the target atomically; open readers keep the old inode.
func replaceFile(source, target string) error { return os.Rename(source, target) }

// Readers never see transient lock errors on Unix: only a missing path is retried briefly.
func transient(err error) bool { return os.IsNotExist(err) }
