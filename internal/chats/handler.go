package chats

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"

	"github.com/aafnx/omnibox-ai/internal/attach"
)

// maxBody bounds a request; one that adds a document to a project carries the file in base64.
const (
	maxBody        = 1 << 20
	maxProjectBody = attach.MaxFile/3*4 + 1<<20
)

// Handler serves the panel's chat and assistant routes. The panel checks the session, CSRF and
// Origin before calling it.
type Handler struct {
	Chats      *Store
	Assistants *Assistants
	Projects   *Projects
	// KeepHours is the chat retention setting now; nil means keep forever.
	KeepHours func() int
	// Logf records failures of the system; their details are not sent to the browser.
	Logf func(format string, args ...any)
}

// Fields each POST action accepts, besides "action".
var (
	chatActions = map[string][]string{
		"create":         {"title", "assistant_id", "model", "project_id", "temporary"},
		"project":        {"id", "project_id"},
		"rename":         {"id", "title"},
		"pin":            {"id"},
		"unpin":          {"id"},
		"delete":         {"id"},
		"delete-all":     {"keep_pinned"},
		"delete-message": {"id", "message_id"},
		"follow-ups":     {"id", "mode"},
		"branch":         {"id", "message_id", "index"},
	}
	assistantActions = map[string][]string{
		"save":    {"id", "emoji", "name", "description", "instructions", "model", "style", "web_search"},
		"delete":  {"id"},
		"default": {"id"},
	}
	projectActions = map[string][]string{
		"save":        {"id", "emoji", "name", "instructions"},
		"delete":      {"id"},
		"file-add":    {"id", "name", "data"},
		"file-remove": {"id", "file_id"},
	}
)

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/chats":
		switch r.Method {
		case http.MethodGet:
			h.getChats(w, r)
		case http.MethodPost:
			h.postChats(w, r)
		default:
			methodNotAllowed(w)
		}
	case "/api/assistants":
		switch r.Method {
		case http.MethodGet:
			h.getAssistants(w, r)
		case http.MethodPost:
			h.postAssistants(w, r)
		default:
			methodNotAllowed(w)
		}
	case "/api/projects":
		switch r.Method {
		case http.MethodGet:
			h.getProjects(w, r)
		case http.MethodPost:
			h.postProjects(w, r)
		default:
			methodNotAllowed(w)
		}
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Не найдено."})
	}
}

func (h *Handler) getChats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k, v := range q {
		if (k != "id" && k != "q" && k != "project") || len(v) != 1 {
			h.fail(w, errorf("Неизвестный параметр запроса %q.", k))
			return
		}
	}
	switch {
	case q.Has("id") && len(q) > 1, q.Has("project") && !q.Has("q"):
		h.fail(w, errorf("Укажите либо id, либо q (и, по желанию, project)."))
	case q.Has("id"):
		c, err := h.Chats.Get(q.Get("id"))
		h.reply(w, c, err)
	case q.Has("q"):
		hits, err := h.Chats.Search(q.Get("q"), q.Get("project"))
		h.reply(w, map[string]any{"hits": hits}, err)
	default:
		list, err := h.Chats.List()
		h.reply(w, map[string]any{"chats": list, "keep_hours": h.keepHours()}, err)
	}
}

