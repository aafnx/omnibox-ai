package chatrun

import (
	"errors"
	"strings"
)

var errToolMarkup = errors.New("model emitted tool markup in content")

// Some providers leak their model's internal tool syntax into content. Hold partial markers
// across SSE chunks so a malformed command never flashes in the UI or enters chat history.
type toolTextFilter struct {
	pending string
	blocked bool
}

func (f *toolTextFilter) split(text string) string {
	if f.blocked {
		return ""
	}
	f.pending += text
	lower := strings.ToLower(f.pending)
	markers := []string{"<tool_call", "<function=", "<|tool_call", "[tool_call]"}
	first, keep := len(lower), 0
	for _, marker := range markers {
		if i := strings.Index(lower, marker); i >= 0 && i < first {
			first = i
		}
		for n := 1; n < len(marker) && n <= len(lower); n++ {
			if strings.HasSuffix(lower, marker[:n]) && n > keep {
				keep = n
			}
		}
	}
	if first < len(lower) {
		out := f.pending[:first]
		f.pending, f.blocked = "", true
		return out
	}
	out := f.pending[:len(f.pending)-keep]
	f.pending = f.pending[len(f.pending)-keep:]
	return out
}
