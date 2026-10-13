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
	"github.com/aafnx/omnibox-ai/internal/chats"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/docx"
)

// automationFile is a file the panel sends: its name and base64 content.
type automationFile struct {
	Name string `json:"name"`
	Data string `json:"data"`
}

type automationRequest struct {
	Action       string                `json:"action"`
	ID           string                `json:"id"`
	Kind         string                `json:"kind"`
	Name         string                `json:"name"`
	Emoji        string                `json:"emoji"`
	Private      bool                  `json:"private"`
	Model        string                `json:"model"`
	Prompt       string                `json:"prompt"`
	Instructions string                `json:"instructions"`
	Web          bool                  `json:"web"`
	Read         bool                  `json:"read"`
	History      bool                  `json:"history"`
	Schedule     *automations.Schedule `json:"schedule"`
	Vars         map[string]string     `json:"vars"`
	Template     *automationFile       `json:"template"`
	Fields       []automations.Field   `json:"fields"`
	Text         string                `json:"text"`
	Files        []automationFile      `json:"files"`
	Values       map[string]string     `json:"values"`
}

// serveAutomations is the automations' API. A run's data is read from the request and never
// written to disk; its result is kept only in an automation whose history is on.
func (c *chatSide) serveAutomations(run *chatrun.Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			out, err := c.automationList()
			if err != nil {
				replyError(w, err)
				return
			}
			reply(w, http.StatusOK, out)
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

// runSummary is what the list shows of an automation's last result.
type runSummary struct {
	Created time.Time `json:"created"`
	Trigger string    `json:"trigger"`
	Error   string    `json:"error,omitempty"`
	Unseen  int       `json:"unseen"`
}

func (c *chatSide) keepRuns() time.Duration {
	return time.Duration(c.Store.Get().ChatSettings().RetentionHours) * time.Hour
}

func (c *chatSide) automationList() (map[string]any, error) {
	list, err := c.Automations.List()
	if err != nil {
		return nil, err
	}
	last, next := map[string]runSummary{}, map[string]time.Time{}
	now := time.Now()
	for _, a := range list {
		if t, ok := a.Schedule.Next(now); ok {
			next[a.ID] = t
		}
		if !a.History {
			continue
		}
		runs, err := c.Automations.Runs(a.ID, c.keepRuns(), now)
		if err != nil || len(runs) == 0 {
			continue
		}
		s := runSummary{Created: runs[0].Created, Trigger: runs[0].Trigger, Error: runs[0].Error}
		for _, r := range runs {
			if !r.Seen {
				s.Unseen++
			}
		}
		last[a.ID] = s
	}
	return map[string]any{"automations": list, "last": last, "next": next}, nil
}

// fromRequest is an automation as the panel edited it.
func fromRequest(a automations.Automation, in automationRequest) automations.Automation {
	a.Name, a.Emoji, a.Private, a.Model, a.Instructions = in.Name, in.Emoji, in.Private, in.Model, in.Instructions
	a.Prompt, a.Web, a.Read, a.History, a.Schedule, a.Vars = in.Prompt, in.Web, in.Read, in.History, in.Schedule, in.Vars
	if a.Kind == automations.KindFields {
		a.Fields = in.Fields
	}
	return a
}

func (c *chatSide) automationAction(ctx context.Context, run *chatrun.Runner, in automationRequest) (any, error) {
	now := time.Now()
	switch in.Action {
	case "create":
		if err := checkModel(in.Model, in.Private); err != nil {
			return nil, err
		}
		if in.Kind != automations.KindDocument {
			return c.Automations.Save(fromRequest(automations.Automation{Kind: in.Kind}, in), nil, now)
		}
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
		// The author's {{marks}} are fields at once; the model finds the rest on "analyze".
		fields, _ := automations.ParseFields("", d)
		tpl := &automations.Template{Name: clipName(in.Template.Name), Size: int64(len(data)), Fields: fields}
		if err := automations.Verify(tpl, d); err != nil {
			return nil, err
		}
		a := fromRequest(automations.Automation{Kind: automations.KindDocument, Template: tpl}, in)
		return c.Automations.Save(a, data, now)
	case "save":
		a, err := c.Automations.Get(in.ID)
		if err != nil {
			return nil, err
		}
		if err := checkModel(in.Model, in.Private); err != nil {
			return nil, err
		}
		a = fromRequest(a, in)
		if a.Kind == automations.KindDocument && in.Fields != nil {
			_, d, err := c.automation(in.ID)
			if err != nil {
				return nil, err
			}
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
		a, err := c.Automations.Get(in.ID)
		if err != nil {
			return nil, err
		}
		return c.execute(ctx, run, a, modelFor(in.Model, a), in, "manual")
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
	case "runs":
		if _, err := c.Automations.Get(in.ID); err != nil {
			return nil, err
		}
		runs, err := c.Automations.Runs(in.ID, c.keepRuns(), now)
		if err != nil {
			return nil, err
		}
		_ = c.Automations.MarkSeen(in.ID)
		return map[string]any{"runs": runs}, nil
	case "runs-delete":
		if err := c.Automations.DeleteRuns(in.ID); err != nil {
			return nil, err
		}
		return map[string]bool{"deleted": true}, nil
	}
	return nil, errors.New("Неизвестное действие.")
}

// execute runs an automation with the data given and keeps its result when its history is on.
// A scheduled run comes here too, with no data and the saved values of the prompt's names.
func (c *chatSide) execute(ctx context.Context, run *chatrun.Runner, a automations.Automation, key string, in automationRequest, trigger string) (any, error) {
	if a.Kind == automations.KindDocument && len(a.Template.Fields) == 0 {
		return nil, errors.New("В шаблоне нет полей: найдите их в настройках автоматизации.")
	}
	vars := in.Vars
	if trigger == "schedule" {
		vars = a.Vars
	}
	prompt, err := automations.FillVars(a.Prompt, vars)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, errors.New("Выберите модель для автоматизации.")
	}
	model, _, err := run.Model(ctx, key)
	if err != nil {
		return nil, err
	}
	data, files, images, err := automationInputs(in, model.Caps.Vision, model.Name, a.Kind != automations.KindTask)
	if err != nil {
		return nil, err
	}
	task := chatrun.Task{Model: key, Private: a.Private, Session: "panel-auto-" + a.ID, Text: data, Files: files, Media: images, Web: a.Web, Read: a.Read}
	if a.Kind == automations.KindTask {
		task.Prompt = prompt
		if strings.TrimSpace(a.Instructions) != "" {
			task.Prompt += "\n\nУказания владельца: " + a.Instructions
		}
	} else {
		b := a
		b.Prompt = prompt
		task.Prompt = automations.ValuesTask(b)
	}
	msg, err := run.Task(ctx, task)
	if err != nil {
		return nil, err
	}
	res := automations.Run{Automation: a.ID, Created: time.Now().UTC(), Trigger: trigger, Model: key, Input: in.Text, Error: msg.Error, Seen: trigger == "manual"}
	for _, f := range in.Files {
		res.Files = append(res.Files, f.Name)
	}
	if msg.Meta != nil {
		res.Sources, res.CostUSD = msg.Meta.Sources, msg.Meta.CostUSD
	}
	if a.Kind == automations.KindTask {
		res.Text = msg.Text
	} else if msg.Error == "" {
		res.Values = automations.ParseValues(msg.Text, a.FieldList())
	}
	if a.History && !a.Private {
		if saved, err := c.Automations.SaveRun(res); err != nil {
			c.logf("automation: result not kept: %v", err)
		} else {
			res.ID = saved.ID
		}
	}
	return map[string]any{"kind": a.Kind, "run": res}, nil
}

// scheduled is a scheduled run: its result goes to the history, its failure too.
func (c *chatSide) scheduled(run *chatrun.Runner) func(ctx context.Context, a automations.Automation) {
	return func(ctx context.Context, a automations.Automation) {
		if _, err := c.execute(ctx, run, a, a.Model, automationRequest{}, "schedule"); err != nil {
			c.logf("automation %s: scheduled run: %v", a.ID, err)
			if a.History && !a.Private {
				_, _ = c.Automations.SaveRun(automations.Run{Automation: a.ID, Created: time.Now().UTC(), Trigger: "schedule", Model: a.Model, Error: err.Error()})
			}
		}
	}
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
func automationInputs(in automationRequest, vision bool, model string, need bool) (string, []chats.Attachment, []map[string]any, error) {
	if utf8.RuneCountInString(in.Text) > chatrun.MaxText {
		return "", nil, nil, fmt.Errorf("Текст длиннее %d символов: приложите его файлом.", chatrun.MaxText)
	}
	if len(in.Files) > chatrun.MaxFiles {
		return "", nil, nil, fmt.Errorf("Не больше %d файлов за раз.", chatrun.MaxFiles)
	}
	var files []chats.Attachment
	var images []map[string]any
	total := 0
	for _, f := range in.Files {
		data, err := decodeFile(f, attach.MaxFile)
		if err != nil {
			return "", nil, nil, err
		}
		if total += len(data); total > chatrun.MaxFilesBytes {
			return "", nil, nil, errors.New("Файлы за раз: не больше 75 МБ суммарно.")
		}
		switch kind := attach.KindOf(f.Name); kind {
		case attach.Image:
			if !vision {
				return "", nil, nil, fmt.Errorf("%s не видит изображения — «%s» не прочитать. Выберите модель со зрением.", model, f.Name)
			}
			images = append(images, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + attach.MIME(f.Name) + ";base64," + f.Data}})
		case attach.Audio, attach.Video, "":
			return "", nil, nil, fmt.Errorf("«%s»: в автоматизацию можно дать текст, фото, Word, Excel и CSV.", f.Name)
		default:
			text, err := attach.ExtractText(f.Name, data)
			if err != nil {
				return "", nil, nil, err
			}
			files = append(files, chats.Attachment{Name: clipName(f.Name), Kind: kind, Text: text})
		}
	}
	text := strings.TrimSpace(in.Text)
	if need && text == "" && len(files) == 0 && len(images) == 0 {
		return "", nil, nil, errors.New("Дайте данные для заполнения: текст, фото или файл.")
	}
	return text, files, images, nil
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
