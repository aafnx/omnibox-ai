// Package chatgpt connects the owner's ChatGPT subscription through OpenAI's official Codex
// app-server (Apache-2.0): it is downloaded from Codex's GitHub releases on the owner's action,
// signs in with a device code and answers in the panel's chat and, by an option, in /v1.
//
// Codex is a coding agent; here it is only a model. Its config gives it no environment, no
// tools, no web search, no memories and no plugins, every request of its own (an approval, a
// tool call, a question) is refused, and a turn that starts anything but a message or reasoning
// is interrupted. Threads are ephemeral, the process starts on demand and stops when idle.
package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aafnx/omnibox-ai/internal/host"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Dir is the part of the data directory that belongs to ChatGPT: the app-server (app), its home
// with the sign-in (home) and an empty working directory (work).
const Dir = "chatgpt"

// ErrNotInstalled means Codex has not been downloaded yet.
var ErrNotInstalled = errors.New("ChatGPT не подключён: установите Codex в «Настройки → ChatGPT».")

// ErrSignedOut means no ChatGPT account is signed in.
var ErrSignedOut = errors.New("ChatGPT не подключён: войдите в аккаунт в «Настройки → ChatGPT».")

// idleStop is how long the app-server waits for the next request before it is stopped.
const idleStop = 15 * time.Minute

// Service runs the app-server and remembers what it last said about the account.
type Service struct {
	Dir     string // <data>/chatgpt
	Version string // OmniBox's version, told to Codex
	Logf    func(format string, args ...any)
	// Spawn starts the app-server; tests give an in-process one.
	Spawn func(exe, home, work string) (io.ReadCloser, io.WriteCloser, func(), error)
	// Releases is where Codex comes from (tests give a local server).
	Releases Releases
	Idle     time.Duration
	Now      func() time.Time

	// mu guards the process; state guards what it said. The reading goroutine takes only state,
	// so a start waiting for an answer under mu never blocks it.
	mu    sync.Mutex
	c     *conn
	kill  func()
	timer *time.Timer
	busy  int

	state      sync.Mutex
	installing *Installing
	login      *Login
	early      *loginResult // a completion that came before its start was answered
	limits     *Limits
	models     []Model
	account    *Account
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func (s *Service) home() string { return filepath.Join(s.Dir, "home") }
func (s *Service) work() string { return filepath.Join(s.Dir, "work") }

// config is written before every start: whatever is in the file is replaced.
const codexConfig = `# Written by OmniBox AI before every start of the app-server; changes are overwritten.
# ChatGPT only answers in the chat: Codex gets no environment, tools, files or commands.
cli_auth_credentials_store = "file"
forced_login_method = "chatgpt"
approval_policy = "on-request"
sandbox_mode = "read-only"
web_search = "disabled"
check_for_update_on_startup = false
project_doc_max_bytes = 0
include_permissions_instructions = false
include_apps_instructions = false
include_collaboration_mode_instructions = false
include_environment_context = false

[analytics]
enabled = false

[history]
persistence = "none"

[skills]
include_instructions = false

[tools.experimental_request_user_input]
enabled = false

[features]
shell_tool = false
unified_exec = false
shell_snapshot = false
view_image = false
sleep_tool = false
js_repl = false
code_mode = false
code_mode_host = false
apply_patch_freeform = false
memories = false
hooks = false
request_permissions_tool = false
multi_agent = false
multi_agent_v2 = false
apps = false
enable_mcp_apps = false
plugins = false
tool_search = false
tool_suggest = false
skill_search = false
image_generation = false
browser_use = false
computer_use = false
in_app_browser = false
goals = false
remote_control = false
prevent_idle_sleep = false
send_message_to_user_async = false
current_time_reminder = false
codex_git_commit = false
worktrees = false
chronicle = false
artifact = false
`

// keep are the files of the home that survive a start: the sign-in and the installation id.
var keep = map[string]bool{"auth.json": true, "config.toml": true, "installation_id": true}

// prepare writes the config and clears what Codex left from earlier runs (its databases, logs
// and caches): threads are ephemeral, so nothing there is needed again.
func (s *Service) prepare() error {
	for _, dir := range []string{s.Dir, s.home(), s.work()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	if err := platform.RestrictToAdmins(s.Dir); err != nil {
		s.logf("chatgpt: access to %s not restricted: %v", s.Dir, err)
	}
	for _, dir := range []string{s.home(), s.work()} {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if dir == s.home() && keep[e.Name()] {
				continue
			}
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
	return platform.WriteAtomic(filepath.Join(s.home(), "config.toml"), []byte(codexConfig), 0o600)
}

func spawn(exe, home, work string) (io.ReadCloser, io.WriteCloser, func(), error) {
	cmd := exec.Command(exe, "--listen", "stdio://")
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "CODEX_HOME="+home)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	cmd.Stderr = io.Discard
	g, err := host.StartGroup(cmd)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("Не удалось запустить Codex: %v", err)
	}
	go func() { _ = cmd.Wait(); g.Kill() }()
	return stdout, stdin, g.Kill, nil
}

// use returns the running app-server, started if needed, and holds it until release is called.
func (s *Service) use(ctx context.Context) (*conn, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	if s.c == nil || !s.c.alive() {
		if err := s.start(ctx); err != nil {
			s.arm()
			return nil, nil, err
		}
	}
	s.busy++
	var once sync.Once
	return s.c, func() {
		once.Do(func() {
			s.mu.Lock()
			s.busy--
			s.arm()
			s.mu.Unlock()
		})
	}, nil
}

// arm stops the app-server after a quiet while; a sign-in in progress keeps it.
func (s *Service) arm() {
	if s.busy > 0 || s.c == nil {
		return
	}
	idle := s.Idle
	if idle == 0 {
		idle = idleStop
	}
	s.state.Lock()
	if s.login != nil && !s.login.Done && s.login.Expires.After(s.now()) {
		idle = max(idle, s.login.Expires.Sub(s.now())+time.Minute)
	}
	s.state.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(idle, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.busy == 0 {
			s.stopLocked()
		}
	})
}

