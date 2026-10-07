package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fakeService answers like the service's local API.
type fakeService struct {
	calls []string
	polls int
	fail  string
}

func (f *fakeService) Do(method, path string, body any, out any) error {
	b, _ := json.Marshal(body)
	f.calls = append(f.calls, method+" "+path+" "+string(b))
	var reply string
	switch {
	case path == "/status":
		reply = `{"version":"0.18.0","computer":"pc","panel":"http://192.168.1.77:8800","api":"https://llm.fnx.red/v1","backends":[{"label":"PrismML","service":"Running","api":"Ready","loaded_model":"ornith-Q4_K_M.gguf","active_context":32768},{"label":"Ollama","service":"Stopped","selected_model":"qwen3:8b","pending":true}]}`
	case path == "/actions":
		reply = `{"id":"op1"}`
	case strings.HasPrefix(path, "/operations/"):
		f.polls++
		reply = `{"state":"running"}`
		if f.polls > 1 {
			reply = `{"state":"done","message":"PrismML: готов."}`
			if f.fail != "" {
				reply = `{"state":"failed","message":"` + f.fail + `"}`
			}
		}
	case strings.HasPrefix(path, "/logs"):
		reply = `[{"stream":"События сервера","text":"line 1"}]`
	case path == "/password-reset":
		reply = `{"password":"new-secret-password"}`
	case path == "/api-key":
		reply = `{"key":"obai-123"}`
	default:
		return errors.New("unexpected " + path)
	}
	return json.Unmarshal([]byte(reply), out)
}

func runCLI(svc Service, args ...string) (string, int) {
	var out, errw bytes.Buffer
	Wait = 0
	code := Run(args, svc, &out, &errw)
	return out.String() + errw.String(), code
}

func TestStatusAndStartWaitForTheOperation(t *testing.T) {
	svc := &fakeService{}
	out, code := runCLI(svc, "status")
	if code != 0 || !strings.Contains(out, "PrismML: работает · API готов · модель ornith-Q4_K_M.gguf · контекст 32768") ||
		!strings.Contains(out, "Ollama: остановлен · выбрана модель qwen3:8b · новые настройки") || !strings.Contains(out, "API для клиентов: https://llm.fnx.red/v1") {
		t.Fatalf("%d %s", code, out)
	}
	out, code = runCLI(svc, "start", "PrismML")
	if code != 0 || !strings.Contains(out, "готов") || svc.calls[1] != `POST /actions {"action":"start","backend":"prismml"}` || svc.polls != 2 {
		t.Fatalf("%d %s %v", code, out, svc.calls)
	}
	failing := &fakeService{fail: "PrismML: модель не выбрана."}
	if out, code := runCLI(failing, "restart", "prismml"); code != 1 || !strings.Contains(out, "модель не выбрана") {
		t.Fatalf("%d %s", code, out)
	}
}

func TestStopWithoutNameStopsAll(t *testing.T) {
	svc := &fakeService{}
	runCLI(svc, "stop")
	if svc.calls[0] != `POST /actions {"action":"stopall"}` {
		t.Fatal(svc.calls)
	}
}

func TestSecretsAndArguments(t *testing.T) {
	svc := &fakeService{}
	if out, code := runCLI(svc, "password", "reset"); code != 0 || !strings.Contains(out, "new-secret-password") {
		t.Fatalf("%d %s", code, out)
	}
	if out, code := runCLI(svc, "key", "new"); code != 0 || !strings.Contains(out, "obai-123") {
		t.Fatalf("%d %s", code, out)
	}
	for _, args := range [][]string{{"start"}, {"start", "vllm"}, {"password"}, {"key", "show"}, {"frobnicate"}, {"logs", "../x"}} {
		if out, code := runCLI(svc, args...); code != 1 || out == "" {
			t.Fatalf("%v: %d %s", args, code, out)
		}
	}
	if out, code := runCLI(svc, "logs", "ollama"); code != 0 || !strings.Contains(out, "line 1") || !strings.HasSuffix(svc.calls[len(svc.calls)-1], "/logs?engine=ollama null") {
		t.Fatalf("%d %s %v", code, out, svc.calls)
	}
	if out, _ := runCLI(svc); !strings.Contains(out, "ai status") {
		t.Fatal("no help")
	}
}
