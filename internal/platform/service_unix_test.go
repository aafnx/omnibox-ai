//go:build !windows

package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExecutableReadsTheUnitProgram(t *testing.T) {
	s := SystemServices{UnitDir: t.TempDir()}
	for _, exe := range []string{"/opt/omnibox/bin/0.18.0/omnibox", "/opt/omni box/bin/omnibox"} {
		spec := ServiceSpec{Name: "omnibox", Executable: exe, Args: []string{"serve", "--data", "/var/lib/omnibox"}}
		os.WriteFile(filepath.Join(s.UnitDir, "omnibox.service"), []byte(Unit(spec)), 0o644)
		if got, err := s.Executable("omnibox"); err != nil || got != exe {
			t.Fatalf("%q %v", got, err)
		}
	}
	if _, err := s.Executable("missing"); err == nil {
		t.Fatal("missing unit read")
	}
}
