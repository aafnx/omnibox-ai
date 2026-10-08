//go:build windows

package platform

import (
	"strings"

	"golang.org/x/sys/windows"
)

// Drives lists ready fixed, removable (USB) and network drives the service sees, with their
// size and free space; CD drives and empty card readers are skipped.
func Drives() []Drive {
	buf := make([]uint16, 256)
	n, err := windows.GetLogicalDriveStrings(uint32(len(buf)), &buf[0])
	if err != nil || n == 0 {
		return nil
	}
	var out []Drive
	for _, root := range driveRoots(buf[:n]) {
		path, _ := windows.UTF16PtrFromString(root)
		kind := map[uint32]string{windows.DRIVE_FIXED: "fixed", windows.DRIVE_REMOVABLE: "removable", windows.DRIVE_REMOTE: "network", windows.DRIVE_RAMDISK: "fixed"}[windows.GetDriveType(path)]
		if kind == "" {
			continue
		}
		var free, total, totalFree uint64
		if windows.GetDiskFreeSpaceEx(path, &free, &total, &totalFree) != nil || total < minDriveSize {
			continue
		}
		label := make([]uint16, windows.MAX_PATH+1)
		_ = windows.GetVolumeInformation(path, &label[0], uint32(len(label)), nil, nil, nil, nil, 0)
		out = append(out, Drive{Name: strings.TrimRight(root, `\`), Label: windows.UTF16ToString(label), Root: root, Total: int64(total), Free: int64(free), Kind: kind})
	}
	return out
}
