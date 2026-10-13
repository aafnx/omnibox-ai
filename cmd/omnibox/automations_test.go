package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	w := post(h, "/api/automations", `{"action":"create","name":"Договор","private":true,"model":"cloud:openai/gpt-5","template":{"name":"Договор.docx","data":"`+tpl+`"}}`)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "только с моделью на ПК") {
		t.Fatalf("a private automation took a cloud model: %d %s", w.Code, w.Body)
	}
	w = post(h, "/api/automations", `{"action":"create","name":"Договор","emoji":"📄","private":true,"template":{"name":"Договор.docx","data":"`+tpl+`"}}`)
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
