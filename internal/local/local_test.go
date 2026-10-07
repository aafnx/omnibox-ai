package local

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestCommandsReachTheServiceOverTheSocket(t *testing.T) {
	// A short path: unix socket paths are limited to about 100 characters.
	dir, err := os.MkdirTemp("", "ob")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	var started map[string]any
	ln, err := Listen(dir)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	srv := &http.Server{Handler: Handler(Options{
		Status:        func(context.Context) any { return map[string]any{"version": "0.18.0"} },
		Logs:          func(engine string) any { return []map[string]string{{"text": "logs of " + engine}} },
		Action:        func(body map[string]any) (string, error) { started = body; return "op1", nil },
		Operation:     func(id string) (any, bool) { return map[string]any{"state": "done"}, id == "op1" },
		ResetPassword: func() (string, error) { return "", errors.New("Не удалось сохранить пароль.") },
		NewKey:        func() (string, error) { return "obai-1", nil },
	})}
	go srv.Serve(ln)
	defer srv.Close()
	c := Dial(dir)
	var status map[string]any
	if err := c.Do("GET", "/status", nil, &status); err != nil || status["version"] != "0.18.0" {
		t.Fatalf("%v %v", status, err)
	}
	var op struct{ ID string }
	if err := c.Do("POST", "/actions", map[string]any{"action": "stopall"}, &op); err != nil || op.ID != "op1" || started["action"] != "stopall" {
		t.Fatalf("%v %v", op, err)
	}
	if err := c.Do("GET", "/operations/missing", nil, nil); err == nil || !strings.Contains(err.Error(), "не найдена") {
		t.Fatalf("%v", err)
	}
	if err := c.Do("POST", "/password-reset", nil, nil); err == nil || !strings.Contains(err.Error(), "пароль") {
		t.Fatalf("%v", err)
	}
	var key struct{ Key string }
	if err := c.Do("POST", "/api-key", nil, &key); err != nil || key.Key != "obai-1" {
		t.Fatalf("%v %v", key, err)
	}
	if info, err := os.Stat(SocketPath(dir)); err == nil && info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("socket open to others: %v", info.Mode())
	}
	srv.Close()
	if err := Dial(dir).Do("GET", "/status", nil, nil); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("stopped service: %v", err)
	}
}
