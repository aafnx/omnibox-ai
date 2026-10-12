package chatgpt

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is an app-server that speaks the protocol on pipes.
type fake struct {
	mu       sync.Mutex
	calls    []string
	params   map[string]json.RawMessage
	refusals []string
	// turn is what a turn does: plain (an answer), tool (starts a command), fail (a limit).
	turn       string
	badConfig  bool
	signedIn   bool
	interrupts int
	unsubs     int
	starts     int
}

func newFake() *fake {
	return &fake{params: map[string]json.RawMessage{}, turn: "plain", signedIn: true}
}

func (f *fake) spawn(exe, home, work string) (io.ReadCloser, io.WriteCloser, func(), error) {
	if _, err := os.Stat(filepath.Join(home, "config.toml")); err != nil {
		return nil, nil, nil, err
	}
	f.mu.Lock()
	f.starts++
	f.mu.Unlock()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go f.serve(inR, outW)
	return outR, inW, func() { _ = inW.Close(); _ = outW.Close() }, nil
}

func (f *fake) serve(in io.Reader, out io.WriteCloser) {
	var wmu sync.Mutex
	send := func(v any) {
		data, _ := json.Marshal(v)
		wmu.Lock()
		_, _ = out.Write(append(data, '\n'))
		wmu.Unlock()
	}
	note := func(method string, params any) { send(map[string]any{"method": method, "params": params}) }
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Error  *rpcError       `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Method == "" {
			if m.Error != nil {
				f.mu.Lock()
				f.refusals = append(f.refusals, string(m.ID))
				f.mu.Unlock()
			}
			continue
		}
		f.mu.Lock()
		f.calls = append(f.calls, m.Method)
		f.params[m.Method] = m.Params
		turn, bad, signed := f.turn, f.badConfig, f.signedIn
		f.mu.Unlock()
		ok := func(result any) { send(map[string]any{"id": m.ID, "result": result}) }
		switch m.Method {
		case "initialize":
			if bad {
				note("configWarning", map[string]any{"summary": "Invalid configuration; using defaults.", "details": "config.toml:3:1"})
			}
			ok(map[string]any{"userAgent": "fake/1.0"})
		case "initialized":
		case "account/read":
			if signed {
				ok(map[string]any{"account": map[string]any{"type": "chatgpt", "email": "owner@example.com", "planType": "plus"}, "requiresOpenaiAuth": true})
			} else {
				ok(map[string]any{"account": nil, "requiresOpenaiAuth": true})
			}
		case "account/rateLimits/read":
			if !signed {
				send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32600, "message": "codex account authentication required to read rate limits"}})
				continue
			}
			ok(map[string]any{"ordinaryUsageAllowed": true, "rateLimits": map[string]any{"limitId": "codex", "planType": "plus",
				"primary":   map[string]any{"usedPercent": 42, "windowDurationMins": 300, "resetsAt": 1791820000},
				"secondary": map[string]any{"usedPercent": 7, "windowDurationMins": 10080, "resetsAt": 1792300000},
				"credits":   map[string]any{"hasCredits": true, "unlimited": false, "balance": "12.50"}},
				"rateLimitsByLimitId": map[string]any{"codex": map[string]any{"limitId": "codex"},
					"gpt-6-astra": map[string]any{"limitId": "gpt-6-astra", "limitName": "GPT-6-Astra", "primary": map[string]any{"usedPercent": 90, "windowDurationMins": 300}}}})
		case "account/login/start":
			ok(map[string]any{"type": "chatgptDeviceCode", "loginId": "L1", "verificationUrl": "https://auth.openai.com/codex/device", "userCode": "ABCD-1234"})
			f.mu.Lock()
			f.signedIn = true
			f.mu.Unlock()
			note("account/login/completed", map[string]any{"loginId": "L1", "success": true})
		case "account/logout":
			f.mu.Lock()
			f.signedIn = false
			f.mu.Unlock()
			ok(map[string]any{})
		case "model/list":
			ok(map[string]any{"data": []any{
				map[string]any{"id": "gpt-5.5", "model": "gpt-5.5", "displayName": "GPT-5.5", "hidden": false, "defaultReasoningEffort": "medium", "isDefault": true,
					"supportedReasoningEfforts": []any{map[string]any{"reasoningEffort": "low"}, map[string]any{"reasoningEffort": "high"}, map[string]any{"reasoningEffort": "ultra"}},
					"inputModalities":           []any{"text", "image"}},
				map[string]any{"id": "hidden-one", "model": "hidden-one", "displayName": "Hidden", "hidden": true},
			}, "nextCursor": nil})
		case "thread/start":
			ok(map[string]any{"thread": map[string]any{"id": "T1"}, "model": "gpt-5.5"})
		case "thread/inject_items", "thread/unsubscribe":
			if m.Method == "thread/unsubscribe" {
				f.mu.Lock()
				f.unsubs++
				f.mu.Unlock()
			}
			ok(map[string]any{})
		case "turn/interrupt":
			f.mu.Lock()
			f.interrupts++
			f.mu.Unlock()
			ok(map[string]any{})
			note("turn/completed", map[string]any{"threadId": "T1", "turn": map[string]any{"id": "U1", "status": "interrupted"}})
		case "turn/start":
			ok(map[string]any{"turn": map[string]any{"id": "U1", "status": "inProgress"}})
			ids := map[string]any{"threadId": "T1", "turnId": "U1"}
			with := func(extra map[string]any) map[string]any {
				out := map[string]any{}
				for k, v := range ids {
					out[k] = v
				}
				for k, v := range extra {
					out[k] = v
				}
				return out
			}
			note("turn/started", map[string]any{"threadId": "T1", "turn": map[string]any{"id": "U1"}})
			// Another thread's answer must not leak into this one.
			note("item/agentMessage/delta", map[string]any{"threadId": "T9", "turnId": "U9", "delta": "LEAK"})
			switch turn {
			case "plain":
				note("item/started", with(map[string]any{"item": map[string]any{"type": "reasoning", "id": "r"}}))
				note("item/reasoning/summaryTextDelta", with(map[string]any{"delta": "Думаю."}))
				note("item/started", with(map[string]any{"item": map[string]any{"type": "agentMessage", "id": "a"}}))
				note("item/agentMessage/delta", with(map[string]any{"delta": "При"}))
				note("item/agentMessage/delta", with(map[string]any{"delta": "вет!"}))
				note("thread/tokenUsage/updated", with(map[string]any{"tokenUsage": map[string]any{"last": map[string]any{"inputTokens": 30, "outputTokens": 5, "reasoningOutputTokens": 2}}}))
				note("account/rateLimits/updated", map[string]any{"rateLimits": map[string]any{"limitId": "codex", "primary": map[string]any{"usedPercent": 43, "windowDurationMins": 300}}})
				note("turn/completed", map[string]any{"threadId": "T1", "turn": map[string]any{"id": "U1", "status": "completed"}})
			case "tool":
				// The app-server asks for an approval and starts a command: both are stopped.
				send(map[string]any{"id": "srv-1", "method": "item/commandExecution/requestApproval", "params": with(map[string]any{"command": "rm -rf /"})})
				note("item/started", with(map[string]any{"item": map[string]any{"type": "commandExecution", "id": "c"}}))
			case "fail":
				note("error", with(map[string]any{"error": map[string]any{"message": "stream disconnected"}, "willRetry": true}))
				note("turn/completed", map[string]any{"threadId": "T1", "turn": map[string]any{"id": "U1", "status": "failed", "error": map[string]any{"message": "You've hit your usage limit."}}})
			case "hang":
			}
		default:
			send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "unknown method"}})
		}
	}
	_ = out.Close()
}

func (f *fake) called(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == method {
			n++
		}
	}
	return n
}

func (f *fake) count(n *int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *n
}

func (f *fake) param(method string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out map[string]any
	_ = json.Unmarshal(f.params[method], &out)
	return out
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "codex-app-server.exe"
	}
	return "codex-app-server"
}

// installedService has Codex "installed" and the fake as its app-server.
func installedService(t *testing.T, f *fake) *Service {
	t.Helper()
	dir := t.TempDir()
	app := filepath.Join(dir, "app", "bin")
	if err := os.MkdirAll(app, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, exeName()), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, _ := json.Marshal(installed{Tag: "rust-v1.0.0", Executable: filepath.Join("bin", exeName())})
	if err := os.WriteFile(filepath.Join(dir, "app", manifest), m, 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Service{Dir: dir, Version: "test", Spawn: f.spawn, Logf: t.Logf}
	t.Cleanup(s.Stop)
	return s
}

func chatBody(t *testing.T, stream bool, messages ...map[string]any) []byte {
	t.Helper()
	list := make([]any, len(messages))
	for i, m := range messages {
		list[i] = m
	}
	data, _ := json.Marshal(map[string]any{"model": "chatgpt/gpt-5.5", "stream": stream, "reasoning_effort": "high", "messages": list})
	return data
}

func TestChatStreamsAnAnswerWithoutTools(t *testing.T) {
	f := newFake()
	s := installedService(t, f)
	body := chatBody(t, true,
		map[string]any{"role": "system", "content": "Сегодня понедельник."},
		map[string]any{"role": "user", "content": "Раньше"},
		map[string]any{"role": "assistant", "content": "Ответ раньше"},
		map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Привет"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}}}})
	resp, err := s.Chat(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	text := string(data)
	for _, want := range []string{`"content":"При"`, `"content":"вет!"`, `"reasoning_content":"Думаю."`, `"finish_reason":"stop"`, `"prompt_tokens":30`, "data: [DONE]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("stream has no %s:\n%s", want, text)
		}
	}
	if strings.Contains(text, "LEAK") {
		t.Fatal("another thread's text leaked into the answer")
	}
	start := f.param("thread/start")
	if start["ephemeral"] != true || start["baseInstructions"] != "Сегодня понедельник." || start["sandbox"] != "read-only" {
		t.Fatalf("thread/start = %v", start)
	}
	if env, ok := start["environments"].([]any); !ok || len(env) != 0 {
		t.Fatalf("the thread must have no environment: %v", start["environments"])
	}
	items := f.param("thread/inject_items")["items"].([]any)
	if len(items) != 2 || !strings.Contains(mustJSON(items[1]), "output_text") {
		t.Fatalf("history = %v", items)
	}
	turn := f.param("turn/start")
	if turn["effort"] != "high" || !strings.Contains(mustJSON(turn["input"]), `"type":"image"`) {
		t.Fatalf("turn/start = %v", turn)
	}
	waitFor(t, func() bool { return f.called("thread/unsubscribe") == 1 })
	if l := s.Quick().Limits; l == nil || l.Primary == nil || l.Primary.UsedPercent != 43 {
		t.Fatalf("limits from the turn = %+v", l)
	}
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestChatWholeAnswerWithoutStream(t *testing.T) {
	f := newFake()
	s := installedService(t, f)
	resp, err := s.Chat(context.Background(), chatBody(t, false, map[string]any{"role": "user", "content": "Привет"}))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Model != "chatgpt/gpt-5.5" || out.Choices[0].Message.Content != "Привет!" || out.Usage.Prompt != 30 {
		t.Fatalf("answer = %+v", out)
	}
	if f.param("thread/start")["baseInstructions"] != defaultInstructions || f.called("thread/inject_items") != 0 {
		t.Fatal("a request without system and history still needs instructions and no history")
	}
}

func TestToolsAndApprovalsAreRefused(t *testing.T) {
	f := newFake()
	f.turn = "tool"
	s := installedService(t, f)
	resp, err := s.Chat(context.Background(), chatBody(t, true, map[string]any{"role": "user", "content": "Удали всё"}))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(data), "commandExecution") || strings.Contains(string(data), "[DONE]") {
		t.Fatalf("a tool must end the answer with an error:\n%s", data)
	}
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.interrupts == 1 })
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.refusals) == 1 })
}

func TestLimitFailureAndCancel(t *testing.T) {
	f := newFake()
	f.turn = "fail"
	s := installedService(t, f)
	resp, err := s.Chat(context.Background(), chatBody(t, true, map[string]any{"role": "user", "content": "Ещё"}))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(data), "Достигнут лимит подписки ChatGPT") {
		t.Fatalf("limit error not explained:\n%s", data)
	}
	// A request the client leaves interrupts the turn.
	f.mu.Lock()
	f.turn = "hang"
	f.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	resp, err = s.Chat(ctx, chatBody(t, true, map[string]any{"role": "user", "content": "Долго"}))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, _ = io.ReadAll(resp.Body)
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.interrupts == 1 })
}

func TestChatRefusesBadRequests(t *testing.T) {
	f := newFake()
	s := installedService(t, f)
	cases := map[string][]byte{
		"unknown model":    []byte(`{"model":"chatgpt/gpt-unknown","messages":[{"role":"user","content":"x"}]}`),
		"hidden model":     []byte(`{"model":"hidden-one","messages":[{"role":"user","content":"x"}]}`),
		"last is not user": []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"x"},{"role":"assistant","content":"y"}]}`),
		"audio":            []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{}}]}]}`),
		"file image":       []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"file:///etc/passwd"}}]}]}`),
	}
	for name, body := range cases {
		_, err := s.Chat(context.Background(), body)
		var e *Error
		if !errors.As(err, &e) || e.Status < 400 || e.Status >= 500 {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if f.called("thread/start") != 0 {
		t.Fatal("a refused request must not start a thread")
	}
}

func TestAccountLoginLimitsAndModels(t *testing.T) {
	f := newFake()
	f.signedIn = false
	s := installedService(t, f)
	ctx := context.Background()
	if a, err := s.Account(ctx); err != nil || a != nil {
		t.Fatalf("signed out: %v %v", a, err)
	}
	if _, err := s.Limits(ctx); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("limits signed out: %v", err)
	}
	l, err := s.StartLogin(ctx)
	if err != nil || l.Code != "ABCD-1234" || l.URL != "https://auth.openai.com/codex/device" {
		t.Fatalf("login = %+v %v", l, err)
	}
	waitFor(t, func() bool { q := s.Quick(); return q.Login != nil && q.Login.Done && q.Login.Error == "" })
	a, err := s.Account(ctx)
	if err != nil || a == nil || a.Email != "owner@example.com" || a.Plan != "plus" {
		t.Fatalf("account = %+v %v", a, err)
	}
	lim, err := s.Limits(ctx)
	if err != nil || lim.Plan != "plus" || lim.Primary.UsedPercent != 42 || lim.Primary.Minutes != 300 || lim.Secondary.Minutes != 10080 ||
		lim.Primary.ResetsAt == nil || lim.Credits == nil || lim.Credits.Balance != "12.50" || len(lim.Others) != 1 || lim.Others[0].Name != "GPT-6-Astra" {
		t.Fatalf("limits = %+v %v", lim, err)
	}
	models, err := s.Models(ctx)
	if err != nil || len(models) != 1 || models[0].ID != "gpt-5.5" || !models[0].Images || strings.Join(models[0].Efforts, ",") != "low,high" {
		t.Fatalf("models = %+v %v", models, err)
	}
	if err := s.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if q := s.Quick(); q.Account != nil || q.Limits != nil {
		t.Fatalf("after logout = %+v", q)
	}
	if n := f.count(&f.starts); n != 1 {
		t.Fatalf("app-server started %d times, want once", n)
	}
}

