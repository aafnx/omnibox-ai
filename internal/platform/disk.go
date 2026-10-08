package platform

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
