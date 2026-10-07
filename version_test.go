package omnibox

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// VERSION is the only place the version is written; the documents that mention it must agree.
func TestVersionIsTheSingleSource(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Version) {
		t.Fatalf("VERSION %q is not x.y.z", Version)
	}
	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	v := regexp.QuoteMeta(Version)
	checks := []struct{ file, pattern string }{
		{"README.md", `Версия \*\*` + v + `\*\*`},
		{"CHANGELOG.md", `(?m)^## ` + v + ` — \d{4}-\d{2}-\d{2}$`},
		{"docs/roadmap.md", `Состояние на [^,]+, версия \*\*` + v + `\*\*`},
		{"docs/roadmap.md", `(?m)^### ` + v + `( |$)`},
	}
	for _, c := range checks {
		if !regexp.MustCompile(c.pattern).MatchString(read(c.file)) {
			t.Errorf("%s does not mention version %s (%s)", c.file, Version, c.pattern)
		}
	}
	// The newest changelog entry is the current version.
	first := regexp.MustCompile(`(?m)^## (\S+)`).FindStringSubmatch(read("CHANGELOG.md"))
	if first == nil || first[1] != Version {
		t.Errorf("the first CHANGELOG entry is %v, want %s", first, Version)
	}
	if strings.Contains(read("cmd/omnibox/main.go"), `"`+Version+`"`) {
		t.Error("the version is written into code instead of read from VERSION")
	}
}
