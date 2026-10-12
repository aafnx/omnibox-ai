package chatgpt

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/aafnx/omnibox-ai/internal/enginemgr"
	"github.com/aafnx/omnibox-ai/internal/platform"
	"github.com/aafnx/omnibox-ai/internal/progress"
)

// Releases finds Codex's app-server in the latest stable release of openai/codex.
type Releases struct {
	HTTP *http.Client
	// API is the GitHub API root; Download is the prefix release files must come from.
	API      string
	Download string
	// Target is the Rust target of the build; empty is this system's.
	Target string
}

const (
	repo       = "openai/codex"
	assetLimit = 1 << 30
	sumsName   = "codex-package_SHA256SUMS"
	manifest   = ".omnibox-codex.json"
)

var tagPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (r Releases) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: time.Hour}
}

func (r Releases) target() string {
	if r.Target != "" {
		return r.Target
	}
	arch := "x86_64"
	if runtime.GOARCH == "arm64" {
		arch = "aarch64"
	}
	switch runtime.GOOS {
	case "windows":
		return arch + "-pc-windows-msvc"
	case "darwin":
		return arch + "-apple-darwin"
	}
	return arch + "-unknown-linux-musl"
}

type asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

type release struct {
	Tag   string
	Build asset
	Sums  *asset
}

