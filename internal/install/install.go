// Package install installs and updates OmniBox AI on this computer. A new installation asks a
// few questions, each with a default, so pressing Enter all the way gives a working setup.
// Every change is undone if a later step fails; an update keeps all settings.
package install

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/control"
	"github.com/aafnx/omnibox-ai/internal/enginemgr"
	"github.com/aafnx/omnibox-ai/internal/gateway"
	"github.com/aafnx/omnibox-ai/internal/panel"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// FirewallRule is the name of the Windows Firewall rule for the panel port.
const FirewallRule = "OmniBox AI"

// Installer installs from the running binary.
type Installer struct {
	ProgramDir string
	DataDir    string
	Version    string
	Source     string // the binary to install (normally the running one)
	Services   platform.Services
	Manager    *enginemgr.Manager
	In         io.Reader
	Out        io.Writer
	Yes        bool // accept every default
	// Platform hooks, replaced in tests.
	AllowPort  func(rule string, port int) error
	RemoveRule func(rule string) error
	Health     func(ctx context.Context, port int, host string) error
	Addresses  func() []string
	// Restrict limits the data directory to administrators; AddToPath makes "ai" a command.
	Restrict  func(path string) error
	AddToPath func(dir string) (func() error, error)
	// ShimDir is where the ai command goes: the program directory on Windows, /usr/local/bin on Linux.
	ShimDir string
	// FailAt makes the named step fail, to test the rollback.
	FailAt string

	in   *bufio.Reader
	eof  bool
	undo []step
}

type step struct {
	name string
	undo func() error
}

func (i *Installer) say(format string, args ...any) { fmt.Fprintf(i.Out, format+"\n", args...) }

// do runs one step and remembers how to undo it.
func (i *Installer) do(name string, run func() error, undo func() error) error {
	if i.FailAt == name {
		return fmt.Errorf("Искусственный сбой установки на шаге %s.", name)
	}
	if err := run(); err != nil {
		return fmt.Errorf("%s: %v", name, err)
	}
	if undo != nil {
		i.undo = append(i.undo, step{name, undo})
	}
	return nil
}

func (i *Installer) rollback(cause error) error {
	i.say("Установка не завершилась: %v", cause)
	i.say("Возвращаю всё как было…")
	var failed []string
	for k := len(i.undo) - 1; k >= 0; k-- {
		if err := i.undo[k].undo(); err != nil {
			failed = append(failed, i.undo[k].name+": "+err.Error())
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%v. Откат выполнен не полностью: %s", cause, strings.Join(failed, "; "))
	}
	return fmt.Errorf("%v. Изменения отменены.", cause)
}

func (i *Installer) configPath() string { return filepath.Join(i.DataDir, "config.json") }

// binary is where this version's program lives; older versions stay next to it for rollback.
func (i *Installer) binary() string {
	name := "omnibox"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(i.ProgramDir, "bin", i.Version, name)
}

// shim is the ai command: a tiny script that runs this version's binary.
func (i *Installer) shim() (path, content string) {
	if runtime.GOOS == "windows" {
		return filepath.Join(i.ShimDir, "ai.cmd"), "@\"" + i.binary() + "\" ai %*\r\n"
	}
	return filepath.Join(i.ShimDir, "ai"), "#!/bin/sh\nexec \"" + i.binary() + "\" ai \"$@\"\n"
}

// placeCommand writes the ai command (an update points it at the new version) and puts it on PATH.
func (i *Installer) placeCommand() error {
	path, content := i.shim()
	old, readErr := os.ReadFile(path)
	if err := i.do("command", func() error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0o755)
	}, func() error {
		if readErr != nil {
			return os.Remove(path)
		}
		return os.WriteFile(path, old, 0o755)
	}); err != nil {
		return err
	}
	var undo func() error
	return i.do("path", func() error {
		var err error
		undo, err = i.AddToPath(i.ShimDir)
		return err
	}, func() error { return undo() })
}

