// Package docx fills a Word template without touching its formatting: the text of the places
// to fill is replaced inside the document's own XML, byte for byte around it, and every other
// part of the file is copied as it was. The model that decides what goes where never sees or
// writes XML.
package docx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// MaxFile is the largest template accepted; a contract with pictures stays far below it.
const MaxFile = 25 << 20

// maxPart limits one XML part read into memory.
const maxPart = 64 << 20

// Para is a paragraph of text: its number in the document, its text and where it stands.
type Para struct {
	N     int    `json:"n"`
	Text  string `json:"text"`
	Where string `json:"where,omitempty"`
}

// Edit replaces Find in paragraph Para with Value: the Occurrence-th match (from 0), or every
// match when Occurrence is -1. With Context, the match is that of Context, and Find is replaced
// inside it: "серии ____ №" tells which blank of "серии ____ № ______" is the series. A line
// break in Value becomes a line break in Word.
type Edit struct {
	Para       int
	Context    string
	Find       string
	Occurrence int
	Value      string
}

// Doc is an opened template.
type Doc struct {
	zip   *zip.Reader
	parts []*part
	paras []*para
}

type part struct {
	name string
	data []byte
}

type para struct {
	part  *part
	where string
	nodes []*node
	// fallback marks the copy of a text box Word keeps for old versions; it is not offered.
	fallback bool
}

// node is one <w:t> element: the byte range of its start tag and of its content in the part.
// A tab or a line break is a fixed node: it is part of the text the model reads, so "г. Казань"
// and the date after a tab do not run together, but it is never rewritten.
type node struct {
	tagStart, tagEnd int
	end              int // where the content ends (start of </w:t>)
	text             string
	fixed            bool
}

func (p *para) text() string {
	var b strings.Builder
	for _, n := range p.nodes {
		b.WriteString(n.text)
	}
	return b.String()
}

// Open reads a .docx file.
func Open(data []byte) (*Doc, error) {
	if len(data) > MaxFile {
		return nil, errors.New("Шаблон больше 25 МБ.")
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("Это не файл Word (.docx).")
	}
	d := &Doc{zip: z}
	var names []string
	for _, f := range z.File {
		if textPart(f.Name) {
			names = append(names, f.Name)
		}
	}
	if !contains(names, "word/document.xml") {
		return nil, errors.New("В файле нет текста документа Word: это не .docx или он повреждён.")
	}
	// The body first, then headers and footers in their own order.
	sort.SliceStable(names, func(i, j int) bool { return rank(names[i]) < rank(names[j]) })
	for _, name := range names {
		data, err := readFile(z, name)
		if err != nil {
			return nil, err
		}
		p := &part{name: name, data: data}
		paras, err := scan(p)
		if err != nil {
			return nil, fmt.Errorf("Файл Word повреждён (%s).", path.Base(name))
		}
		d.parts = append(d.parts, p)
		d.paras = append(d.paras, paras...)
	}
	return d, nil
}

func textPart(name string) bool {
	if name == "word/document.xml" {
		return true
	}
	base := path.Base(name)
	return path.Dir(name) == "word" && strings.HasSuffix(base, ".xml") && (strings.HasPrefix(base, "header") || strings.HasPrefix(base, "footer"))
}

func rank(name string) int {
	switch {
	case name == "word/document.xml":
		return 0
	case strings.Contains(name, "header"):
		return 1
	}
	return 2
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func readFile(z *zip.Reader, name string) ([]byte, error) {
	for _, f := range z.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, errors.New("Файл Word не читается.")
		}
		defer rc.Close()
		data, err := io.ReadAll(io.LimitReader(rc, maxPart+1))
		if err != nil || len(data) > maxPart {
			return nil, errors.New("Файл Word не читается или слишком велик.")
		}
		return data, nil
	}
	return nil, errors.New("Файл Word не читается.")
}

// table tracks the position inside one table while scanning.
type table struct{ n, row, col int }

