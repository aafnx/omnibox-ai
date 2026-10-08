//go:build !windows

package platform

import (
	"bufio"
	"os"
	"strings"
	"syscall"
)

// localFS are file systems on local disks; pseudo, network and overlay mounts are skipped.
var localFS = map[string]bool{"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true, "zfs": true, "f2fs": true, "ntfs": true, "ntfs3": true, "vfat": true, "exfat": true}

// Drives lists mounted local file systems with their size and free space.
func Drives() []Drive {
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Drive
	seen := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || !localFS[fields[2]] || seen[fields[0]] {
			continue
		}
		mount := strings.ReplaceAll(fields[1], `\040`, " ")
		var st syscall.Statfs_t
		if syscall.Statfs(mount, &st) != nil {
			continue
		}
		total, free := int64(st.Blocks)*int64(st.Bsize), int64(st.Bavail)*int64(st.Bsize)
		if total < minDriveSize {
			continue
		}
		seen[fields[0]] = true
		root := mount
		if !strings.HasSuffix(root, "/") {
			root += "/"
		}
		out = append(out, Drive{Name: mount, Label: fields[0], Root: root, Total: total, Free: free, Kind: "fixed"})
	}
	return out
}