func panelSpec(exe, data string) platform.ServiceSpec {
	return platform.ServiceSpec{Name: config.PanelService, DisplayName: "OmniBox AI", Description: "Панель и OpenAI API OmniBox AI.",
		Executable: exe, Args: []string{"serve", "--data", data}, Automatic: true}
}

func engineSpec(name, exe, data string) platform.ServiceSpec {
	return platform.ServiceSpec{Name: config.ServiceName(name), DisplayName: "OmniBox AI · " + config.Label(name),
		Description: "Движок " + config.Label(name) + " для OmniBox AI. Запускается из панели.",
		Executable:  exe, Args: []string{"engine", name, "--data", data}}
}

// Run installs, or updates when OmniBox AI is already installed.
func (i *Installer) Run(ctx context.Context) error {
	i.in = bufio.NewReader(i.In)
	// Each run undoes only its own steps.
	i.undo = nil
	if _, err := os.Stat(i.configPath()); err == nil {
		return i.update(ctx)
	}
	return i.fresh(ctx)
}

func copyFile(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	tmp := to + ".tmp"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(tmp)
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, to)
}

// placeBinary copies the program into bin/<version>; a version already there is reused.
func (i *Installer) placeBinary() error {
	exe := i.binary()
	existed := false
	if _, err := os.Stat(exe); err == nil {
		existed = true
	}
	return i.do("files", func() error {
		if existed && samePath(i.Source, exe) {
			return nil
		}
		return copyFile(i.Source, exe)
	}, func() error {
		if existed {
			return nil
		}
		return os.RemoveAll(filepath.Dir(exe))
	})
}

func samePath(a, b string) bool {
	x, _ := filepath.Abs(a)
	y, _ := filepath.Abs(b)
	return strings.EqualFold(x, y)
}

// ask shows a question with its default; Enter (or --yes) takes the default.
func (i *Installer) ask(question, def string) string {
	if i.Yes {
		return def
	}
	if def != "" {
		fmt.Fprintf(i.Out, "%s [%s]: ", question, def)
	} else {
		fmt.Fprintf(i.Out, "%s: ", question)
	}
	line, err := i.in.ReadString('\n')
	i.eof = err != nil
	if line = clean(line); line == "" {
		return def
	}
	return line
}

// clean drops what a terminal or a copied link can add around an answer: spaces, invisible
// characters (zero-width space, BOM) and enclosing quotes or angle brackets.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	return strings.Trim(strings.TrimSpace(s), "\"'`<>")
}

// askValid repeats a question until check accepts the answer. Without an answer to give
// (--yes or end of input) a rejected default is an error.
func (i *Installer) askValid(question, def string, check func(string) (string, error)) (string, error) {
	for {
		answer := i.ask(question, def)
		value, err := check(answer)
		if err == nil {
			return value, nil
		}
		if i.Yes || i.eof {
			return "", err
		}
		i.say("%s Получено: %q. Повторите ввод.", strings.TrimSuffix(err.Error(), "."), answer)
	}
}

func (i *Installer) yes(question string, def bool) bool {
	d := "Y/n"
	if !def {
		d = "y/N"
	}
	for {
		switch strings.ToLower(i.ask(question+" ("+d+")", "")) {
		case "":
			return def
		case "y", "yes", "д", "да":
			return true
		case "n", "no", "н", "нет":
			return false
		}
	}
}

