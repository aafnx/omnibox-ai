package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/library"
)

func contractDocx() []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fw, _ := w.Create("word/document.xml")
	fw.Write([]byte(`<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
		`<w:p><w:r><w:t>ДОГОВОР № {{Номер}}</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t xml:space="preserve">Покупатель: </w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>____________</w:t></w:r><w:r><w:t>, паспорт ____ ______.</w:t></w:r></w:p>` +
		`</w:body></w:document>`))
	w.Close()
	return buf.Bytes()
}

// The whole way of a document automation: the template is saved with its {{marks}}, the model
// finds the other places, proposes values from the owner's data, and the approved values become
// a Word file that is never written to disk. A private automation refuses a cloud model and its
// requests go through the entrance that keeps no text.
func TestDocumentAutomation(t *testing.T) {
	c, data := chatRig(t)
	run := c.runner(nil)
	var asked []map[string]any
	answers := []string{
		`{"fields":[{"name":"ФИО покупателя","hint":"полностью","places":[{"para":1,"context":"Покупатель: ____________","find":"____________"}]},{"name":"Серия паспорта","places":[{"para":1,"context":"паспорт ____ ","find":"____"}]}]}`,
		`{"values":[{"field":"ФИО покупателя","value":"Петров Пётр Петрович","source":"из сообщения"},{"field":"Номер","value":"15/10","source":"из сообщения"}]}`,
	}
	local := library.Model{Key: "prismml:a.gguf", Source: "local", Engine: "prismml", Name: "Gemma", Alias: "gemma", Running: true}
	run.Model = func(ctx context.Context, key string) (library.Model, int, error) { return local, 32768, nil }
	run.Local = func(context.Context, string, []byte) *http.Response {
		t.Fatal("an automation went the chat's way")
		return nil
	}
	run.Automation = func(ctx context.Context, session string, body []byte) *http.Response {
		var b map[string]any
		json.Unmarshal(body, &b)
		asked = append(asked, b)
		a := answers[0]
		answers = answers[1:]
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": a}}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: " + string(chunk) + "\n\ndata: [DONE]\n\n"))}
	}
	h := c.serveAutomations(run)
	tpl := base64.StdEncoding.EncodeToString(contractDocx())
	w := post(h, "/api/automations", `{"action":"create","kind":"document","name":"Договор","private":true,"model":"cloud:openai/gpt-5","template":{"name":"Договор.docx","data":"`+tpl+`"}}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "только с моделью на ПК") {
		t.Fatalf("a private automation took a cloud model: %d %s", w.Code, w.Body)
	}
	w = post(h, "/api/automations", `{"action":"create","kind":"document","name":"Договор","emoji":"📄","private":true,"template":{"name":"Договор.docx","data":"`+tpl+`"}}`)
	var a struct {
		ID       string `json:"id"`
		Template struct {
			Fields []struct {
				Name string `json:"name"`
			} `json:"fields"`
		} `json:"template"`
	}
	json.Unmarshal(w.Body.Bytes(), &a)
	if w.Code != 200 || len(a.Template.Fields) != 1 || a.Template.Fields[0].Name != "Номер" {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	// The model finds the rest; the owner saves them.
	w = post(h, "/api/automations", `{"action":"analyze","id":"`+a.ID+`","model":"prismml:a.gguf"}`)
	var found struct {
		Fields json.RawMessage `json:"fields"`
	}
	json.Unmarshal(w.Body.Bytes(), &found)
	if w.Code != 200 || !strings.Contains(string(found.Fields), "ФИО покупателя") || !strings.Contains(string(found.Fields), `"name":"Номер"`) {
		t.Fatalf("analyze: %d %s", w.Code, w.Body)
	}
	if tools := asked[0]["tools"]; tools != nil {
		t.Fatal("an automation was offered tools")
	}
	w = post(h, "/api/automations", `{"action":"save","id":"`+a.ID+`","name":"Договор купли-продажи","private":true,"fields":`+string(found.Fields)+`}`)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	// A run proposes values; nothing of it is written to disk.
	before := listFiles(t, data)
	w = post(h, "/api/automations", `{"action":"run","id":"`+a.ID+`","model":"prismml:a.gguf","text":"Покупатель Петров Пётр Петрович, договор 15/10"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"value":"Петров Пётр Петрович"`) || !strings.Contains(w.Body.String(), `"field":"Серия паспорта","value":"","source":"","missing":true`) {
		t.Fatalf("run: %d %s", w.Code, w.Body)
	}
	if w := post(h, "/api/automations", `{"action":"run","id":"`+a.ID+`","model":"prismml:a.gguf","files":[{"name":"паспорт.jpg","data":"AAAA"}]}`); w.Code != 400 || !strings.Contains(w.Body.String(), "не видит изображения") {
		t.Fatalf("a photo for a blind model: %d %s", w.Code, w.Body)
	}
	w = post(h, "/api/automations", `{"action":"build","id":"`+a.ID+`","values":{"ФИО покупателя":"Петров Пётр Петрович","Номер":"15/10","Серия паспорта":""}}`)
	var built struct{ Result, Name string }
	json.Unmarshal(w.Body.Bytes(), &built)
	if w.Code != 200 || built.Result == "" || !strings.HasPrefix(built.Name, "Договор купли-продажи ") {
		t.Fatalf("build: %d %s", w.Code, w.Body)
	}
	if after := listFiles(t, data); after != before {
		t.Fatalf("a run wrote to disk:\n%s\n---\n%s", before, after)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/automations/file?result="+built.Result, nil)
	rec := httptest.NewRecorder()
	c.serveAutomationFile(rec, r)
	z, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if rec.Code != 200 || err != nil || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %d %v", rec.Code, err)
	}
	f, _ := z.File[0].Open()
	doc, _ := io.ReadAll(f)
	if !strings.Contains(string(doc), "ДОГОВОР № 15/10") || !strings.Contains(string(doc), `<w:b/></w:rPr><w:t xml:space="preserve">Петров Пётр Петрович</w:t>`) || !strings.Contains(string(doc), "паспорт ____ ______.") {
		t.Fatalf("document: %s", doc)
	}
	// Deleting the automation deletes its template.
	if w := post(h, "/api/automations", `{"action":"delete","id":"`+a.ID+`"}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if entries, _ := os.ReadDir(filepath.Join(data, "automations", "templates")); len(entries) != 0 {
		t.Fatal("the template stays")
	}
}

func listFiles(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b.WriteString(path + " " + info.ModTime().String() + "\n")
		}
		return nil
	})
	return b.String()
}

// A task of the owner's own: its {{names}} are asked at the run, its result kept in the history
// when that is on; a scheduled run uses the saved values and marks its result unseen. Fields are
// checked after the model read them.
func TestTaskAndFieldsAutomations(t *testing.T) {
	c, _ := chatRig(t)
	run := c.runner(nil)
	answers := []string{}
	var prompts []string
	local := library.Model{Key: "prismml:a.gguf", Source: "local", Engine: "prismml", Name: "Gemma", Alias: "gemma", Running: true, Caps: library.Caps{Vision: true}}
	run.Model = func(ctx context.Context, key string) (library.Model, int, error) { return local, 32768, nil }
	run.Automation = func(ctx context.Context, session string, body []byte) *http.Response {
		var b struct {
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		json.Unmarshal(body, &b)
		last, _ := json.Marshal(b.Messages[len(b.Messages)-1].Content)
		prompts = append(prompts, string(last))
		a := answers[0]
		answers = answers[1:]
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": a}}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: " + string(chunk) + "\n\ndata: [DONE]\n\n"))}
	}
	h := c.serveAutomations(run)
	w := post(h, "/api/automations", `{"action":"create","kind":"task","name":"Сводка","prompt":"Сводка новостей о {{тема}}","history":true,"model":"prismml:a.gguf"}`)
	var a struct{ ID string }
	json.Unmarshal(w.Body.Bytes(), &a)
	if w.Code != 200 || a.ID == "" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := post(h, "/api/automations", `{"action":"run","id":"`+a.ID+`"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "тема") {
		t.Fatalf("a name left empty: %d %s", w.Code, w.Body)
	}
	answers = append(answers, "Главное за неделю: …")
	w = post(h, "/api/automations", `{"action":"run","id":"`+a.ID+`","vars":{"тема":"ИИ"}}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"text":"Главное за неделю: …"`) || !strings.Contains(prompts[0], "Сводка новостей о ИИ") {
		t.Fatalf("run: %d %s %v", w.Code, w.Body, prompts)
	}
	// A scheduled run: saved values, an unseen result in the list until the history is opened.
	w = post(h, "/api/automations", `{"action":"save","id":"`+a.ID+`","name":"Сводка","prompt":"Сводка новостей о {{тема}}","history":true,"model":"prismml:a.gguf","vars":{"тема":"роботах"},"schedule":{"enabled":true,"every":"day","at":"08:00"}}`)
	if w.Code != 200 {
		t.Fatalf("save: %s", w.Body)
	}
	answers = append(answers, "Роботы: …")
	saved, _ := c.Automations.Get(a.ID)
	c.scheduled(run)(context.Background(), saved)
	list, _ := c.automationList()
	if s := list["last"].(map[string]runSummary)[a.ID]; s.Trigger != "schedule" || s.Unseen != 1 || !strings.Contains(prompts[1], "роботах") {
		t.Fatalf("%+v %v", s, prompts)
	}
	if _, ok := list["next"].(map[string]time.Time)[a.ID]; !ok {
		t.Fatal("no next run")
	}
	w = post(h, "/api/automations", `{"action":"runs","id":"`+a.ID+`"}`)
	if !strings.Contains(w.Body.String(), "Роботы: …") || !strings.Contains(w.Body.String(), "Главное за неделю") {
		t.Fatalf("runs: %s", w.Body)
	}
	if list, _ := c.automationList(); list["last"].(map[string]runSummary)[a.ID].Unseen != 0 {
		t.Fatal("opened history still unseen")
	}
	// A private one keeps no history whatever was asked.
	w = post(h, "/api/automations", `{"action":"create","kind":"fields","name":"Паспорт","private":true,"history":true,"prompt":"Распознай паспорт РФ на фото.","fields":[{"name":"Серия","check":"passport_series"},{"name":"Номер","check":"passport_number"}]}`)
	var p struct {
		ID      string
		History bool
	}
	json.Unmarshal(w.Body.Bytes(), &p)
	if w.Code != 200 || p.History {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	answers = append(answers, `{"values":[{"field":"Серия","value":"9204","source":"с фото"},{"field":"Номер","value":"12345","source":"с фото"}]}`)
	w = post(h, "/api/automations", `{"action":"run","id":"`+p.ID+`","model":"prismml:a.gguf","files":[{"name":"паспорт.jpg","data":"AAAA"}]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"warning":"Номер паспорта — 6 цифр."`) || strings.Contains(w.Body.String(), `"field":"Серия","value":"9204","source":"с фото","missing":false,"warning"`) {
		t.Fatalf("fields: %d %s", w.Code, w.Body)
	}
	if runs, _ := c.Automations.Runs(p.ID, 0, time.Now()); len(runs) != 0 {
		t.Fatal("a private run was kept")
	}
	// Steps: the second reads what the first gave; a cloud model in a step of a private one is refused.
	steps := `"steps":[{"kind":"fields","name":"Паспорт","fields":[{"name":"Серия"}]},{"kind":"task","name":"Письмо","prompt":"Напиши письмо {{кому}} с серией паспорта"%s}]`
	if w := post(h, "/api/automations", `{"action":"create","name":"Письмо","private":true,`+fmt.Sprintf(steps, `,"model":"cloud:o/m"`)+`}`); w.Code != 400 {
		t.Fatalf("cloud step in a private automation: %d %s", w.Code, w.Body)
	}
	w = post(h, "/api/automations", `{"action":"create","name":"Письмо","private":true,"model":"prismml:a.gguf",`+fmt.Sprintf(steps, "")+`}`)
	var m struct {
		ID, Kind string
		Steps    []struct{ Kind string }
	}
	json.Unmarshal(w.Body.Bytes(), &m)
	if w.Code != 200 || m.Kind != "task" || len(m.Steps) != 2 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	answers = append(answers, `{"values":[{"field":"Серия","value":"9204"}]}`, "Уважаемый Иван, серия 9204.")
	prompts = nil
	w = post(h, "/api/automations", `{"action":"run","id":"`+m.ID+`","text":"Паспорт 9204 123456","vars":{"кому":"Ивану"}}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"text":"Уважаемый Иван, серия 9204."`) || !strings.Contains(w.Body.String(), `"name":"Паспорт","kind":"fields"`) {
		t.Fatalf("steps: %d %s", w.Code, w.Body)
	}
	if len(prompts) != 2 || !strings.Contains(prompts[1], "Результаты предыдущих шагов") || !strings.Contains(prompts[1], "Серия: 9204") || !strings.Contains(prompts[1], "Ивану") || !strings.Contains(prompts[1], "Паспорт 9204 123456") {
		t.Fatalf("%v", prompts)
	}
	// A step that fails ends the run with its name.
	answers = append(answers, `{"values":[{"field":"Серия","value":"9204"}]}`)
	run.Automation = func(ctx context.Context, session string, body []byte) *http.Response {
		if len(answers) == 0 {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"boom"}}`))}
		}
		a := answers[0]
		answers = answers[1:]
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": a}}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: " + string(chunk) + "\n\ndata: [DONE]\n\n"))}
	}
	w = post(h, "/api/automations", `{"action":"run","id":"`+m.ID+`","text":"Паспорт 9204","vars":{"кому":"Ивану"}}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"error":"Письмо: `) {
		t.Fatalf("failed step: %d %s", w.Code, w.Body)
	}
}