func (h *Handler) postChats(w http.ResponseWriter, r *http.Request) {
	action, f, err := readBody(w, r, chatActions)
	if err != nil {
		h.fail(w, err)
		return
	}
	var id, title, assistant, model, msg, mode, project string
	var keepPinned, temporary bool
	var index int
	if err := f.get("id", &id, "title", &title, "assistant_id", &assistant, "model", &model,
		"message_id", &msg, "keep_pinned", &keepPinned, "mode", &mode, "index", &index,
		"project_id", &project, "temporary", &temporary); err != nil {
		h.fail(w, err)
		return
	}
	switch action {
	case "create":
		// Without assistant_id the chat starts with the default assistant; "" means none.
		if _, given := f["assistant_id"]; !given && h.Assistants != nil {
			l, err := h.Assistants.Load()
			if err != nil {
				h.fail(w, err)
				return
			}
			assistant = l.Default
		} else if assistant != "" && h.Assistants != nil {
			l, err := h.Assistants.Load()
			if err != nil {
				h.fail(w, err)
				return
			}
			if _, ok := l.Find(assistant); !ok {
				h.fail(w, errAssistantNotFound)
				return
			}
		}
		if err := h.checkProject(project); err != nil {
			h.fail(w, err)
			return
		}
		c, err := h.Chats.Start(New{Title: title, KeepHours: h.keepHours(), AssistantID: assistant, Model: model,
			ProjectID: project, Temporary: temporary})
		h.reply(w, c, err)
	case "delete":
		h.reply(w, map[string]any{"ok": true}, h.Chats.Delete(id))
	case "delete-all":
		n, err := h.Chats.DeleteAll(keepPinned)
		h.reply(w, map[string]any{"deleted": n}, err)
	case "branch":
		// Another version of the conversation: the whole chat, since its messages change.
		c, err := h.Chats.SwitchBranch(id, msg, index)
		h.reply(w, c, err)
	default:
		// The remaining actions change one chat and answer with its new summary.
		var c *Chat
		switch action {
		case "rename":
			c, err = h.Chats.Rename(id, title)
		case "pin", "unpin":
			c, err = h.Chats.SetPinned(id, action == "pin")
		case "delete-message":
			c, err = h.Chats.DeleteMessage(id, msg)
		case "project":
			// "" takes the chat out of its project.
			if err = h.checkProject(project); err == nil {
				c, err = h.Chats.SetProject(id, project)
			}
		case "follow-ups":
			// on or off for this chat; auto follows the setting.
			on, off := true, false
			switch mode {
			case "on":
				c, err = h.Chats.SetFollowUps(id, &on)
			case "off":
				c, err = h.Chats.SetFollowUps(id, &off)
			case "auto":
				c, err = h.Chats.SetFollowUps(id, nil)
			default:
				err = errorf("Предложения в чате: on, off или auto.")
			}
		}
		if err != nil {
			h.fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, c.Summary())
	}
}

func (h *Handler) getAssistants(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) > 0 {
		h.fail(w, errorf("У запроса нет параметров."))
		return
	}
	l, err := h.Assistants.Load()
	h.reply(w, l, err)
}

func (h *Handler) postAssistants(w http.ResponseWriter, r *http.Request) {
	action, f, err := readBody(w, r, assistantActions)
	if err != nil {
		h.fail(w, err)
		return
	}
	var a Assistant
	if err := f.get("id", &a.ID, "emoji", &a.Emoji, "name", &a.Name, "description", &a.Description,
		"instructions", &a.Instructions, "model", &a.Model, "style", &a.Style, "web_search", &a.WebSearch); err != nil {
		h.fail(w, err)
		return
	}
	var saved Assistant
	switch action {
	case "save":
		saved, err = h.Assistants.Save(a)
	case "delete":
		err = h.Assistants.Delete(a.ID)
	case "default":
		err = h.Assistants.SetDefault(a.ID)
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	l, err := h.Assistants.Load()
	if err != nil {
		h.fail(w, err)
		return
	}
	out := map[string]any{"assistants": l.Assistants, "default": l.Default}
	if action == "save" {
		out["assistant"] = saved
	}
	writeJSON(w, http.StatusOK, out)
}

// checkProject refuses a project that does not exist; "" is no project.
func (h *Handler) checkProject(id string) error {
	if id == "" {
		return nil
	}
	if h.Projects == nil {
		return errProjectNotFound
	}
	_, err := h.Projects.Get(id)
	return err
}

func (h *Handler) getProjects(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) > 0 {
		h.fail(w, errorf("У запроса нет параметров."))
		return
	}
	list, err := h.Projects.List()
	h.reply(w, map[string]any{"projects": list}, err)
}

