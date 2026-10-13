package docx

import (
	"archive/zip"
	"bytes"
	"io"
	"regexp"
	"strings"
	"testing"
)

// A contract the way Word writes it: a placeholder split over runs with different formatting,
// a blank line to fill, a table, a header and a text box with its old-Word copy.
const body = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"><w:body>` +
	`<w:p><w:pPr><w:jc w:val="center"/></w:pPr><w:r><w:rPr><w:b/></w:rPr><w:t>ДОГОВОР № </w:t></w:r><w:r><w:rPr><w:b/><w:color w:val="FF0000"/></w:rPr><w:t>{{Но</w:t></w:r><w:r><w:rPr><w:i/></w:rPr><w:t>мер}}</w:t></w:r></w:p>` +
	`<w:p><w:r><w:t xml:space="preserve">Покупатель: </w:t></w:r><w:r><w:rPr><w:u w:val="single"/></w:rPr><w:t>____________</w:t></w:r><w:r><w:t>, далее «Покупатель».</w:t></w:r></w:p>` +
	`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>Цена</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:rPr><w:sz w:val="28"/></w:rPr><w:t>0 руб.</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` +
	`<w:p><w:r><w:t>Подпись: ____________ / ____________</w:t></w:r><w:r><w:delText>старое</w:delText></w:r><w:r><w:instrText> PAGE </w:instrText></w:r></w:p>` +
	`<w:p><w:r><mc:AlternateContent><mc:Choice><w:drawing><w:txbxContent><w:p><w:r><w:t>Печать</w:t></w:r></w:p></w:txbxContent></w:drawing></mc:Choice><mc:Fallback><w:pict><w:txbxContent><w:p><w:r><w:t>Печать</w:t></w:r></w:p></w:txbxContent></w:pict></mc:Fallback></mc:AlternateContent></w:r></w:p>` +
	`<w:p/><w:sectPr/></w:body></w:document>`

const header = `<?xml version="1.0" encoding="UTF-8"?><w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:p><w:r><w:t>г. Казань, {{Дата}}</w:t></w:r></w:p></w:hdr>`

func template(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range []struct{ name, data string }{
		{"[Content_Types].xml", `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>`},
		{"word/document.xml", body},
		{"word/header1.xml", header},
		{"word/styles.xml", `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:style w:styleId="Normal"/></w:styles>`},
		{"word/media/image1.png", "\x89PNG fake"},
	} {
		fw, _ := w.Create(f.name)
		fw.Write([]byte(f.data))
	}
	w.Close()
	return buf.Bytes()
}

