//go:build windows

package platform

import "testing"

func TestCommandLineRoundTrip(t *testing.T) {
	spec := ServiceSpec{Executable: `C:\Program Files\OmniBox AI\bin\0.18.0\omnibox.exe`, Args: []string{"serve", "--data", `C:\ProgramData\OmniBox AI`}}
	if got := firstArg(commandLine(spec)); got != spec.Executable {
		t.Fatalf("%q", got)
	}
	if got := firstArg(`C:\tools\omnibox.exe serve`); got != `C:\tools\omnibox.exe` {
		t.Fatalf("%q", got)
	}
}