// scan finds the paragraphs and their <w:t> elements. Text belongs to the innermost paragraph
// (a text box is a paragraph inside a paragraph); deleted text and field codes are other
// elements and are left alone.
func scan(p *part) ([]*para, error) {
	dec := xml.NewDecoder(bytes.NewReader(p.data))
	dec.Strict = false
	var (
		out    []*para
		open   []*para
		tables []table
		count  int
		cur    *node
		buf    strings.Builder
		old    int // depth inside <mc:Fallback>
		runs   int // depth inside <w:r>
	)
	label := partLabel(p.name)
	for {
		before := dec.InputOffset()
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		after := dec.InputOffset()
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space == "mc" && t.Name.Local == "Fallback" {
				old++
			}
			if t.Name.Space != "w" {
				continue
			}
			selfClosing := after >= 2 && p.data[after-2] == '/'
			switch t.Name.Local {
			case "p":
				if selfClosing {
					continue
				}
				where := label
				if len(tables) > 0 {
					tb := tables[len(tables)-1]
					where = joinWhere(label, fmt.Sprintf("таблица %d, строка %d, столбец %d", tb.n, tb.row, tb.col))
				}
				pa := &para{part: p, where: where, fallback: old > 0}
				open = append(open, pa)
				out = append(out, pa)
			case "r":
				if !selfClosing {
					runs++
				}
			case "tab", "br", "cr":
				// Inside a run they are text; in paragraph properties <w:tab> is a tab stop.
				if runs > 0 && len(open) > 0 {
					s := "\t"
					if t.Name.Local != "tab" {
						s = "\n"
					}
					open[len(open)-1].nodes = append(open[len(open)-1].nodes, &node{text: s, fixed: true})
				}
			case "tbl":
				count++
				tables = append(tables, table{n: count})
			case "tr":
				if len(tables) > 0 {
					tables[len(tables)-1].row++
					tables[len(tables)-1].col = 0
				}
			case "tc":
				if len(tables) > 0 {
					tables[len(tables)-1].col++
				}
			case "t":
				if selfClosing || len(open) == 0 {
					continue
				}
				cur = &node{tagStart: int(before), tagEnd: int(after)}
				buf.Reset()
			}
		case xml.CharData:
			if cur != nil {
				buf.Write(t)
			}
		case xml.EndElement:
			if t.Name.Space == "mc" && t.Name.Local == "Fallback" && old > 0 {
				old--
			}
			if t.Name.Space != "w" {
				continue
			}
			switch t.Name.Local {
			case "t":
				if cur != nil {
					cur.end = int(before)
					cur.text = buf.String()
					open[len(open)-1].nodes = append(open[len(open)-1].nodes, cur)
					cur = nil
				}
			case "p":
				if len(open) > 0 {
					open = open[:len(open)-1]
				}
			case "r":
				if runs > 0 {
					runs--
				}
			case "tbl":
				if len(tables) > 0 {
					tables = tables[:len(tables)-1]
				}
			}
		}
	}
	return out, nil
}

func partLabel(name string) string {
	switch {
	case strings.Contains(name, "header"):
		return "верхний колонтитул"
	case strings.Contains(name, "footer"):
		return "нижний колонтитул"
	}
	return ""
}

func joinWhere(a, b string) string {
	if a == "" {
		return b
	}
	return a + ", " + b
}

// Paragraphs lists the paragraphs that hold text, numbered as Edit.Para expects.
func (d *Doc) Paragraphs() []Para {
	var out []Para
	for i, p := range d.paras {
		if t := p.text(); strings.TrimSpace(t) != "" && !p.fallback {
			out = append(out, Para{N: i, Text: t, Where: p.where})
		}
	}
	return out
}

// Text is the paragraph's text, "" for a number out of range.
func (d *Doc) Text(n int) string {
	if n < 0 || n >= len(d.paras) {
		return ""
	}
	return d.paras[n].text()
}

// Count is how many times find occurs in paragraph n.
func (d *Doc) Count(n int, find string) int {
	if find == "" {
		return 0
	}
	return strings.Count(d.Text(n), find)
}

// span is one replacement inside a paragraph's text.
type span struct {
	start, end int
	value      string
}

