package attach

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	w.Close()
	return b.Bytes()
}

func TestKinds(t *testing.T) {
	for name, want := range map[string]string{"a.PNG": Image, "b.mp4": Video, "c.m4a": Audio, "d.docx": Word, "e.csv": Excel, "f.go": Text, "g.exe": "", "h": ""} {
		if got := KindOf(name); got != want {
			t.Errorf("%s: %q", name, got)
		}
	}
	if Needs(Image) != "vision" || Needs(Audio) != "audio" || Needs(Word) != "" || MIME("x.jpg") != "image/jpeg" {
		t.Fatal("needs or mime")
	}
}

func TestWordAndExcelBecomeText(t *testing.T) {
	doc := zipOf(t, map[string]string{"word/document.xml": `<w:document xmlns:w="w"><w:body>
		<w:p><w:r><w:t>Привет,</w:t></w:r><w:r><w:t xml:space="preserve"> мир</w:t></w:r></w:p>
		<w:tbl><w:tr><w:tc><w:p><w:r><w:t>A1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>B1</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
		<w:p><w:r><w:t>Конец</w:t></w:r></w:p></w:body></w:document>`})
	got, err := ExtractText("a.docx", doc)
	if err != nil || !strings.Contains(got, "Привет, мир\n") || !strings.Contains(got, "A1 \tB1") || !strings.HasSuffix(got, "Конец") {
		t.Fatalf("%q %v", got, err)
	}
	book := zipOf(t, map[string]string{
		"xl/workbook.xml":      `<workbook><sheets><sheet name="Бюджет" sheetId="1"/></sheets></workbook>`,
		"xl/sharedStrings.xml": `<sst><si><t>Статья</t></si><si><r><t>Су</t></r><r><t>мма</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row><c r="A1" t="s"><v>0</v></c><c r="C1" t="s"><v>1</v></c></row>
			<row><c r="A2" t="inlineStr"><is><t>Еда</t></is></c><c r="C2"><v>120.5</v></c></row></sheetData></worksheet>`})
	got, err = ExtractText("b.xlsx", book)
	if err != nil || got != "## Лист «Бюджет»\nСтатья\t\tСумма\nЕда\t\t120.5" {
		t.Fatalf("%q %v", got, err)
	}
	got, err = ExtractText("c.csv", []byte("\xef\xbb\xbfa;b\n1;\"2;3\"\n"))
	if err != nil || got != "a\tb\n1\t2;3" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestBadFilesAreRefused(t *testing.T) {
	if _, err := ExtractText("a.txt", []byte{0xff, 0xfe, 0}); err == nil {
		t.Fatal("binary text")
	}
	if _, err := ExtractText("a.docx", []byte("not a zip")); err == nil {
		t.Fatal("broken docx")
	}
	if _, err := ExtractText("a.png", []byte("x")); err == nil {
		t.Fatal("an image is not text")
	}
	if _, err := ExtractText("a.txt", make([]byte, MaxFile+1)); err == nil || !strings.Contains(err.Error(), "25 МБ") {
		t.Fatal(err)
	}
	long := strings.Repeat("я", MaxText+5)
	if got, _ := ExtractText("a.md", []byte(long)); !strings.HasSuffix(got, "(файл обрезан)") {
		t.Fatal("long text is not cut")
	}
}
