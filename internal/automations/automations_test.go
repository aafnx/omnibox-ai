package automations

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/docx"
)

const contract = `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
	`<w:p><w:r><w:t>ДОГОВОР № {{Номер}}</w:t></w:r></w:p>` +
	`<w:p><w:r><w:t xml:space="preserve">Покупатель: </w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>Иванов Иван Иванович</w:t></w:r><w:r><w:t>, паспорт серии ____ № ______.</w:t></w:r></w:p>` +
	`<w:p><w:r><w:t>Подпись: ____________ / ____________</w:t></w:r></w:p>` +
	`</w:body></w:document>`

func template(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fw, _ := w.Create("word/document.xml")
	fw.Write([]byte(contract))
	w.Close()
	return buf.Bytes()
}

func open(t *testing.T) *docx.Doc {
	t.Helper()
	d, err := docx.Open(template(t))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// The model's answer is checked against the template: places that are not there are dropped,
// a context the model trimmed differently still finds a unique place, a repeated context is
// filled everywhere, and the author's {{marks}} are fields even when the model forgets them.
func TestParseFields(t *testing.T) {
	d := open(t)
	answer := "Вот поля:\n```json\n" + `{"fields":[
	 {"name":"ФИО покупателя","hint":"полностью, в именительном падеже","places":[{"para":1,"context":"Покупатель: Иванов Иван Иванович,","find":"Иванов Иван Иванович"}]},
	 {"name":"Серия паспорта","type":"passport_series","places":[{"para":1,"context":"серии ____ №","find":"____"}]},
	 {"name":"Номер паспорта","places":[{"para":1,"context":"№ ______","find":"______"}]},
	 {"name":"Подпись","type":"signature","places":[{"para":2,"context":"____________","find":"____________"}]},
	 {"name":"Выдумка","places":[{"para":7,"context":"нет","find":"нет"},{"para":1,"context":"чего нет","find":"нет"}]}
	]}` + "\n```"
	fields, dropped := ParseFields(answer, d)
	names := []string{}
	for _, f := range fields {
		names = append(names, f.Name)
	}
	if strings.Join(names, "|") != "ФИО покупателя|Серия паспорта|Номер паспорта|Подпись|Номер" || dropped != 2 {
		t.Fatalf("fields %v, dropped %d", names, dropped)
	}
	if p := fields[0].Places[0]; p.Context != "Покупатель: Иванов Иван Иванович," || p.All || fields[0].Hint == "" {
		t.Fatalf("%+v", fields[0])
	}
	if fields[1].Check != "passport_series" || fields[3].Check != "" {
		t.Fatalf("types: %q %q", fields[1].Check, fields[3].Check)
	}
	if p := fields[3].Places[0]; !p.All {
		t.Fatal("a context found twice is filled in both places")
	}
	if p := fields[4].Places[0]; p.Para != 0 || p.Find != "{{Номер}}" {
		t.Fatalf("placeholder %+v", p)
	}
	// A text the owner types is found wherever it stands; a text not in the template is nowhere.
	if pl := FindPlaces(d, "____________"); len(pl) != 1 || pl[0].Para != 2 || !pl[0].All {
		t.Fatalf("%+v", pl)
	}
	if pl := FindPlaces(d, "Иванов Иван"); len(pl) != 1 || pl[0].Para != 1 || pl[0].All {
		t.Fatalf("%+v", pl)
	}
	if FindPlaces(d, "нет такого") != nil || FindPlaces(d, "  ") != nil {
		t.Fatal("a text not in the template has no places")
	}
	if !strings.Contains(FieldsPrompt(d, "", time.Now())[0]["content"].(string), `"inn" (ИНН)`) {
		t.Fatal("the model is told the types")
	}
	if f, _ := ParseFields("не JSON", d); len(f) != 1 {
		t.Fatalf("without the model only the marks remain: %+v", f)
	}
}

// The approved values fill the template; an empty value leaves the place as it was.
func TestEditsFillTheTemplate(t *testing.T) {
	d := open(t)
	fields, _ := ParseFields(`{"fields":[{"name":"Серия","places":[{"para":1,"context":"серии ____ №","find":"____"}]},{"name":"Номер паспорта","places":[{"para":1,"context":"№ ______","find":"______"}]},{"name":"ФИО","places":[{"para":1,"context":"Иванов Иван Иванович","find":"Иванов Иван Иванович"}]}]}`, d)
	tpl := &Template{Fields: fields}
	if err := Verify(tpl, d); err != nil || tpl.Fields[0].Places[0].Text == "" {
		t.Fatalf("%v %+v", err, tpl.Fields[0])
	}
	out, err := d.Fill(Edits(tpl, map[string]string{"Серия": "9204", "Номер паспорта": "123456", "ФИО": "  ", "Номер": "15/10"}))
	if err != nil {
		t.Fatal(err)
	}
	z, _ := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	rc, _ := z.File[0].Open()
	xml, _ := io.ReadAll(rc)
	s := string(xml)
	if !strings.Contains(s, "серии 9204 № 123456.") || !strings.Contains(s, "<w:b/></w:rPr><w:t>Иванов Иван Иванович</w:t>") || !strings.Contains(s, "ДОГОВОР № 15/10") {
		t.Fatalf("%s", s)
	}
	bad := &Template{Fields: []Field{{Name: "X", Places: []Place{{Para: 1, Context: "нет такого", Find: "нет"}}}}}
	if err := Verify(bad, d); err == nil {
		t.Fatal("a place not in the template is refused")
	}
}

