package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aafnx/omnibox-ai/internal/attach"
	"github.com/aafnx/omnibox-ai/internal/automations"
	"github.com/aafnx/omnibox-ai/internal/chatrun"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/docx"
)

// automationFile is a file the panel sends: its name and base64 content.
type automationFile struct {
	Name string `json:"name"`
	Data string `json:"data"`
}

type automationRequest struct {
	Action       string              `json:"action"`
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Emoji        string              `json:"emoji"`
	Private      bool                `json:"private"`
	Model        string              `json:"model"`
	Instructions string              `json:"instructions"`
	Template     *automationFile     `json:"template"`
	Fields       []automations.Field `json:"fields"`
	Text         string              `json:"text"`
	Files        []automationFile    `json:"files"`
	Values       map[string]string   `json:"values"`
}

// serveAutomations is the automations' API. Nothing a run receives is written to disk: the data
// is read from the request, and the document is kept in memory for an hour.
func (c *chatSide) serveAutomations(run *chatrun.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			list, err := c.Automations.List()
			if err != nil {
				replyError(w, err)
				return
			}
			reply(w, http.StatusOK, map[string]any{"automations": list})
			return
		}
		if r.Method != http.MethodPost {
			reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "Метод запрещён."})
			return
		}
		if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") || r.ContentLength > chatrun.MaxBody {
			replyError(w, errors.New("Нужен JSON-запрос; файлы — до 25 МБ каждый."))
			return
		}
		var in automationRequest
		dec := json.NewDecoder(io.LimitReader(r.Body, chatrun.MaxBody+1))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			replyError(w, errors.New("Некорректный запрос автоматизации."))
			return
		}
		out, err := c.automationAction(r.Context(), run, in)
		if err != nil {
			replyError(w, err)
			return
		}
		reply(w, http.StatusOK, out)
	}
}

func (c *chatSide) automationAction(ctx context.Context, run *chatrun.Runner, in automationRequest) (any, error) {
	now := time.Now()
	switch in.Action {
	case "create":
		if in.Template == nil {
			return nil, errors.New("Загрузите шаблон Word (.docx).")
		}
		data, err := decodeFile(*in.Template, docx.MaxFile)
		if err != nil {
			return nil, err
		}
		if !strings.HasSuffix(strings.ToLower(in.Template.Name), ".docx") {
			return nil, errors.New("Шаблон — файл Word .docx. Файл .doc сохраните в Word как .docx.")
		}
		d, err := docx.Open(data)
		if err != nil {
			return nil, err
		}
		if err := checkModel(in.Model, in.Private); err != nil {
			return nil, err
		}
		// The author's {{marks}} are fields at once; the model finds the rest on "analyze".
		fields, _ := automations.ParseFields("", d)
		tpl := &automations.Template{Name: clipName(in.Template.Name), Size: int64(len(data)), Fields: fields}
		if err := automations.Verify(tpl, d); err != nil {
			return nil, err
		}
		a := automations.Automation{Name: in.Name, Emoji: in.Emoji, Kind: automations.KindDocument, Private: in.Private,
			Model: in.Model, Instructions: in.Instructions, Template: tpl}
		return c.Automations.Save(a, data, now)
	case "save":
		a, d, err := c.automation(in.ID)
		if err != nil {
			return nil, err
		}
		if err := checkModel(in.Model, in.Private); err != nil {
			return nil, err
		}
		a.Name, a.Emoji, a.Private, a.Model, a.Instructions = in.Name, in.Emoji, in.Private, in.Model, in.Instructions
		if in.Fields != nil {
			tpl := *a.Template
			tpl.Fields = in.Fields
			if err := automations.Verify(&tpl, d); err != nil {
				return nil, err
			}
			a.Template = &tpl
		}
		return c.Automations.Save(a, nil, now)
	case "delete":
		if err := c.Automations.Delete(in.ID); err != nil {
			return nil, err
		}
		return map[string]bool{"deleted": true}, nil
	case "analyze":
		a, d, err := c.automation(in.ID)
		if err != nil {
			return nil, err
		}
		text, cost, err := run.Ask(ctx, chatrun.Ask{Model: modelFor(in.Model, a), Private: a.Private, Session: "panel-auto-" + a.ID,
			Messages: automations.FieldsPrompt(d, a.Instructions, now)})
		if err != nil {
			return nil, err
		}
		fields, dropped := automations.ParseFields(text, d)
		tpl := &automations.Template{Fields: fields}
		if err := automations.Verify(tpl, d); err != nil {
			return nil, err
		}
		return map[string]any{"fields": tpl.Fields, "dropped": dropped, "cost_usd": cost}, nil
	case "run":
		a, _, err := c.automation(in.ID)
		if err != nil {
			return nil, err
		}
		if len(a.Template.Fields) == 0 {
			return nil, errors.New("В шаблоне нет полей: найдите их в настройках автоматизации.")
		}
		key := modelFor(in.Model, a)
		model, _, err := run.Model(ctx, key)
		if err != nil {
			return nil, err
		}
		data, images, err := automationInputs(in, model.Caps.Vision, model.Name)
		if err != nil {
			return nil, err
		}
		text, cost, err := run.Ask(ctx, chatrun.Ask{Model: key, Private: a.Private, Session: "panel-auto-" + a.ID,
			Messages: automations.ValuesPrompt(a, data, images, now)})
		if err != nil {
			return nil, err
		}
		return map[string]any{"values": automations.ParseValues(text, a.Template), "cost_usd": cost, "model": key}, nil
	case "build":
		a, d, err := c.automation(in.ID)
		if err != nil {
			return nil, err
		}
		out, err := d.Fill(automations.Edits(a.Template, in.Values))
		if err != nil {
			return nil, err
		}
		name := automations.FileName(a.Name, now)
		return map[string]string{"result": c.Results.Put(name, out), "name": name}, nil
	}
	return nil, errors.New("Неизвестное действие.")
}