// Fill returns a copy of the template with the edits made. Every edit must find its text: a
// template that changed since its places were chosen is refused rather than half filled.
func (d *Doc) Fill(edits []Edit) ([]byte, error) {
	byPara := map[int][]span{}
	for _, e := range edits {
		if e.Para < 0 || e.Para >= len(d.paras) || e.Find == "" {
			return nil, fmt.Errorf("Место «%s» не найдено в шаблоне.", clip(e.Find, 40))
		}
		text := d.paras[e.Para].text()
		outer, inner := e.Find, 0
		if e.Context != "" {
			outer = e.Context
			if inner = strings.Index(e.Context, e.Find); inner < 0 {
				return nil, fmt.Errorf("Место «%s» не найдено в шаблоне.", clip(e.Find, 40))
			}
		}
		found := 0
		for at, k := 0, 0; ; k++ {
			i := strings.Index(text[at:], outer)
			if i < 0 {
				break
			}
			s := at + i
			if e.Occurrence < 0 || e.Occurrence == k {
				byPara[e.Para] = append(byPara[e.Para], span{start: s + inner, end: s + inner + len(e.Find), value: e.Value})
				found++
			}
			at = s + len(outer)
		}
		if found == 0 {
			return nil, fmt.Errorf("Место «%s» не найдено в шаблоне.", clip(e.Find, 40))
		}
	}
	// New text for each changed node, by part.
	changed := map[*part]map[*node]string{}
	for n, spans := range byPara {
		p := d.paras[n]
		sort.Slice(spans, func(i, j int) bool { return spans[i].start > spans[j].start })
		for i := 1; i < len(spans); i++ {
			if spans[i].end > spans[i-1].start {
				return nil, fmt.Errorf("Два поля заполняют один и тот же текст в абзаце %d.", n+1)
			}
		}
		texts := make([]string, len(p.nodes))
		offs := make([]int, len(p.nodes))
		at := 0
		for i, nd := range p.nodes {
			texts[i], offs[i] = nd.text, at
			at += len(nd.text)
		}
		touched := map[int]bool{}
		// From the end backwards: a later replacement never moves an earlier one's text.
		for _, s := range spans {
			placed := false
			for i, nd := range p.nodes {
				lo, hi := offs[i], offs[i]+len(nd.text)
				if hi <= s.start || lo >= s.end || lo == hi || nd.fixed {
					continue
				}
				a, b := max(s.start, lo)-lo, min(s.end, hi)-lo
				if !placed {
					texts[i] = texts[i][:a] + s.value + texts[i][b:]
					placed = true
				} else {
					texts[i] = texts[i][:a] + texts[i][b:]
				}
				touched[i] = true
			}
		}
		if !placedAll(spans, p, offs) {
			return nil, fmt.Errorf("Место в абзаце %d состоит только из табуляций и переносов: его нельзя заполнить.", n+1)
		}
		if changed[p.part] == nil {
			changed[p.part] = map[*node]string{}
		}
		for i := range touched {
			changed[p.part][p.nodes[i]] = texts[i]
		}
	}
	out := map[string][]byte{}
	for p, nodes := range changed {
		out[p.name] = rewrite(p.data, nodes)
	}
	return d.write(out)
}

// placedAll tells whether every span has a text node to take its value.
func placedAll(spans []span, p *para, offs []int) bool {
	for _, s := range spans {
		ok := false
		for i, nd := range p.nodes {
			if !nd.fixed && offs[i] < s.end && offs[i]+len(nd.text) > s.start {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// rewrite replaces the content of the changed <w:t> elements, from the end of the part to its
// start, so the byte ranges found by scan stay valid.
func rewrite(data []byte, nodes map[*node]string) []byte {
	list := make([]*node, 0, len(nodes))
	for n := range nodes {
		list = append(list, n)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].tagStart > list[j].tagStart })
	res := append([]byte(nil), data...)
	for _, n := range list {
		tag := string(res[n.tagStart:n.tagEnd])
		if !strings.Contains(tag, "xml:space") {
			// Without it Word drops the spaces at the edges of the new text.
			tag = strings.TrimSuffix(tag, ">") + ` xml:space="preserve">`
		}
		var b bytes.Buffer
		b.Write(res[:n.tagStart])
		b.WriteString(tag)
		b.WriteString(escape(nodes[n]))
		b.Write(res[n.end:])
		res = b.Bytes()
	}
	return res
}

// escape writes text for a <w:t>; a line break closes the text, breaks the line and opens a
// new text in the same run, so it keeps the run's formatting.
func escape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(strings.ReplaceAll(s, "\r\n", "\n")))
	return strings.ReplaceAll(strings.ReplaceAll(b.String(), "&#xA;", "\n"), "\n", `</w:t><w:br/><w:t xml:space="preserve">`)
}

// write builds the new file: changed parts compressed again, every other entry copied raw.
func (d *Doc) write(changed map[string][]byte) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range d.zip.File {
		data, ok := changed[f.Name]
		if !ok {
			if err := w.Copy(f); err != nil {
				return nil, errors.New("Документ не собран.")
			}
			continue
		}
		h := f.FileHeader
		h.Method = zip.Deflate
		h.CompressedSize64, h.UncompressedSize64, h.CRC32 = 0, 0, 0
		fw, err := w.CreateHeader(&h)
		if err != nil {
			return nil, errors.New("Документ не собран.")
		}
		if _, err := fw.Write(data); err != nil {
			return nil, errors.New("Документ не собран.")
		}
	}
	if err := w.Close(); err != nil {
		return nil, errors.New("Документ не собран.")
	}
	return buf.Bytes(), nil
}

// Placeholders are the {{name}} marks the template's author wrote, with their paragraphs.
func (d *Doc) Placeholders() map[string][]int {
	out := map[string][]int{}
	for i, p := range d.paras {
		text := p.text()
		for at := 0; ; {
			s := strings.Index(text[at:], "{{")
			if s < 0 {
				break
			}
			s += at
			e := strings.Index(text[s:], "}}")
			if e < 0 {
				break
			}
			name := strings.TrimSpace(text[s+2 : s+e])
			if name != "" && len(name) <= 80 && !strings.ContainsAny(name, "{}") {
				if l := out[name]; len(l) == 0 || l[len(l)-1] != i {
					out[name] = append(out[name], i)
				}
			}
			at = s + e + 2
		}
	}
	return out
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
