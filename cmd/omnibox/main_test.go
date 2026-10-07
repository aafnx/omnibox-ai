package main

import (
	"bytes"
	"strings"
	"testing"

	omnibox "github.com/aafnx/omnibox-ai"
)

func TestVersionMatchesVersionFile(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), omnibox.Version) || omnibox.Version == "" {
		t.Fatalf("output %q, version %q", out.String(), omnibox.Version)
	}
}

func TestServeRequiresPreviewUntilControlIsPorted(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"serve", "--root", t.TempDir()}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "--preview") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if code := run([]string{"unknown"}, &out, &errOut); code != 2 {
		t.Fatalf("unknown command exit %d", code)
	}
}