func TestParseValues(t *testing.T) {
	fields := []Field{{Name: "ФИО"}, {Name: "Серия", Check: "passport_series"}, {Name: "Дата"}}
	vals := ParseValues(`{"values":[{"field":"фио","value":" Петров Пётр ","source":"из паспорта на фото"},{"field":"Серия","value":"92 0"},{"field":"Лишнее","value":"x"}]}`, fields)
	if len(vals) != 3 || vals[0].Value != "Петров Пётр" || vals[0].Field != "ФИО" || vals[0].Missing || vals[1].Warning == "" || !vals[2].Missing {
		t.Fatalf("%+v", vals)
	}
	task := ValuesTask(Automation{Name: "Паспорт", Instructions: "Даты — ДД.ММ.ГГГГ", Steps: []Step{{Kind: KindFields, Fields: fields}}}, 0, "Распознай паспорт РФ на фото.")
	if !strings.HasPrefix(task, "Распознай паспорт РФ на фото.") || !strings.Contains(task, "- «Серия»") || !strings.Contains(task, "Даты — ДД.ММ.ГГГГ") {
		t.Fatal(task)
	}
}

// The store keeps the template next to the list, readable by the service only, and deleting an
// automation deletes its template at once.
func TestStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), Dir)
	s := &Store{Dir: dir}
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	tpl := &Template{Name: "Договор.docx", Fields: []Field{{Name: "ФИО", Places: []Place{{Para: 1, Find: "x"}}}}}
	if _, err := s.Save(Automation{Name: "Договор", Kind: KindDocument, Template: tpl}, nil, now); err == nil {
		t.Fatal("a document automation needs its template")
	}
	a, err := s.Save(Automation{Name: " Договор купли-продажи ", Kind: KindDocument, Private: true, Template: tpl}, template(t), now)
	if err != nil || a.ID == "" || a.Name != "Договор купли-продажи" {
		t.Fatalf("%+v %v", a, err)
	}
	if data, err := s.Template(a.ID); err != nil || !bytes.Equal(data, template(t)) {
		t.Fatal("template not kept")
	}
	if info, err := os.Stat(filepath.Join(dir, "templates", a.ID+".docx")); err != nil || (info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/') {
		t.Fatalf("template mode %v %v", info.Mode(), err)
	}
	a.Name = "Договор"
	if b, err := s.Save(a, nil, now.Add(time.Hour)); err != nil || !b.Created.Equal(now) || !b.Private {
		t.Fatalf("%+v %v", b, err)
	}
	if list, _ := s.List(); len(list) != 1 || list[0].Name != "Договор" {
		t.Fatalf("%+v", list)
	}
	if _, err := s.Save(Automation{Name: "Повтор", Kind: KindDocument, Template: &Template{Fields: []Field{{Name: "А", Places: []Place{{}}}, {Name: "а", Places: []Place{{}}}}}}, template(t), now); err == nil {
		t.Fatal("two fields with one name")
	}
	if err := s.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "templates", a.ID+".docx")); !os.IsNotExist(err) {
		t.Fatal("the template stays after the automation is deleted")
	}
	if _, err := s.Template("../../config"); err == nil {
		t.Fatal("an id is not a path")
	}
}

// Built documents live in memory for an hour, never on disk.
func TestResults(t *testing.T) {
	now := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	r := &Results{Now: func() time.Time { return now }}
	id := r.Put("Договор 2026-10-12.docx", []byte("doc"))
	if name, data, ok := r.Get(id); !ok || name != "Договор 2026-10-12.docx" || string(data) != "doc" {
		t.Fatal("result not kept")
	}
	now = now.Add(ResultTTL + time.Second)
	if _, _, ok := r.Get(id); ok {
		t.Fatal("result outlived its hour")
	}
	for i := 0; i < maxResults+5; i++ {
		r.Put("x", nil)
	}
	if len(r.items) != maxResults {
		t.Fatalf("%d kept", len(r.items))
	}
	if FileName(`Договор: "Ромашка"/1`, now) != "Договор Ромашка 1 2026-10-12.docx" {
		t.Fatal(FileName(`Договор: "Ромашка"/1`, now))
	}
}
