//go:build windows

package platform

import (
	"os"
	"strings"
	"testing"
)

func TestDrivesListsTheSystemDrive(t *testing.T) {
	system := strings.ToUpper(os.Getenv("SystemDrive"))
	for _, d := range Drives() {
		if strings.EqualFold(d.Name, system) {
			return
		}
	}
	t.Fatalf("%s is missing from %+v", system, Drives())
}
