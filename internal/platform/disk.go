package platform

import "unicode/utf16"

// Drive is a disk the service sees (Windows) or a mounted local file system (Linux).
type Drive struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Root  string `json:"-"`
	Total int64  `json:"total"`
	Free  int64  `json:"free"`
	// Kind is fixed, removable (USB) or network.
	Kind string `json:"kind,omitempty"`
}

// minDriveSize hides tiny system and boot partitions.
const minDriveSize = 1 << 30

// driveRoots splits the answer of GetLogicalDriveStrings: roots ("C:\\") each ending in a zero,
// the list ending in another. A plain UTF-16 decoding stops at the first zero, keeping one drive.
func driveRoots(buf []uint16) []string {
	var out []string
	start := 0
	for i, c := range buf {
		if c != 0 {
			continue
		}
		if i > start {
			out = append(out, string(utf16.Decode(buf[start:i])))
		}
		start = i + 1
	}
	return out
}