func (h *Handler) postProjects(w http.ResponseWriter, r *http.Request) {
	action, f, err := readBodyN(w, r, projectActions, maxProjectBody)
	if err != nil {
		h.fail(w, err)
		return
	}
	var x Project
	var name, data, fileID string
	if err := f.get("id", &x.ID, "emoji", &x.Emoji, "name", &x.Name, "instructions", &x.Instructions,
		"data", &data, "file_id", &fileID); err != nil {
		h.fail(w, err)
		return
	}
	out := map[string]any{}
	switch action {
	case "save":
		var saved Project
		saved, err = h.Projects.Save(x)
		out["project"] = saved
	case "delete":
		// The chats of the project stay, outside any project.
		if err = h.Projects.Delete(x.ID); err == nil {
			err = h.Chats.LeaveProject(x.ID)
		}
	case "file-add":
		name = x.Name
		var file ProjectFile
		file, err = h.addFile(x.ID, name, data)
		out["file"] = file
	case "file-remove":
		err = h.Projects.RemoveFile(x.ID, fileID)
	}
	if err != nil {
		h.fail(w, err)
		return
	}
	list, err := h.Projects.List()
	out["projects"] = list
	h.reply(w, out, err)
}

// addFile keeps the text of a document; the file itself is not written to disk.
func (h *Handler) addFile(id, name, data string) (ProjectFile, error) {
	kind := attach.KindOf(name)
	if kind == "" || attach.Needs(kind) != "" {
		return ProjectFile{}, errorf("«%s»: в проект добавляются документы — PDF, Word, Excel, CSV или текст.", name)
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return ProjectFile{}, errorf("«%s»: файл передан с ошибкой.", name)
	}
	text, err := attach.ExtractText(name, raw)
	if err != nil {
		return ProjectFile{}, errorf("%v", err)
	}
	return h.Projects.AddFile(id, name, kind, int64(len(raw)), text)
}

func (h *Handler) keepHours() int {
	if h.KeepHours == nil {
		return 0
	}
	return max(h.KeepHours(), 0)
}

func (h *Handler) reply(w http.ResponseWriter, value any, err error) {
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

// fail answers a refused request: not found is 404, a check the owner can fix is 400 with its
// message, and a failure of the system is 500 with a general message, since details may name
// paths; they go to the log.
func (h *Handler) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
	case isInput(err):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
	default:
		if h.Logf != nil {
			h.Logf("chats: %v", err)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "Не удалось прочитать или сохранить чаты. Подробности записаны в журнал панели."})
	}
}

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, POST")
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "Метод не поддерживается."})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value)
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(bytes.TrimRight(buf.Bytes(), "\n"))
}

// fields is a request body: a JSON object whose keys were checked against its action.
type fields map[string]json.RawMessage

// readBody reads a JSON object of up to 1 MiB and checks that its action is known and that it
// has no keys the action does not take.
func readBody(w http.ResponseWriter, r *http.Request, actions map[string][]string) (string, fields, error) {
	return readBodyN(w, r, actions, maxBody)
}

func readBodyN(w http.ResponseWriter, r *http.Request, actions map[string][]string, limit int64) (string, fields, error) {
	if t, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || t != "application/json" {
		return "", nil, errorf("Нужен JSON-запрос.")
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return "", nil, errorf("Запрос больше %d МБ.", limit>>20)
		}
		return "", nil, errorf("Неполный запрос.")
	}
	var f fields
	if err := json.Unmarshal(data, &f); err != nil || f == nil {
		return "", nil, errorf("Нужен JSON-объект.")
	}
	var action string
	if err := f.get("action", &action); err != nil {
		return "", nil, err
	}
	allowed, ok := actions[action]
	if action == "" {
		return "", nil, errorf("Укажите действие (action).")
	}
	if !ok {
		return "", nil, errorf("Неизвестное действие %q.", action)
	}
	for k := range f {
		if k != "action" && !slices.Contains(allowed, k) {
			return "", nil, errorf("Неизвестное поле %q для действия %s.", k, action)
		}
	}
	return action, f, nil
}

// get decodes pairs of key and destination; a missing key leaves its destination unchanged.
func (f fields) get(pairs ...any) error {
	for i := 0; i+1 < len(pairs); i += 2 {
		key := pairs[i].(string)
		raw, ok := f[key]
		if !ok {
			continue
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errorf("Поле %s не может быть null.", key)
		}
		if err := json.Unmarshal(raw, pairs[i+1]); err != nil {
			return errorf("Неверный тип поля %s.", key)
		}
	}
	return nil
}
