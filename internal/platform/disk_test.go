package platform

import (
	"slices"
	"testing"
	"unicode/utf16"
)

func TestDriveRootsKeepsEveryDrive(t *testing.T) {
	buf := utf16.Encode([]rune("A:\\\x00C:\\\x00D:\\\x00Z:\\\x00\x00"))
	if got := driveRoots(buf); !slices.Equal(got, []string{`A:\`, `C:\`, `D:\`, `Z:\`}) {
		t.Fatalf("%q", got)
	}
	if got := driveRoots(nil); len(got) != 0 {
		t.Fatalf("%q", got)
	}
}