// start launches the app-server and introduces OmniBox; s.mu is held.
func (s *Service) start(ctx context.Context) error {
	s.stopLocked()
	exe, _ := s.Installed()
	if exe == "" {
		return ErrNotInstalled
	}
	if err := s.prepare(); err != nil {
		return fmt.Errorf("Не удалось подготовить каталог ChatGPT: %v", err)
	}
	run := s.Spawn
	if run == nil {
		run = spawn
	}
	stdout, stdin, kill, err := run(exe, s.home(), s.work())
	if err != nil {
		return err
	}
	c := newConn(stdout, stdin)
	c.refused = func(method string) { s.logf("chatgpt: refused the app-server's request %s", method) }
	badConfig := make(chan string, 1)
	c.subscribe(func(method string, params json.RawMessage) { s.notified(method, params, badConfig) })
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var init struct {
		UserAgent string `json:"userAgent"`
	}
	err = c.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "omnibox", "title": "OmniBox AI", "version": s.Version},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &init)
	if err == nil {
		err = c.notify("initialized", nil)
	}
	if err == nil {
		// Answered after the warnings of the start, which the reader has handled by then.
		err = c.call(ctx, "account/read", map[string]any{"refreshToken": false}, nil)
	}
	// A config Codex cannot read is replaced by its defaults — with every tool on. It never runs so.
	select {
	case w := <-badConfig:
		err = fmt.Errorf("Codex не принял настройки OmniBox: %s", w)
	default:
	}
	if err != nil {
		kill()
		return fmt.Errorf("Codex не запустился: %v", err)
	}
	s.c, s.kill = c, kill
	s.state.Lock()
	s.models = nil
	s.state.Unlock()
	s.logf("chatgpt: app-server started (%s)", init.UserAgent)
	return nil
}

// stopLocked ends the app-server; s.mu is held.
func (s *Service) stopLocked() {
	if s.kill != nil {
		s.kill()
	}
	if s.c != nil {
		s.c.shut(errors.New("Codex остановлен"))
	}
	s.c, s.kill = nil, nil
	s.state.Lock()
	s.login = nil
	s.state.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
}

// Stop ends the app-server now; the next request starts it again.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

// notified keeps what the app-server tells about the account between requests.
func (s *Service) notified(method string, params json.RawMessage, badConfig chan string) {
	switch method {
	case "configWarning":
		var w struct {
			Summary string `json:"summary"`
			Details string `json:"details"`
		}
		_ = json.Unmarshal(params, &w)
		if strings.Contains(strings.ToLower(w.Summary), "invalid configuration") {
			select {
			case badConfig <- strings.TrimSpace(w.Summary + " " + w.Details):
			default:
			}
		}
	case "account/rateLimits/updated":
		var n struct {
			RateLimits snapshot `json:"rateLimits"`
		}
		if json.Unmarshal(params, &n) == nil {
			s.state.Lock()
			s.limits = mergeLimits(s.limits, n.RateLimits, s.now())
			s.state.Unlock()
		}
	case "account/login/completed":
		var n loginResult
		_ = json.Unmarshal(params, &n)
		s.state.Lock()
		if s.login != nil && (n.LoginID == "" || n.LoginID == s.login.ID) {
			n.apply(s.login)
		} else {
			s.early = &n
		}
		s.account = nil
		s.state.Unlock()
	case "account/updated":
		s.state.Lock()
		s.account, s.limits = nil, nil
		s.state.Unlock()
	}
}

type loginResult struct {
	LoginID string `json:"loginId"`
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

func (n loginResult) apply(l *Login) {
	l.Done, l.Error = true, ""
	if !n.Success {
		l.Error = n.Error
		if l.Error == "" {
			l.Error = "Вход не завершён."
		}
	}
}