func TestBadConfigNeverRuns(t *testing.T) {
	f := newFake()
	f.badConfig = true
	s := installedService(t, f)
	if _, err := s.Account(context.Background()); err == nil || !strings.Contains(err.Error(), "не принял настройки") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartWritesLockedConfigAndClearsLeftovers(t *testing.T) {
	f := newFake()
	s := installedService(t, f)
	home := filepath.Join(s.Dir, "home")
	_ = os.MkdirAll(home, 0o700)
	for _, name := range []string{"auth.json", "logs_2.sqlite", "state_5.sqlite-wal"} {
		_ = os.WriteFile(filepath.Join(home, name), []byte("x"), 0o600)
	}
	_ = os.WriteFile(filepath.Join(home, "config.toml"), []byte("[features]\nshell_tool = true\n"), 0o600)
	if _, err := s.Account(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	for _, want := range []string{"shell_tool = false", "web_search = \"disabled\"", "include_environment_context = false", "[tools.experimental_request_user_input]\nenabled = false"} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("config has no %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "auth.json")); err != nil {
		t.Fatal("the sign-in must be kept")
	}
	if _, err := os.Stat(filepath.Join(home, "logs_2.sqlite")); err == nil {
		t.Fatal("Codex's leftovers must be cleared")
	}
}

func TestIdleStop(t *testing.T) {
	f := newFake()
	s := installedService(t, f)
	s.Idle = 50 * time.Millisecond
	if _, err := s.Account(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !s.Quick().Running })
	if _, err := s.Account(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := f.count(&f.starts); n != 2 {
		t.Fatalf("starts = %d", n)
	}
}

func TestNotInstalled(t *testing.T) {
	s := &Service{Dir: t.TempDir(), Spawn: newFake().spawn}
	if _, err := s.Account(context.Background()); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
	if s.Quick().Installed || s.SignedIn() {
		t.Fatal("nothing is installed")
	}
}

func tgz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func TestInstallChecksAndReplaces(t *testing.T) {
	archive := tgz(t, map[string]string{"bin/" + exeName(): "binary", "bin/codex-code-mode-host": "helper"})
	sum := sha256.Sum256(archive)
	var srv *httptest.Server
	digest := "sha256:" + hex.EncodeToString(sum[:])
	tag := "rust-v2.0.0"
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/openai/codex/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": tag, "assets": []any{
				map[string]any{"name": "codex-app-server-package-test-target.tar.gz", "browser_download_url": srv.URL + "/dl/" + tag + "/pkg.tar.gz", "size": len(archive), "digest": digest},
				map[string]any{"name": "codex-package-test-target.tar.gz", "browser_download_url": srv.URL + "/dl/other", "size": 10},
			}})
		case "/dl/" + tag + "/pkg.tar.gz":
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := newFake()
	s := &Service{Dir: t.TempDir(), Spawn: f.spawn, Releases: Releases{API: srv.URL, Download: srv.URL + "/dl/", Target: "test-target"}}
	defer s.Stop()
	got, err := s.Install(context.Background())
	if err != nil || got != tag {
		t.Fatalf("install = %q %v", got, err)
	}
	exe, v := s.Installed()
	if v != tag || filepath.Base(exe) != exeName() {
		t.Fatalf("installed = %q %q", exe, v)
	}
	// A wrong checksum is refused and the installed version stays.
	tag, digest = "rust-v3.0.0", "sha256:"+strings.Repeat("0", 64)
	if _, err := s.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "Контрольная сумма") {
		t.Fatalf("bad checksum: %v", err)
	}
	if _, v := s.Installed(); v != "rust-v2.0.0" {
		t.Fatalf("installed after a failed update = %q", v)
	}
	// No checksum at all: not installed.
	digest = ""
	if _, err := s.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "контрольной суммы") {
		t.Fatalf("no checksum: %v", err)
	}
	// A download from anywhere but the release is refused.
	s.Releases.Download = "https://github.com/openai/codex/releases/download/"
	digest = "sha256:" + hex.EncodeToString(sum[:])
	if _, err := s.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "не указывает на выпуск") {
		t.Fatalf("foreign URL: %v", err)
	}
	if err := s.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Dir); !os.IsNotExist(err) {
		t.Fatal("Remove must delete Codex and the sign-in")
	}
}