// latest reads the newest stable release and picks this system's app-server build: the package
// with its helper, else the bare binary.
func (r Releases) latest(ctx context.Context) (release, error) {
	api := r.API
	if api == "" {
		api = "https://api.github.com"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, api+"/repos/"+repo+"/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "omnibox")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := *r.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return release{}, fmt.Errorf("GitHub недоступен: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return release{}, fmt.Errorf("GitHub ответил %d.", resp.StatusCode)
	}
	var raw struct {
		Tag        string  `json:"tag_name"`
		Draft      bool    `json:"draft"`
		Prerelease bool    `json:"prerelease"`
		Assets     []asset `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&raw); err != nil {
		return release{}, fmt.Errorf("GitHub вернул некорректный выпуск Codex: %v", err)
	}
	if raw.Draft || raw.Prerelease || !tagPattern.MatchString(raw.Tag) {
		return release{}, errors.New("Последний выпуск Codex не стабильный — повторите позже.")
	}
	t := r.target()
	names := []string{"codex-app-server-package-" + t + ".tar.gz", "codex-app-server-" + t + ".tar.gz", "codex-app-server-" + t + ".zip", "codex-app-server-" + t + ".exe.zip"}
	out := release{Tag: raw.Tag}
	for _, name := range names {
		for _, a := range raw.Assets {
			if a.Name == name && out.Build.Name == "" {
				out.Build = a
			}
		}
	}
	for _, a := range raw.Assets {
		if a.Name == sumsName {
			a := a
			out.Sums = &a
		}
	}
	if out.Build.Name == "" {
		return release{}, fmt.Errorf("В выпуске Codex %s нет app-server для %s.", raw.Tag, t)
	}
	return out, nil
}

// checksum is the expected SHA256 of the build: GitHub's digest of the asset, else the line of
// the release's checksum list. A build without either is not installed.
func (r Releases) checksum(ctx context.Context, rel release) (string, error) {
	if d, ok := strings.CutPrefix(rel.Build.Digest, "sha256:"); ok && len(d) == 64 {
		return d, nil
	}
	if rel.Sums == nil {
		return "", errors.New("У выпуска Codex нет контрольной суммы — установка отменена.")
	}
	if err := r.checkURL(rel.Sums.URL); err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, rel.Sums.URL, nil)
	req.Header.Set("User-Agent", "omnibox")
	resp, err := r.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("Не удалось скачать контрольные суммы Codex: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Контрольные суммы Codex: ответ %d.", resp.StatusCode)
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 1<<20))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == rel.Build.Name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("В контрольных суммах Codex нет %s — установка отменена.", rel.Build.Name)
}

func (r Releases) checkURL(u string) error {
	prefix := r.Download
	if prefix == "" {
		prefix = "https://github.com/" + repo + "/releases/download/"
	}
	if !strings.HasPrefix(u, prefix) {
		return errors.New("Ссылка на файл Codex не указывает на выпуск GitHub.")
	}
	return nil
}

func (r Releases) download(ctx context.Context, a asset, sum, path string) error {
	if err := r.checkURL(a.URL); err != nil {
		return err
	}
	if a.Size <= 0 || a.Size > assetLimit {
		return fmt.Errorf("Недопустимый размер файла %s.", a.Name)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	req.Header.Set("User-Agent", "omnibox")
	resp, err := r.client().Do(req)
	if err != nil {
		return fmt.Errorf("Не удалось скачать %s: %v", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Не удалось скачать %s: ответ %d.", a.Name, resp.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(progress.Reader(ctx, resp.Body, a.Size), a.Size+1))
	if err != nil {
		return fmt.Errorf("Не удалось скачать %s: %v", a.Name, err)
	}
	if n != a.Size {
		return fmt.Errorf("Файл %s скачан не полностью: %d из %d байт.", a.Name, n, a.Size)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), sum) {
		return fmt.Errorf("Контрольная сумма %s не совпадает.", a.Name)
	}
	return f.Close()
}

type installed struct {
	Tag        string    `json:"tag"`
	Executable string    `json:"executable"`
	Installed  time.Time `json:"installed"`
}

func (s *Service) app() string { return filepath.Join(s.Dir, "app") }

// Installed is the app-server's executable and version, or "" before installation.
func (s *Service) Installed() (exe, tag string) {
	data, err := platform.ReadShared(filepath.Join(s.app(), manifest))
	var m installed
	if err != nil || json.Unmarshal(data, &m) != nil || !filepath.IsLocal(m.Executable) {
		return "", ""
	}
	exe = filepath.Join(s.app(), m.Executable)
	if info, err := os.Stat(exe); err != nil || !info.Mode().IsRegular() {
		return "", ""
	}
	return exe, m.Tag
}

// Install downloads the latest Codex app-server, checks its checksum and that it starts, and
// replaces the installed one. The sign-in is kept.
func (s *Service) Install(ctx context.Context) (string, error) {
	rel, err := s.Releases.latest(ctx)
	if err != nil {
		return "", err
	}
	if _, tag := s.Installed(); tag == rel.Tag {
		return tag, nil
	}
	sum, err := s.Releases.checksum(ctx, rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", err
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	stage := filepath.Join(s.Dir, ".staging-"+hex.EncodeToString(b[:]))
	defer os.RemoveAll(stage)
	files := filepath.Join(stage, "files")
	if err := os.MkdirAll(files, 0o700); err != nil {
		return "", err
	}
	archive := filepath.Join(stage, filepath.Base(rel.Build.Name))
	if err := s.Releases.download(ctx, rel.Build, sum, archive); err != nil {
		return "", err
	}
	if err := enginemgr.Extract(archive, files); err != nil {
		return "", fmt.Errorf("Не удалось распаковать %s: %v", rel.Build.Name, err)
	}
	_ = os.Remove(archive)
	exe := findServer(files)
	if exe == "" {
		return "", fmt.Errorf("В выпуске Codex %s нет codex-app-server.", rel.Tag)
	}
	_ = os.Chmod(exe, 0o755)
	rel2, _ := filepath.Rel(files, exe)
	data, _ := json.MarshalIndent(installed{Tag: rel.Tag, Executable: rel2, Installed: time.Now().UTC()}, "", "  ")
	if err := os.WriteFile(filepath.Join(files, manifest), data, 0o600); err != nil {
		return "", err
	}
	// The running app-server is stopped before its files are replaced; the old version is kept
	// until the new one has proved it starts.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	old := filepath.Join(stage, "old")
	if _, err := os.Stat(s.app()); err == nil {
		if err := os.Rename(s.app(), old); err != nil {
			return "", fmt.Errorf("Не удалось заменить Codex: %v", err)
		}
	}
	if err := os.Rename(files, s.app()); err != nil {
		_ = os.Rename(old, s.app())
		return "", fmt.Errorf("Не удалось установить Codex: %v", err)
	}
	if err := s.start(ctx); err != nil {
		s.stopLocked()
		_ = os.RemoveAll(s.app())
		_ = os.Rename(old, s.app())
		return "", err
	}
	s.arm()
	return rel.Tag, nil
}

// Installing is a download of Codex in progress, or the last one's failure.
type Installing struct {
	Running bool   `json:"running"`
	Done    int64  `json:"done,omitempty"`
	Total   int64  `json:"total,omitempty"`
	Error   string `json:"error,omitempty"`
}

// StartInstall downloads Codex in the background: the panel follows it in the status, and a
// browser that leaves the page does not cut the download.
func (s *Service) StartInstall() error {
	s.state.Lock()
	if s.installing != nil && s.installing.Running {
		s.state.Unlock()
		return errors.New("Codex уже скачивается.")
	}
	s.installing = &Installing{Running: true}
	s.state.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		ctx = progress.With(ctx, func(done, total int64) {
			s.state.Lock()
			s.installing.Done, s.installing.Total = done, total
			s.state.Unlock()
		})
		tag, err := s.Install(ctx)
		s.state.Lock()
		s.installing.Running = false
		if err != nil {
			s.installing.Error = err.Error()
			s.logf("chatgpt: install failed: %v", err)
		} else {
			s.installing = nil
			s.logf("chatgpt: Codex %s installed", tag)
		}
		s.state.Unlock()
	}()
	return nil
}

// findServer is the shallowest codex-app-server executable of an unpacked build.
func findServer(dir string) string {
	best := ""
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if !strings.HasPrefix(name, "codex-app-server") || (runtime.GOOS == "windows") != strings.HasSuffix(name, ".exe") {
			return nil
		}
		if best == "" || len(path) < len(best) {
			best = path
		}
		return nil
	})
	return best
}

// Remove stops the app-server and deletes Codex with the sign-in.
func (s *Service) Remove() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
	s.state.Lock()
	s.limits, s.models, s.account = nil, nil, nil
	s.state.Unlock()
	return os.RemoveAll(s.Dir)
}