// automation is a saved automation with its opened template.
func (c *chatSide) automation(id string) (automations.Automation, *docx.Doc, error) {
	a, err := c.Automations.Get(id)
	if err != nil {
		return a, nil, err
	}
	data, err := c.Automations.Template(a.ID)
	if err != nil {
		return a, nil, err
	}
	d, err := docx.Open(data)
	return a, d, err
}

// modelFor is the run's model: the automation's own, else the one chosen in the chat.
func modelFor(sent string, a automations.Automation) string {
	if a.Model != "" {
		return a.Model
	}
	return sent
}

// checkModel refuses a cloud model for a private automation when it is saved, as Ask does when
// it runs.
func checkModel(key string, private bool) error {
	if key == "" {
		return nil
	}
	kind, _, ok := config.SplitKey(key)
	if !ok {
		return errors.New("Неизвестная модель.")
	}
	if private && config.Remote(kind) {
		return chatrun.ErrNotLocal
	}
	return nil
}

// automationInputs turns the owner's data into the model's: the message and the text of the
// documents, and the pictures for a model that sees.
func automationInputs(in automationRequest, vision bool, model string) (string, []map[string]any, error) {
	if utf8.RuneCountInString(in.Text) > chatrun.MaxText {
		return "", nil, fmt.Errorf("Текст длиннее %d символов: приложите его файлом.", chatrun.MaxText)
	}
	if len(in.Files) > chatrun.MaxFiles {
		return "", nil, fmt.Errorf("Не больше %d файлов за раз.", chatrun.MaxFiles)
	}
	var b strings.Builder
	if t := strings.TrimSpace(in.Text); t != "" {
		b.WriteString("Сообщение пользователя:\n" + t + "\n\n")
	}
	var images []map[string]any
	total := 0
	for _, f := range in.Files {
		data, err := decodeFile(f, attach.MaxFile)
		if err != nil {
			return "", nil, err
		}
		if total += len(data); total > chatrun.MaxFilesBytes {
			return "", nil, errors.New("Файлы за раз: не больше 75 МБ суммарно.")
		}
		switch attach.KindOf(f.Name) {
		case attach.Image:
			if !vision {
				return "", nil, fmt.Errorf("%s не видит изображения — «%s» не прочитать. Выберите модель со зрением.", model, f.Name)
			}
			images = append(images, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + attach.MIME(f.Name) + ";base64," + f.Data}})
		case attach.Audio, attach.Video, "":
			return "", nil, fmt.Errorf("«%s»: в автоматизацию можно дать текст, фото, Word, Excel и CSV.", f.Name)
		default:
			text, err := attach.ExtractText(f.Name, data)
			if err != nil {
				return "", nil, err
			}
			b.WriteString("Файл «" + f.Name + "»:\n" + text + "\n\n")
		}
	}
	if b.Len() == 0 && len(images) == 0 {
		return "", nil, errors.New("Дайте данные для заполнения: текст, фото или файл.")
	}
	return b.String(), images, nil
}

func decodeFile(f automationFile, limit int) ([]byte, error) {
	if base64.StdEncoding.DecodedLen(len(f.Data)) > limit+3 {
		return nil, fmt.Errorf("«%s»: больше 25 МБ.", f.Name)
	}
	data, err := base64.StdEncoding.DecodeString(f.Data)
	if err != nil || len(data) > limit {
		return nil, fmt.Errorf("«%s» не прочитан.", f.Name)
	}
	return data, nil
}

func clipName(name string) string {
	if r := []rune(name); len(r) > 200 {
		return string(r[:200])
	}
	return name
}

// serveAutomationFile hands out a built document or a template, as a file to save.
func (c *chatSide) serveAutomationFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		reply(w, http.StatusMethodNotAllowed, map[string]string{"error": "Метод запрещён."})
		return
	}
	var (
		name string
		data []byte
		ok   bool
	)
	if id := r.URL.Query().Get("result"); id != "" {
		name, data, ok = c.Results.Get(id)
	} else if id := r.URL.Query().Get("template"); id != "" {
		if a, err := c.Automations.Get(id); err == nil {
			if data, err = c.Automations.Template(id); err == nil {
				name, ok = a.Template.Name, true
			}
		}
	}
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{"error": "Файла нет: документ хранится час после сборки — соберите его снова."})
		return
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}
