package omnibox

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// Windows PowerShell 5.1 reads a script without a BOM in the ANSI code page and garbles
// Russian text; the release workflow publishes install.ps1 as it is in the repository.
func TestBootstrapScripts(t *testing.T) {
	ps, err := os.ReadFile("install.ps1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(ps, []byte("\xef\xbb\xbf")) {
		t.Fatal("install.ps1 needs a UTF-8 BOM")
	}
	sh, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"install.ps1": string(ps), "install.sh": string(sh)} {
		// Both verify the release before running it, and never put a token on a command line.
		if !strings.Contains(text, "SHA256SUMS") || !strings.Contains(text, " install") {
			t.Errorf("%s does not check SHA256SUMS before running omnibox install", name)
		}
		if strings.Contains(text, "--token") || strings.Contains(text, "GH_TOKEN=") {
			t.Errorf("%s passes a token on a command line", name)
		}
	}
	if bytes.Contains(sh, []byte("\r\n")) {
		t.Fatal("install.sh has CRLF line endings")
	}
}
