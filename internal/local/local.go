// Package local is the channel between the ai command and the service: HTTP over a unix socket
// in the data directory. Only administrators (root on Linux) can open it, so it needs no
// password; the panel's network rules do not apply to it, and it is never reachable remotely.
package local

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// SocketPath is the socket in the data directory.
func SocketPath(dataDir string) string { return filepath.Join(dataDir, "run", "omnibox.sock") }

// Options are what the service offers to the command line.
type Options struct {
	Status func(ctx context.Context) any
	Logs   func(engine string) any
	// Action starts an operation from structured fields; Operation reports it by id.
	Action    func(body map[string]any) (string, error)
	Operation func(id string) (any, bool)
	// ResetPassword sets a new random panel password, ends every panel session and returns it.
	ResetPassword func() (string, error)
	// NewKey replaces the API key and returns the new one.
	NewKey func() (string, error)
}

// Handler serves the command line's requests.
func Handler(o Options) http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(w http.ResponseWriter, err error) {
		reply(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
	}
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, o.Status(r.Context())) })
	mux.HandleFunc("GET /logs", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, o.Logs(r.URL.Query().Get("engine"))) })
	mux.HandleFunc("POST /actions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(io.LimitReader(r.Body, 16384)).Decode(&body); err != nil {
			fail(w, errors.New("Нужен JSON-объект."))
			return
		}
		id, err := o.Action(body)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 202, map[string]any{"id": id})
	})
	mux.HandleFunc("GET /operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		op, ok := o.Operation(r.PathValue("id"))
		if !ok {
			reply(w, 404, map[string]any{"error": "Операция не найдена."})
			return
		}
		reply(w, 200, op)
	})
	secret := func(fn func() (string, error), field string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			v, err := fn()
			if err != nil {
				fail(w, err)
				return
			}
			reply(w, 200, map[string]any{field: v})
		}
	}
	mux.HandleFunc("POST /password-reset", secret(o.ResetPassword, "password"))
	mux.HandleFunc("POST /api-key", secret(o.NewKey, "key"))
	return mux
}

// Listen opens the socket; a socket left by a crashed service is replaced.
func Listen(dataDir string) (net.Listener, error) {
	path := SocketPath(dataDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// On Linux only root may connect; on Windows the data directory's ACL (SYSTEM and
	// Administrators) decides, which the installer sets.
	_ = os.Chmod(path, 0o600)
	return ln, nil
}

// Client talks to the service.
type Client struct {
	HTTP *http.Client
}

// Dial returns a client for the service of the data directory.
func Dial(dataDir string) *Client {
	path := SocketPath(dataDir)
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}}
}

// ErrNotRunning means the service is not running or the socket is not reachable.
var ErrNotRunning = errors.New("Служба OmniBox AI не отвечает. Запустите её (Windows: sc start omnibox, Linux: systemctl start omnibox) или выполните команду от имени администратора.")

// Do sends a request and decodes the answer; a service error comes back as an error.
func (c *Client) Do(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, "http://omnibox"+path, reader)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return ErrNotRunning
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return errors.New(resp.Status)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
