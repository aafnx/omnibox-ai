//go:build windows

package platform

import (
	"strings"

	"golang.org/x/sys/windows"
)

// Drives lists ready fixed drives with their size and free space.
func Drives() []Drive {
	buf := make([]uint16, 256)
	n, err := windows.GetLogicalDriveStrings(uint32(len(buf)), &buf[0])
	if err != nil || n == 0 {
		return nil
	}
	var out []Drive
	for _, root := range strings.Split(windows.UTF16ToString(buf[:n]), "\x00") {
		if root == "" {
			continue
		}
		path, _ := windows.UTF16PtrFromString(root)
		if windows.GetDriveType(path) != windows.DRIVE_FIXED {
			continue
		}
		var free, total, totalFree uint64
		if windows.GetDiskFreeSpaceEx(path, &free, &total, &totalFree) != nil || total < minDriveSize {
			continue
		}
		label := make([]uint16, windows.MAX_PATH+1)
		_ = windows.GetVolumeInformation(path, &label[0], uint32(len(label)), nil, nil, nil, nil, 0)
		out = append(out, Drive{Name: strings.TrimRight(root, `\`), Label: windows.UTF16ToString(label), Root: root, Total: int64(total), Free: int64(free)})
	}
	return out
}