func files(t *testing.T, data []byte) map[string]string {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range z.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestParagraphs(t *testing.T) {
	d, err := Open(template(t))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range d.Paragraphs() {
		got = append(got, p.Text+" | "+p.Where)
	}
	want := []string{
		"ДОГОВОР № {{Номер}} | ",
		"Покупатель: ____________, далее «Покупатель». | ",
		"Цена | таблица 1, строка 1, столбец 1",
		"0 руб. | таблица 1, строка 1, столбец 2",
		"Подпись: ____________ / ____________ | ",
		"Печать | ",
		"г. Казань, {{Дата}} | верхний колонтитул",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("paragraphs:\n%s", strings.Join(got, "\n"))
	}
	ph := d.Placeholders()
	if len(ph["Номер"]) != 1 || len(ph["Дата"]) != 1 {
		t.Fatalf("placeholders %v", ph)
	}
}

// The owner's requirement: only the text of the places changes. Every other file of the template
// is byte for byte the same, and in the changed XML everything outside the text is the same.
func TestFillKeepsTheFormatting(t *testing.T) {
	src := template(t)
	d, _ := Open(src)
	n := func(text string) int {
		for _, p := range d.Paragraphs() {
			if strings.HasPrefix(p.Text, text) {
				return p.N
			}
		}
		t.Fatalf("no paragraph %q", text)
		return -1
	}
	out, err := d.Fill([]Edit{
		{Para: n("ДОГОВОР"), Find: "{{Номер}}", Value: "15/10 & <А>"},
		{Para: n("Покупатель"), Find: "____________", Value: " Иванов Иван Иванович "},
		{Para: n("0 руб."), Find: "0 руб.", Value: "120 000 руб."},
		{Para: n("Подпись"), Find: "____________", Occurrence: 1, Value: "И. И. Иванов"},
		{Para: n("г. Казань"), Find: "{{Дата}}", Value: "12 октября 2026 г.\nвторник"},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, after := files(t, src), files(t, out)
	for _, name := range []string{"[Content_Types].xml", "word/styles.xml", "word/media/image1.png"} {
		if before[name] != after[name] {
			t.Fatalf("%s changed", name)
		}
	}
	doc := after["word/document.xml"]
	// The placeholder took the first run with its formatting; the rest of it is gone, the runs stay.
	if !strings.Contains(doc, `<w:rPr><w:b/><w:color w:val="FF0000"/></w:rPr><w:t xml:space="preserve">15/10 &amp; &lt;А&gt;</w:t></w:r><w:r><w:rPr><w:i/></w:rPr><w:t xml:space="preserve"></w:t>`) {
		t.Fatalf("number: %s", doc)
	}
	if !strings.Contains(doc, `<w:u w:val="single"/></w:rPr><w:t xml:space="preserve"> Иванов Иван Иванович </w:t>`) ||
		!strings.Contains(doc, `<w:sz w:val="28"/></w:rPr><w:t xml:space="preserve">120 000 руб.</w:t>`) ||
		!strings.Contains(doc, `Подпись: ____________ / И. И. Иванов</w:t>`) ||
		!strings.Contains(doc, `<w:delText>старое</w:delText>`) || !strings.Contains(doc, `<w:instrText> PAGE </w:instrText>`) {
		t.Fatalf("document: %s", doc)
	}
	if !strings.Contains(after["word/header1.xml"], `г. Казань, 12 октября 2026 г.</w:t><w:br/><w:t xml:space="preserve">вторник</w:t>`) {
		t.Fatalf("header: %s", after["word/header1.xml"])
	}
	// Outside the <w:t> texts the XML is the same as before.
	text := regexp.MustCompile(`(?s)<w:t( [^>]*)?>.*?</w:t>|<w:br/>`)
	if text.ReplaceAllString(before["word/document.xml"], "T") != text.ReplaceAllString(doc, "T") {
		t.Fatal("markup outside the text changed")
	}
	// The result opens again and reads as filled.
	again, err := Open(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Text(n("ДОГОВОР")); got != "ДОГОВОР № 15/10 & <А>" {
		t.Fatalf("reopened: %q", got)
	}
}

func TestFillRefusesWhatIsNotThere(t *testing.T) {
	d, _ := Open(template(t))
	if _, err := d.Fill([]Edit{{Para: 0, Find: "{{Цена}}", Value: "1"}}); err == nil || !strings.Contains(err.Error(), "не найдено") {
		t.Fatalf("%v", err)
	}
	if _, err := d.Fill([]Edit{{Para: 99, Find: "x", Value: "1"}}); err == nil {
		t.Fatal("a paragraph out of range is refused")
	}
	if _, err := d.Fill([]Edit{{Para: 1, Find: "Покупатель: ___", Value: "a"}, {Para: 1, Find: "____________", Value: "b"}}); err == nil || !strings.Contains(err.Error(), "один и тот же") {
		t.Fatalf("overlap: %v", err)
	}
	// The context tells two blanks apart; only the blank inside it changes.
	out, err := d.Fill([]Edit{{Para: 4, Context: "/ ____________", Find: "____________", Value: "Петров"}})
	if err != nil || !strings.Contains(files(t, out)["word/document.xml"], "Подпись: ____________ / Петров") {
		t.Fatalf("context: %v", err)
	}
	if _, err := d.Fill([]Edit{{Para: 4, Context: "Подпись:", Find: "____", Value: "x"}}); err == nil {
		t.Fatal("a blank outside its context is refused")
	}
	// A tab is part of the text the model reads but stays a tab.
	tab := strings.Replace(body, `<w:t xml:space="preserve">Покупатель: </w:t>`, `<w:t xml:space="preserve">Покупатель:</w:t><w:tab/>`, 1)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fw, _ := zw.Create("word/document.xml")
	fw.Write([]byte(tab))
	zw.Close()
	td, err := Open(buf.Bytes())
	if err != nil || td.Text(1) != "Покупатель:\t____________, далее «Покупатель»." {
		t.Fatalf("tab: %q %v", td.Text(1), err)
	}
	if out, err := td.Fill([]Edit{{Para: 1, Find: "\t____________", Value: "Иванов"}}); err != nil || !strings.Contains(files(t, out)["word/document.xml"], `Покупатель:</w:t><w:tab/></w:r><w:r><w:rPr><w:u w:val="single"/></w:rPr><w:t xml:space="preserve">Иванов</w:t>`) {
		t.Fatalf("tab fill: %v", err)
	}
	// Every match: the same blank twice.
	out, err = d.Fill([]Edit{{Para: 4, Find: "____________", Occurrence: -1, Value: "x"}})
	if err != nil || !strings.Contains(files(t, out)["word/document.xml"], "Подпись: x / x") {
		t.Fatalf("%v", err)
	}
	if _, err := Open([]byte("not a zip")); err == nil {
		t.Fatal("not a docx")
	}
}
