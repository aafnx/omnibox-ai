// Package enginemgr installs engine runtimes (PrismML llama-server, Ollama) from their public
// GitHub releases. Each version lives in <data>/engines/<engine>/<tag>; an engine's launch
// points at one of them. Network access happens only on the owner's action.
package enginemgr

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
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
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/progress"
	"github.com/aafnx/omnibox-ai/internal/storage"
)

// Kind describes where an engine's builds come from.
type Kind struct {
	Repo string
	Exe  string
	// AssetSets are alternatives, best first; every pattern of a set must match one asset.
	AssetSets [][]*regexp.Regexp
}

func re(s string) *regexp.Regexp { return regexp.MustCompile(s) }

// Kinds lists the engines for this operating system.
var Kinds = kinds(runtime.GOOS)

func kinds(goos string) map[string]Kind {
	if goos == "windows" {
		return map[string]Kind{
			// CUDA 13 builds support Blackwell (RTX 50); CUDA 12 is the fallback. The cudart archive carries the CUDA DLLs.
			config.PrismML: {Repo: "PrismML-Eng/llama.cpp", Exe: "llama-server.exe", AssetSets: [][]*regexp.Regexp{
				{re(`^llama-.+-bin-win-cuda-13\.\d+-x64\.zip$`), re(`^cudart-llama-bin-win-cuda-13\.\d+-x64\.zip$`)},
				{re(`^llama-.+-bin-win-cuda-12\.\d+-x64\.zip$`), re(`^cudart-llama-bin-win-cuda-12\.\d+-x64\.zip$`)},
			}},
			config.Ollama: {Repo: "ollama/ollama", Exe: "ollama.exe", AssetSets: [][]*regexp.Regexp{{re(`^ollama-windows-amd64\.zip$`)}}},
		}
	}
	return map[string]Kind{
		config.PrismML: {Repo: "PrismML-Eng/llama.cpp", Exe: "llama-server", AssetSets: [][]*regexp.Regexp{
			{re(`^llama-.+-bin-ubuntu-x64\.(?:zip|tar\.gz)$`)},
		}},
		config.Ollama: {Repo: "ollama/ollama", Exe: "ollama", AssetSets: [][]*regexp.Regexp{{re(`^ollama-linux-amd64\.tgz$`)}}},
	}
}

const (
	assetLimit   = 4 << 30
	manifestName = ".omnibox-engine.json"
)

var tagPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// Asset is one file of a release.
type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"-"`
	Size   int64  `json:"size"`
	SHA256 string `json:"-"`
}

// Release is a version with a build for this system.
type Release struct {
	Tag        string    `json:"tag"`
	Published  time.Time `json:"published"`
	Prerelease bool      `json:"prerelease"`
	Size       int64     `json:"size"`
	Assets     []Asset   `json:"-"`
}

// Version is an installed runtime.
type Version struct {
	Tag        string    `json:"tag"`
	Executable string    `json:"-"`
	Dir        string    `json:"-"`
	Installed  time.Time `json:"installed"`
}

// Manager downloads and keeps engine versions.
type Manager struct {
	DataDir string
	HTTP    *http.Client
	// API is the GitHub API root; Download is the prefix release files must come from.
	API      string
	Download func(repo string) string
	// Check runs the unpacked engine once to prove it starts (a missing DLL stops it at once).
	Check func(ctx context.Context, exe string) error
	Kinds map[string]Kind
}

func (m *Manager) kind(name string) (Kind, error) {
	kinds := m.Kinds
	if kinds == nil {
		kinds = Kinds
	}
	k, ok := kinds[name]
	if !ok {
		return Kind{}, fmt.Errorf("Неизвестный движок: %s. Доступны: prismml, ollama.", name)
	}
	return k, nil
}

func (m *Manager) client() *http.Client {
	if m.HTTP != nil {
		return m.HTTP
	}
	return &http.Client{Timeout: 2 * time.Hour}
}

func (m *Manager) root(name string) string { return filepath.Join(storage.EnginesDir(m.DataDir), name) }

// Releases lists recent releases with a build for this system, newest first; drafts are skipped.
func (m *Manager) Releases(ctx context.Context, name string) ([]Release, error) {
	k, err := m.kind(name)
	if err != nil {
		return nil, err
	}
	api := m.API
	if api == "" {
		api = "https://api.github.com"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, api+"/repos/"+k.Repo+"/releases?per_page=30", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "omnibox")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := *m.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub недоступен: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub ответил %d.", resp.StatusCode)
	}
	var raw []struct {
		Tag        string    `json:"tag_name"`
		Draft      bool      `json:"draft"`
		Prerelease bool      `json:"prerelease"`
		Published  time.Time `json:"published_at"`
		Assets     []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&raw); err != nil {
		return nil, fmt.Errorf("GitHub вернул некорректный список выпусков: %v", err)
	}
	var out []Release
	for _, r := range raw {
		if r.Draft || !tagPattern.MatchString(r.Tag) {
			continue
		}
		for _, set := range k.AssetSets {
			var chosen []Asset
			for _, pattern := range set {
				found := false
				for _, a := range r.Assets {
					if pattern.MatchString(a.Name) {
						chosen = append(chosen, Asset{Name: a.Name, URL: a.URL, Size: a.Size, SHA256: strings.TrimPrefix(a.Digest, "sha256:")})
						found = true
						break
					}
				}
				if !found {
					chosen = nil
					break
				}
			}
			if chosen != nil {
				rel := Release{Tag: r.Tag, Published: r.Published, Prerelease: r.Prerelease, Assets: chosen}
				for _, a := range chosen {
					rel.Size += a.Size
				}
				out = append(out, rel)
				break
			}
		}
	}
	return out, nil
}

// Find returns the release with tag, or the newest stable one for "".
func (m *Manager) Find(ctx context.Context, name, tag string) (Release, error) {
	if tag != "" && !tagPattern.MatchString(tag) {
		return Release{}, errors.New("Версия движка: латиница, цифры, точка, дефис, до 64 символов.")
	}
	releases, err := m.Releases(ctx, name)
	if err != nil {
		return Release{}, err
	}
	for _, r := range releases {
		if (tag == "" && !r.Prerelease) || (tag != "" && r.Tag == tag) {
			return r, nil
		}
	}
	if tag == "" {
		return Release{}, fmt.Errorf("Не найден выпуск %s со сборкой для этой системы.", config.Label(name))
	}
	return Release{}, fmt.Errorf("Версия %s не найдена среди последних выпусков %s со сборкой для этой системы.", tag, config.Label(name))
}

type manifest struct {
	SchemaVersion int       `json:"schema_version"`
	Engine        string    `json:"engine"`
	Tag           string    `json:"tag"`
	Executable    string    `json:"executable"`
	Installed     time.Time `json:"installed"`
	Assets        []string  `json:"assets"`
}

// Installed lists verified versions, newest install first.
func (m *Manager) Installed(name string) []Version {
	entries, err := os.ReadDir(m.root(name))
	if err != nil {
		return nil
	}
	var out []Version
	for _, e := range entries {
		if !e.IsDir() || e.Type()&os.ModeSymlink != 0 || !tagPattern.MatchString(e.Name()) {
			continue
		}
		dir := filepath.Join(m.root(name), e.Name())
		data, err := os.ReadFile(filepath.Join(dir, manifestName))
		var mf manifest
		if err != nil || json.Unmarshal(data, &mf) != nil || !filepath.IsLocal(mf.Executable) {
			continue
		}
		exe := filepath.Join(dir, mf.Executable)
		if info, err := os.Stat(exe); err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, Version{Tag: e.Name(), Executable: exe, Dir: dir, Installed: mf.Installed})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Installed.After(out[j].Installed) })
	return out
}

// TagOf is the managed version an executable belongs to, or "" for one installed elsewhere.
func (m *Manager) TagOf(name, exe string) string {
	if exe == "" {
		return ""
	}
	rel, err := filepath.Rel(m.root(name), exe)
	if err != nil || !filepath.IsLocal(rel) {
		return ""
	}
	return strings.Split(filepath.ToSlash(rel), "/")[0]
}

func randomHex() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Install downloads, verifies and unpacks a release into its version directory. An installed
// version is returned as is.
func (m *Manager) Install(ctx context.Context, name, tag string) (Version, error) {
	k, err := m.kind(name)
	if err != nil {
		return Version{}, err
	}
	release, err := m.Find(ctx, name, tag)
	if err != nil {
		return Version{}, err
	}
	for _, v := range m.Installed(name) {
		if v.Tag == release.Tag {
			return v, nil
		}
	}
	stage := filepath.Join(storage.EnginesDir(m.DataDir), ".staging-"+randomHex())
	defer os.RemoveAll(stage)
	files := filepath.Join(stage, "files")
	if err := os.MkdirAll(files, 0o755); err != nil {
		return Version{}, err
	}
	prefix := "https://github.com/" + k.Repo + "/releases/download/"
	if m.Download != nil {
		prefix = m.Download(k.Repo)
	}
	for _, a := range release.Assets {
		if !strings.HasPrefix(a.URL, prefix) {
			return Version{}, errors.New("Ссылка на файл движка не указывает на выпуск GitHub.")
		}
		if a.Size <= 0 || a.Size > assetLimit {
			return Version{}, fmt.Errorf("Недопустимый размер файла %s.", a.Name)
		}
		archive := filepath.Join(stage, filepath.Base(a.Name))
		if err := m.download(ctx, a, archive); err != nil {
			return Version{}, err
		}
		if err := Extract(archive, files); err != nil {
			return Version{}, fmt.Errorf("Не удалось распаковать %s: %v", a.Name, err)
		}
		_ = os.Remove(archive)
	}
	exe := findExe(files, k.Exe)
	if exe == "" {
		return Version{}, fmt.Errorf("В выпуске %s нет %s.", release.Tag, k.Exe)
	}
	_ = os.Chmod(exe, 0o755)
	check := m.Check
	if check == nil {
		check = checkBinary
	}
	if err := check(ctx, exe); err != nil {
		return Version{}, err
	}
	rel, _ := filepath.Rel(files, exe)
	mf := manifest{SchemaVersion: 1, Engine: name, Tag: release.Tag, Executable: rel, Installed: time.Now().UTC()}
	for _, a := range release.Assets {
		mf.Assets = append(mf.Assets, a.Name)
	}
	data, _ := json.MarshalIndent(mf, "", "  ")
	if err := os.WriteFile(filepath.Join(files, manifestName), data, 0o644); err != nil {
		return Version{}, err
	}
	target := filepath.Join(m.root(name), release.Tag)
	if err := os.MkdirAll(m.root(name), 0o755); err != nil {
		return Version{}, err
	}
	_ = os.RemoveAll(target)
	if err := os.Rename(files, target); err != nil {
		return Version{}, err
	}
	return Version{Tag: release.Tag, Executable: filepath.Join(target, rel), Dir: target, Installed: mf.Installed}, nil
}

func (m *Manager) download(ctx context.Context, a Asset, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "omnibox")
	resp, err := m.client().Do(req)
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
	if a.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), a.SHA256) {
		return fmt.Errorf("Контрольная сумма %s не совпадает.", a.Name)
	}
	return f.Close()
}

// target joins an archive entry name to dir and refuses names that would leave it.
func target(dir, name string) (string, error) {
	name = filepath.FromSlash(strings.TrimPrefix(name, "./"))
	if name == "" || name == "." {
		return "", nil
	}
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("недопустимый путь в архиве: %s", name)
	}
	return filepath.Join(dir, name), nil
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(r, assetLimit)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Extract unpacks a zip or tar.gz archive; links and entries outside dir are refused.
func Extract(archive, dir string) error {
	if strings.HasSuffix(archive, ".zip") {
		z, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			path, err := target(dir, f.Name)
			if err != nil {
				return err
			}
			if path == "" || f.FileInfo().IsDir() {
				continue
			}
			if !f.Mode().IsRegular() {
				return fmt.Errorf("ссылка в архиве: %s", f.Name)
			}
			r, err := f.Open()
			if err != nil {
				return err
			}
			err = writeFile(path, r, f.Mode().Perm()|0o600)
			r.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		path, err := target(dir, h.Name)
		if err != nil {
			return err
		}
		switch {
		case path == "" || h.Typeflag == tar.TypeDir:
		case h.Typeflag == tar.TypeReg:
			if err := writeFile(path, tr, os.FileMode(h.Mode).Perm()|0o600); err != nil {
				return err
			}
		case h.Typeflag == tar.TypeSymlink && filepath.IsLocal(filepath.Join(filepath.Dir(h.Name), h.Linkname)) && !filepath.IsAbs(h.Linkname):
			// Linux builds link versioned shared libraries; links stay inside the version directory.
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			if err := os.Symlink(h.Linkname, path); err != nil {
				return err
			}
		default:
			return fmt.Errorf("недопустимый элемент архива: %s", h.Name)
		}
	}
}

// findExe returns the shallowest file named exe.
func findExe(dir, exe string) string {
	best := ""
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && strings.EqualFold(d.Name(), exe) && (best == "" || len(path) < len(best)) {
			best = path
		}
		return nil
	})
	return best
}

func checkBinary(ctx context.Context, exe string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--version")
	cmd.Dir = filepath.Dir(exe)
	out, _ := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("Движок не ответил на --version за 60 с.")
	}
	if !regexp.MustCompile(`(?i)version`).Match(out) {
		line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		return fmt.Errorf("Движок не запустился: %s", line)
	}
	return nil
}

// RemoveUnused deletes versions that neither the current nor the rollback launch uses.
func (m *Manager) RemoveUnused(name string, e *config.Engine) []string {
	used := map[string]bool{}
	if e != nil {
		used[m.TagOf(name, e.Executable)] = true
		if e.Previous != nil {
			used[m.TagOf(name, e.Previous.Executable)] = true
		}
	}
	var removed []string
	for _, v := range m.Installed(name) {
		if !used[v.Tag] && os.RemoveAll(v.Dir) == nil {
			removed = append(removed, v.Tag)
		}
	}
	return removed
}
