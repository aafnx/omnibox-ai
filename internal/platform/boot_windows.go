//go:build windows

package platform

import (
	"time"

	"golang.org/x/sys/windows"
)

// BootTime is when the system started.
func BootTime() time.Time {
	return time.Now().Add(-time.Duration(windows.DurationSinceBoot()))
}
