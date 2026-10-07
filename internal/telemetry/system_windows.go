//go:build windows

package telemetry

import (
	"context"
	"errors"
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procGetSystemTimes       = kernel32.NewProc("GetSystemTimes")
	procGlobalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")
)

func fileTime(ft syscall.Filetime) uint64 {
	return uint64(ft.HighDateTime)<<32 | uint64(ft.LowDateTime)
}

func cpuTimes() (idle, total uint64, err error) {
	var i, k, u syscall.Filetime
	r, _, callErr := procGetSystemTimes.Call(uintptr(unsafe.Pointer(&i)), uintptr(unsafe.Pointer(&k)), uintptr(unsafe.Pointer(&u)))
	if r == 0 {
		return 0, 0, callErr
	}
	// Kernel time includes idle time.
	return fileTime(i), fileTime(k) + fileTime(u), nil
}

func cpuPercent(ctx context.Context, window time.Duration) (float64, error) {
	idle1, total1, err := cpuTimes()
	if err != nil {
		return 0, err
	}
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-time.After(window):
	}
	idle2, total2, err := cpuTimes()
	if err != nil || total2 <= total1 {
		return 0, errors.New("no CPU progress")
	}
	return 100 * (1 - float64(idle2-idle1)/float64(total2-total1)), nil
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func memory() (total, available uint64, err error) {
	m := memoryStatusEx{}
	m.Length = uint32(unsafe.Sizeof(m))
	r, _, callErr := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&m)))
	if r == 0 {
		return 0, 0, callErr
	}
	return m.TotalPhys, m.AvailPhys, nil
}
