// Package attach turns files attached in the panel's chat into what a model reads: text files,
// Word documents and Excel tables become text; images, audio and video stay as data for models
// that understand them. Nothing is written to disk.
package attach

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Kinds of attachments; each but the text ones needs a capability of the model.
const (
	Image = "image"
	Video = "video"
	Audio = "audio"
	Text  = "text"
	Word  = "word"
	Excel = "excel"
)

// MaxFile is the largest attachment; MaxText bounds the text taken from one file.
const (
	MaxFile = 25 << 20
	MaxText = 400_000
)

var extensions = map[string]string{
	"png": Image, "jpg": Image, "jpeg": Image, "webp": Image, "gif": Image,
	"mp4": Video, "mov": Video, "webm": Video, "mkv": Video, "avi": Video,
	"mp3": Audio, "wav": Audio, "m4a": Audio, "ogg": Audio, "flac": Audio, "opus": Audio,
	"docx": Word, "xlsx": Excel, "csv": Excel,
	"txt": Text, "md": Text, "json": Text, "log": Text, "yaml": Text, "yml": Text, "xml": Text, "html": Text,
	"go": Text, "py": Text, "js": Text, "ts": Text, "ps1": Text, "sh": Text, "ini": Text, "conf": Text,
}

// KindOf returns the kind of a file by its name, or "" for an unsupported file.
func KindOf(name string) string {
	return extensions[strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))]
}

// Needs is the model capability a kind requires: vision, video, audio, or "" for text.
func Needs(kind string) string {
	switch kind {
	case Image:
		return "vision"
	case Video, Audio:
		return kind
	}
	return ""
}

// MIME is the media type sent to the model for binary kinds.
func MIME(name string) string {
	switch strings.ToLower(strings.TrimPrefix(path.Ext(name), ".")) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	case "mp4":
		return "video/mp4"
	case "mov":
		return "video/quicktime"
	case "webm":
		return "video/webm"
	case "mkv":
		return "video/x-matroska"
	case "avi":
		return "video/x-msvideo"
	case "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "m4a":
		return "audio/mp4"
	case "ogg", "opus":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	}
	return "application/octet-stream"
}

// AudioFormat is the input_audio format name of OpenAI-compatible APIs.
func AudioFormat(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	if ext == "m4a" {
		return "m4a"
	}
	return ext
}

// ExtractText reads a text, Word or Excel file as text for the model.
func ExtractText(name string, data []byte) (string, error) {
	if len(data) > MaxFile {
		return "", fmt.Errorf("«%s»: больше 25 МБ.", name)
	}
	var text string
	var err error
	switch KindOf(name) {
	case Text:
		text, err = plain(name, data)
	case Word:
		text, err = docx(data)
	case Excel:
		if strings.EqualFold(path.Ext(name), ".csv") {
			text, err = csvText(data)
		} else {
			text, err = xlsx(data)
		}
	default:
		return "", fmt.Errorf("«%s»: это не текстовый файл.", name)
	}
	if err != nil {
		return "", fmt.Errorf("«%s» не прочитан: %v", name, err)
	}
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > MaxText {
		text = string([]rune(text)[:MaxText]) + "\n…(файл обрезан)"
	}
	return text, nil
}

func plain(name string, data []byte) (string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("нужен текст в UTF-8")
	}
	return string(data), nil
}

func csvText(data []byte) (string, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		return "", errors.New("нужен CSV в UTF-8")
	}
	// The separator Excel writes in Russian locales is ";".
	r := csv.NewReader(bytes.NewReader(data))
	if first, _, _ := strings.Cut(string(data), "\n"); strings.Count(first, ";") > strings.Count(first, ",") {
		r.Comma = ';'
	}
	r.FieldsPerRecord, r.LazyQuotes = -1, true
	var b strings.Builder
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errors.New("некорректный CSV")
		}
		b.WriteString(strings.Join(rec, "\t"))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// Archive limits against a hostile file: the entries read and their unpacked sizes.
const (
	maxEntries = 2000
	maxPart    = 64 << 20
)

func openZip(data []byte) (*zip.Reader, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("файл повреждён или это не формат Office Open XML")
	}
	if len(z.File) > maxEntries {
		return nil, errors.New("слишком много частей в файле")
	}
	return z, nil
}