// DefaultAddresses are this computer's IPv4 addresses, its name and localhost.
func DefaultAddresses() []string {
	var out []string
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil && !ipnet.IP.IsLoopback() && !ipnet.IP.IsLinkLocalUnicast() {
				out = append(out, ipnet.IP.String())
			}
		}
	}
	if host, err := os.Hostname(); err == nil && regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`).MatchString(host) {
		out = append(out, strings.ToLower(host))
	}
	return append(out, "localhost")
}

type answers struct {
	port           int
	hosts          []string
	apiURL         string
	publicOrigin   string
	trustedProxies []string
	engines        bool
	models         string
}

// The owner's NAS: Synology reverse proxy for https://llm.fnx.red. Answer "-" to install without it.
const (
	DefaultPublicOrigin = "https://llm.fnx.red"
	DefaultNAS          = "192.168.1.10"
)

func (i *Installer) wizard() (answers, error) {
	var a answers
	addresses := DefaultAddresses()
	if i.Addresses != nil {
		addresses = i.Addresses()
	}
	for {
		p, err := strconv.Atoi(i.ask("Порт панели и API", strconv.Itoa(config.DefaultPanelPort)))
		if err == nil && p >= 1024 && p <= 65535 && p != 8801 && p != 8802 {
			a.port = p
			break
		}
		i.say("Нужен порт от 1024 до 65535, кроме 8801 и 8802 (их занимают движки).")
		if i.Yes {
			return a, errors.New("некорректный порт")
		}
	}
	if _, err := i.askValid("Адреса панели через запятую", strings.Join(addresses, ", "), func(s string) (string, error) {
		a.hosts = nil
		for _, h := range strings.Split(s, ",") {
			if h = clean(h); h != "" && !slices.Contains(a.hosts, h) {
				if err := config.ValidHost(h); err != nil {
					return "", err
				}
				a.hosts = append(a.hosts, h)
			}
		}
		if len(a.hosts) == 0 || len(a.hosts) > 20 {
			return "", errors.New("Нужно от 1 до 20 адресов.")
		}
		return s, nil
	}); err != nil {
		return a, err
	}
	first := "localhost"
	for _, h := range a.hosts {
		if net.ParseIP(h) != nil {
			first = h
			break
		}
	}
	var err error
	if a.publicOrigin, err = i.askValid("HTTPS-адрес через reverse proxy на NAS («-» — без NAS)", DefaultPublicOrigin, func(s string) (string, error) {
		if s == "-" || strings.EqualFold(s, "нет") || strings.EqualFold(s, "no") {
			return "", nil
		}
		if !strings.Contains(s, "://") {
			s = "https://" + s
		}
		return config.PublicOrigin(s)
	}); err != nil {
		return a, err
	}
	if a.publicOrigin != "" {
		if _, err := i.askValid("IP NAS через запятую", DefaultNAS, func(s string) (string, error) {
			a.trustedProxies = nil
			for _, ip := range strings.Split(s, ",") {
				if ip = clean(ip); ip != "" {
					if net.ParseIP(ip) == nil {
						return "", errors.New("Нужны IP-адреса NAS, например 192.168.1.10, без масок и портов.")
					}
					a.trustedProxies = append(a.trustedProxies, ip)
				}
			}
			if len(a.trustedProxies) == 0 || len(a.trustedProxies) > 20 {
				return "", errors.New("Нужно от 1 до 20 IP-адресов NAS.")
			}
			return s, nil
		}); err != nil {
			return a, err
		}
	}
	// Clients outside the LAN reach the API through the NAS, under the same name as the panel.
	apiDefault := "http://" + first + ":" + strconv.Itoa(a.port) + "/v1"
	if a.publicOrigin != "" {
		apiDefault = a.publicOrigin + "/v1"
	}
	if a.apiURL, err = i.askValid("Адрес API для клиентов", apiDefault, func(s string) (string, error) {
		if s = strings.TrimRight(s, "/"); !config.ClientBaseURL(s) {
			return "", errors.New("Нужен адрес HTTP/HTTPS, который заканчивается на /v1.")
		}
		return s, nil
	}); err != nil {
		return a, err
	}
	a.engines = i.yes("Установить PrismML и Ollama последних версий?", true)
	a.models = i.ask("Каталог моделей", filepath.Join(i.DataDir, "models"))
	return a, nil
}

func (i *Installer) fresh(ctx context.Context) error {
	if s, err := i.Services.Query(config.PanelService); err == nil && s.State != platform.Missing {
		return errors.New("Служба omnibox уже есть, но настроек нет. Удалите службу omnibox и повторите установку.")
	}
	i.say("OmniBox AI %s: новая установка. Enter принимает ответ в скобках.", i.Version)
	a, err := i.wizard()
	if err != nil {
		return err
	}
	password, key := panel.GeneratePassword(), ""
	hash, err := panel.NewPassword(password)
	if err != nil {
		return err
	}
	cfg := config.Default()
	cfg.Panel.Port, cfg.Panel.Hosts, cfg.Panel.Password = a.port, a.hosts, hash
	cfg.Panel.PublicOrigin, cfg.Panel.TrustedProxies = a.publicOrigin, a.trustedProxies
	var keyHash string
	key, keyHash = gateway.NewKey()
	cfg.API = config.API{Enabled: true, PublicURL: a.apiURL, RequireKey: true, KeyHash: keyHash, Engine: config.PrismML, QueueDepth: 4}
	if err := cfg.Validate(); err != nil {
		return err
	}
	err = func() error {
		if err := i.placeBinary(); err != nil {
			return err
		}
		if err := i.do("data", func() error {
			if err := os.MkdirAll(filepath.Join(i.DataDir, "logs"), 0o755); err != nil {
				return err
			}
			return i.Restrict(i.DataDir)
		}, nil); err != nil {
			return err
		}
		if err := i.do("config", func() error { return cfg.Save(i.configPath()) }, func() error { return os.Remove(i.configPath()) }); err != nil {
			return err
		}
		spec := panelSpec(i.binary(), i.DataDir)
		if err := i.do("services", func() error { return i.Services.Install(spec) },
			func() error { return i.Services.Remove(spec.Name, control.StopTimeout) }); err != nil {
			return err
		}
		if err := i.do("firewall", func() error { return i.AllowPort(FirewallRule, a.port) }, func() error { return i.RemoveRule(FirewallRule) }); err != nil {
			return err
		}
		if err := i.placeCommand(); err != nil {
			return err
		}
		if err := i.do("panel", func() error { return i.Services.Start(config.PanelService) }, func() error { return i.Services.Stop(config.PanelService, control.StopTimeout) }); err != nil {
			return err
		}
		return i.do("panel-started", func() error { return i.Health(ctx, a.port, healthHost(a.hosts)) }, nil)
	}()
	if err != nil {
		return i.rollback(err)
	}
	// Committed. The secrets are shown first: an engine download can be long or interrupted,
	// and the panel works without engines.
	i.say("")
	i.say("Установлено: OmniBox AI %s.", i.Version)
	i.say("Панель: http://%s:%d", healthHost(a.hosts), a.port)
	if a.publicOrigin != "" {
		i.say("Через NAS: %s", a.publicOrigin)
	}
	i.say("Пароль панели: %s", password)
	i.say("API для клиентов (Base URL): %s", a.apiURL)
	i.say("API key: %s", key)
	i.say("Пароль и ключ показаны один раз и нигде не сохранены. Сохраните их в менеджере паролей; пароль можно сменить в панели.")
	i.say("Команда ai (в новом окне терминала, от имени администратора): ai status, ai help; забытый пароль — ai password reset, новый ключ — ai key new.")
	if a.engines {
		i.say("")
		i.installEngines(ctx, a.models)
	}
	i.say("")
	i.say("Готово.")
	if runtime.GOOS != "windows" {
		i.say("Откройте в межсетевом экране TCP-порт %d для локальной сети.", a.port)
	}
	return nil
}

func healthHost(hosts []string) string {
	for _, h := range hosts {
		if net.ParseIP(h) != nil {
			return h
		}
	}
	if slices.Contains(hosts, "localhost") || len(hosts) == 0 {
		return "localhost"
	}
	return hosts[0]
}

func (i *Installer) installEngines(ctx context.Context, models string) {
	store, err := config.Open(i.configPath())
	if err != nil {
		i.say("Движки не установлены: %v", err)
		return
	}
	c := &control.Controller{Store: store, Services: i.Services, Manager: i.Manager, DataDir: i.DataDir, Executable: i.binary()}
	for _, name := range config.EngineNames {
		i.say("%s: скачивание последней версии…", config.Label(name))
		message, err := c.InstallEngine(ctx, name, "")
		if err != nil {
			i.say("%s не установлен: %v Установите его позже в панели (Обновления → ИИ-движки).", config.Label(name), err)
			continue
		}
		if models != "" {
			_ = store.Update(func(cfg *config.Config) error {
				if name == config.PrismML {
					cfg.Engines[name].ModelsDirectory = models
				} else {
					cfg.Engines[name].ModelsDirectory = filepath.Join(models, "ollama")
				}
				return nil
			})
			_ = os.MkdirAll(store.Get().Engines[name].ModelsDirectory, 0o755)
		}
		i.say("%s", message)
	}
}

// update keeps every setting: it puts the new program next to the old one, points the
// services at it and restarts the panel; if the panel does not come up, the old version returns.
func (i *Installer) update(ctx context.Context) error {
	cfg, err := config.Load(i.configPath())
	if err != nil {
		return err
	}
	i.say("OmniBox AI %s: обновление, настройки сохраняются.", i.Version)
	err = func() error {
		if err := i.placeBinary(); err != nil {
			return err
		}
		names := []string{config.PanelService}
		for _, name := range config.EngineNames {
			if _, ok := cfg.Engines[name]; ok {
				names = append(names, config.ServiceName(name))
			}
		}
		for _, service := range names {
			old, err := i.Services.Executable(service)
			if err != nil {
				return fmt.Errorf("служба %s: %v", service, err)
			}
			spec := panelSpec(i.binary(), i.DataDir)
			if service != config.PanelService {
				spec = engineSpec(strings.TrimPrefix(service, "omnibox-"), i.binary(), i.DataDir)
			}
			previous := spec
			previous.Executable = old
			if err := i.do("services", func() error { return i.Services.Change(spec) }, func() error { return i.Services.Change(previous) }); err != nil {
				return err
			}
		}
		if err := i.placeCommand(); err != nil {
			return err
		}
		// The panel restarts on the new program; a failure brings the old one back up.
		if err := i.do("panel", func() error {
			if err := i.Services.Stop(config.PanelService, control.StopTimeout); err != nil {
				return err
			}
			return i.Services.Start(config.PanelService)
		}, func() error {
			_ = i.Services.Stop(config.PanelService, control.StopTimeout)
			return i.Services.Start(config.PanelService)
		}); err != nil {
			return err
		}
		return i.do("panel-started", func() error { return i.Health(ctx, cfg.Panel.Port, healthHost(cfg.Panel.Hosts)) }, nil)
	}()
	if err != nil {
		return i.rollback(err)
	}
	i.pruneVersions()
	i.say("Готово: OmniBox AI %s. Работающий движок перейдёт на новую версию при следующем запуске.", i.Version)
	i.say("Пароль панели и API key не изменились. Забыли — ai password reset или ai key new (от имени администратора) покажут новые.")
	return nil
}

// pruneVersions keeps this version and the newest previous one.
func (i *Installer) pruneVersions() {
	dir := filepath.Join(i.ProgramDir, "bin")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type version struct {
		name string
		mod  time.Time
	}
	var others []version
	for _, e := range entries {
		if e.IsDir() && e.Name() != i.Version {
			if info, err := e.Info(); err == nil {
				others = append(others, version{e.Name(), info.ModTime()})
			}
		}
	}
	slices.SortFunc(others, func(a, b version) int { return b.mod.Compare(a.mod) })
	for k, v := range others {
		if k >= 1 {
			_ = os.RemoveAll(filepath.Join(dir, v.name))
		}
	}
}

// PanelHealth waits until the panel answers on its port.
func PanelHealth(ctx context.Context, port int, host string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/", nil)
		req.Host = host
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("панель не ответила за 30 с; подробности — в журнале службы omnibox")
		case <-time.After(500 * time.Millisecond):
		}
	}
}
