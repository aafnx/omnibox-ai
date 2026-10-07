package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ContentMatch is one stored request whose text contains the search.
type ContentMatch struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Model     string    `json:"model"`
	Started   time.Time `json:"started"`
	Snippet   string    `json:"snippet"`
}

var sessionPattern = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

// searchLimit is how many matches the panel lists.
const searchLimit = 50

type storedContent struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Model     string          `json:"model"`
	Started   time.Time       `json:"started"`
	Request   json.RawMessage `json:"request"`
	Response  json.RawMessage `json:"response"`
	Text      *string         `json:"response_text"`
	Reasoning string          `json:"reasoning_text"`
}

// texts collects every string of a JSON value: messages, prompts and answers.
func texts(raw json.RawMessage, out *[]string) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			*out = append(*out, t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		}
	}
	walk(v)
}

// eachContent calls fn for the stored requests, oldest file first; it reads the files only on
// the owner's request.
func (g *Gateway) eachContent(fn func(line []byte, c storedContent)) {
	g.journal.Lock()
	defer g.journal.Unlock()
	for _, suffix := range []string{".2", ".1", ""} {
		f, err := os.Open(filepath.Join(g.Dir, "content.jsonl"+suffix))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64<<10), 8*textLimit)
		for scanner.Scan() {
			var c storedContent
			if json.Unmarshal(scanner.Bytes(), &c) == nil && c.ID != "" {
				fn(scanner.Bytes(), c)
			}
		}
		f.Close()
	}
}

// SearchContent finds stored requests whose text contains query (any case), newest first;
// session limits the search to one session.
func (g *Gateway) SearchContent(query, session string) []ContentMatch {
	query = strings.ToLower(strings.TrimSpace(query))
	out := []ContentMatch{}
	if utf8.RuneCountInString(query) < 2 || (session != "" && !sessionPattern.MatchString(session)) {
		return out
	}
	g.eachContent(func(_ []byte, c storedContent) {
		if session != "" && c.SessionID != session {
			return
		}
		var all []string
		texts(c.Request, &all)
		texts(c.Response, &all)
		if c.Text != nil {
			all = append(all, *c.Text)
		}
		all = append(all, c.Reasoning)
		for _, t := range all {
			if i := strings.Index(strings.ToLower(t), query); i >= 0 {
				out = append(out, ContentMatch{ID: c.ID, SessionID: c.SessionID, Model: c.Model, Started: c.Started, Snippet: snippet(t, i, len(query))})
				return
			}
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Started.After(out[j].Started) })
	if len(out) > searchLimit {
		out = out[:searchLimit]
	}
	return out
}

// snippet is the text around a match, cut at rune boundaries.
func snippet(text string, at, n int) string {
	lower := strings.ToLower(text)
	if len(lower) != len(text) {
		// Lower-casing changed byte lengths: show the start of the text instead of guessing.
		at, n = 0, 0
	}
	start, end := max(0, at-60), min(len(text), at+n+100)
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	s := strings.Join(strings.Fields(text[start:end]), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(text) {
		s += "…"
	}
	return s
}

// ExportSession returns the stored requests of a session as JSON lines, oldest first.
func (g *Gateway) ExportSession(session string) ([]byte, bool) {
	if !sessionPattern.MatchString(session) {
		return nil, false
	}
	var buf bytes.Buffer
	g.eachContent(func(line []byte, c storedContent) {
		if c.SessionID == session {
			buf.Write(line)
			buf.WriteByte('\n')
		}
	})
	return buf.Bytes(), buf.Len() > 0
}
