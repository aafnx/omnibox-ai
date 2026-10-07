// Package omnibox holds build-wide facts shared by every command.
package omnibox

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var versionFile string

// Version is the single product version from the VERSION file.
var Version = strings.TrimSpace(versionFile)