func readPart(z *zip.Reader, name string) ([]byte, bool) {
	for _, f := range z.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, false
			}
			defer rc.Close()
			data, err := io.ReadAll(io.LimitReader(rc, maxPart+1))
			if err != nil || len(data) > maxPart {
				return nil, false
			}
			return data, true
		}
	}
	return nil, false
}

// docx takes the paragraphs of word/document.xml: text runs, tabs and breaks; tables become
// lines with cells separated by tabs.
func docx(data []byte) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	doc, ok := readPart(z, "word/document.xml")
	if !ok {
		return "", errors.New("в файле нет текста документа")
	}
	dec := xml.NewDecoder(bytes.NewReader(doc))
	var b strings.Builder
	inText, cellOpen := false, false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errors.New("документ повреждён")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteByte('\t')
			case "br", "cr":
				b.WriteByte('\n')
			case "tc":
				if cellOpen {
					b.WriteByte('\t')
				}
				cellOpen = true
			case "tr":
				cellOpen = false
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if !cellOpen {
					b.WriteByte('\n')
				} else {
					b.WriteByte(' ')
				}
			case "tr":
				b.WriteByte('\n')
				cellOpen = false
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		}
	}
	return b.String(), nil
}

// xlsx writes every sheet as lines of tab-separated cells, shared strings resolved.
func xlsx(data []byte) (string, error) {
	z, err := openZip(data)
	if err != nil {
		return "", err
	}
	var shared []string
	if s, ok := readPart(z, "xl/sharedStrings.xml"); ok {
		var sst struct {
			SI []struct {
				T string `xml:"t"`
				R []struct {
					T string `xml:"t"`
				} `xml:"r"`
			} `xml:"si"`
		}
		if xml.Unmarshal(s, &sst) != nil {
			return "", errors.New("таблица повреждена")
		}
		for _, si := range sst.SI {
			t := si.T
			for _, r := range si.R {
				t += r.T
			}
			shared = append(shared, t)
		}
	}
	names := map[string]string{}
	if wb, ok := readPart(z, "xl/workbook.xml"); ok {
		var w struct {
			Sheets []struct {
				Name string `xml:"name,attr"`
				ID   string `xml:"sheetId,attr"`
			} `xml:"sheets>sheet"`
		}
		if xml.Unmarshal(wb, &w) == nil {
			for i, s := range w.Sheets {
				names["xl/worksheets/sheet"+strconv.Itoa(i+1)+".xml"] = s.Name
			}
		}
	}
	var sheets []string
	for _, f := range z.File {
		if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
			sheets = append(sheets, f.Name)
		}
	}
	sort.Slice(sheets, func(i, j int) bool { return sheetNumber(sheets[i]) < sheetNumber(sheets[j]) })
	var b strings.Builder
	for _, name := range sheets {
		part, ok := readPart(z, name)
		if !ok {
			continue
		}
		var ws struct {
			Rows []struct {
				Cells []struct {
					Ref    string `xml:"r,attr"`
					Type   string `xml:"t,attr"`
					Value  string `xml:"v"`
					Inline string `xml:"is>t"`
				} `xml:"c"`
			} `xml:"sheetData>row"`
		}
		if xml.Unmarshal(part, &ws) != nil {
			return "", errors.New("таблица повреждена")
		}
		title := names[name]
		if title == "" {
			title = strings.TrimSuffix(path.Base(name), ".xml")
		}
		fmt.Fprintf(&b, "## Лист «%s»\n", title)
		for _, row := range ws.Rows {
			var cells []string
			col := 0
			for _, c := range row.Cells {
				// Empty cells are skipped in the file; keep the columns aligned.
				if n := column(c.Ref); n > col {
					for ; col < n; col++ {
						cells = append(cells, "")
					}
				}
				v := c.Value
				switch c.Type {
				case "s":
					if i, err := strconv.Atoi(v); err == nil && i >= 0 && i < len(shared) {
						v = shared[i]
					}
				case "inlineStr":
					v = c.Inline
				}
				cells = append(cells, v)
				col++
			}
			b.WriteString(strings.TrimRight(strings.Join(cells, "\t"), "\t"))
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func sheetNumber(name string) int {
	n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "xl/worksheets/sheet"), ".xml"))
	return n
}

// column is the zero-based column of a cell reference such as C7.
func column(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A'+1)
	}
	return n - 1
}
