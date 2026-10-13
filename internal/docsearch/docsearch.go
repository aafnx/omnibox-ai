// Package docsearch picks the parts of a project's documents that answer a question when all of
// them do not fit in the model's context. It ranks fragments by the question's words (BM25 with
// a rough stemmer for Russian and English) on the PC, without a second model or an index on disk.
package docsearch

import (
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ChunkRunes is the size of a fragment: a few paragraphs.
const ChunkRunes = 1200

// Doc is a document by name with its text.
type Doc struct {
	Name string
	Text string
}

// Piece is a fragment of a document: Part counts from 0 in the document.
type Piece struct {
	Doc  string
	Part int
	Of   int
	Text string
}

// Pick returns the whole documents when they fit in chars; otherwise the fragments that match
// the question best, then the beginnings of the documents while room is left, in the order of
// the documents. whole tells which of the two it did.
func Pick(docs []Doc, question string, chars int) (pieces []Piece, whole bool) {
	total := 0
	for _, d := range docs {
		total += utf8.RuneCountInString(d.Text)
	}
	if total <= chars {
		for _, d := range docs {
			pieces = append(pieces, Piece{Doc: d.Name, Part: 0, Of: 1, Text: d.Text})
		}
		return pieces, true
	}
	var all []Piece
	for _, d := range docs {
		parts := Split(d.Text, ChunkRunes)
		for i, p := range parts {
			all = append(all, Piece{Doc: d.Name, Part: i, Of: len(parts), Text: p})
		}
	}
	order := Rank(all, question)
	// Then the start of each document, part by part across documents.
	byPart := make([]int, len(all))
	for i := range byPart {
		byPart[i] = i
	}
	slices.SortStableFunc(byPart, func(a, b int) int { return all[a].Part - all[b].Part })
	taken := make([]bool, len(all))
	left := chars
	for _, i := range append(order, byPart...) {
		n := utf8.RuneCountInString(all[i].Text)
		if taken[i] || n > left {
			continue
		}
		taken[i], left = true, left-n
	}
	for i, p := range all {
		if taken[i] {
			pieces = append(pieces, p)
		}
	}
	return pieces, false
}

// Split cuts text into fragments of up to size runes at paragraph breaks, then at sentence
// ends or spaces when a paragraph is longer.
func Split(text string, size int) []string {
	var out []string
	var cur strings.Builder
	n := 0
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
		n = 0
	}
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimRight(para, " \t\r")
		for utf8.RuneCountInString(para) > size {
			r := []rune(para)
			cut := breakAt(r, size)
			flush()
			out = append(out, strings.TrimSpace(string(r[:cut])))
			para = strings.TrimSpace(string(r[cut:]))
		}
		m := utf8.RuneCountInString(para) + 1
		if n+m > size {
			flush()
		}
		cur.WriteString(para)
		cur.WriteByte('\n')
		n += m
	}
	flush()
	return out
}

// breakAt is where to cut a long paragraph: after the last sentence end, else the last space,
// in the second half of size.
func breakAt(r []rune, size int) int {
	for i := size - 1; i > size/2; i-- {
		if (r[i] == '.' || r[i] == '!' || r[i] == '?' || r[i] == ';') && i+1 < len(r) && unicode.IsSpace(r[i+1]) {
			return i + 1
		}
	}
	for i := size - 1; i > size/2; i-- {
		if unicode.IsSpace(r[i]) {
			return i + 1
		}
	}
	return size
}

// Rank returns the indexes of the pieces that have words of the question, best first.
func Rank(pieces []Piece, question string) []int {
	query := slices.Compact(slices.Sorted(slices.Values(Terms(question))))
	if len(query) == 0 || len(pieces) == 0 {
		return nil
	}
	const k1, b = 1.2, 0.75
	tf := make([]map[string]int, len(pieces))
	lens := make([]int, len(pieces))
	df := map[string]int{}
	avg := 0.0
	for i, p := range pieces {
		terms := Terms(p.Text)
		lens[i] = len(terms)
		avg += float64(len(terms))
		tf[i] = map[string]int{}
		for _, t := range terms {
			tf[i][t]++
		}
		for _, q := range query {
			if tf[i][q] > 0 {
				df[q]++
			}
		}
	}
	avg = max(avg/float64(len(pieces)), 1)
	scores := make([]float64, len(pieces))
	var out []int
	for i := range pieces {
		for _, q := range query {
			f := float64(tf[i][q])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(pieces)-df[q])+0.5)/(float64(df[q])+0.5))
			scores[i] += idf * f * (k1 + 1) / (f + k1*(1-b+b*float64(lens[i])/avg))
		}
		if scores[i] > 0 {
			out = append(out, i)
		}
	}
	slices.SortStableFunc(out, func(x, y int) int {
		switch {
		case scores[x] > scores[y]:
			return -1
		case scores[x] < scores[y]:
			return 1
		}
		return 0
	})
	return out
}

// Terms are the words of text as stems, without the most common short words.
func Terms(text string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(c rune) bool {
		return !unicode.IsLetter(c) && !unicode.IsDigit(c)
	}) {
		if utf8.RuneCountInString(w) < 2 || stop[w] {
			continue
		}
		out = append(out, Stem(w))
	}
	return out
}

// Stem keeps the first six letters of a long word and drops the vowel ending of a short one, so
// «договор» and «договора», «цена» and «цены» meet. Numbers stay whole.
func Stem(w string) string {
	r := []rune(w)
	if unicode.IsDigit(r[0]) {
		return w
	}
	if len(r) > 6 {
		return string(r[:6])
	}
	for len(r) > 3 && strings.ContainsRune("аеёиоуыэюяйьe", r[len(r)-1]) {
		r = r[:len(r)-1]
	}
	return string(r)
}

var stop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`и в во на не что как по с со о об от до из за для к ко а но да
		ли же бы то это этот эта эти тот та те так там тут где когда или ни уже еще ещё мне меня мой моя
		мы вы он она они оно его ее её их им ему ей нам вам вас нас при про под над без через между
		есть быть был была были будет все всё весь вся какой какая какие который которая которые
		чем чтобы если раз только the a an of to in on for and or is are was be it this that with by`) {
		stop[w] = true
	}
}
