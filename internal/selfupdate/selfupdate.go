// Package selfupdate downloads a release of OmniBox AI itself from its private GitHub repository:
// the archive for this system and SHA256SUMS, checked before anything runs. The token is used for
// the GitHub API only; the file itself comes from the signed storage link without it.
package selfupdate

import (
	"context"
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
	"github.com/aafnx/omnibox-ai/internal/progress"
)

// Repo is where the releases are published.
const Repo = "aafnx/omnibox-ai"

// archiveLimit bounds a downloaded file; the program archive is a few megabytes.
const archiveLimit = 256 << 20

// Updater talks to GitHub for one download.
type Updater struct {
	Token string
	// API is https://api.github.com; tests point it at a local server.
	API  string
	GOOS string
	HTTP *http.Client
	// Expires is when the token stops working, as GitHub reports it for expiring tokens.
	Expires string
}

// Release is the version to install and its files.
type Release struct {
	Tag    string
	Assets map[string]string // name → API URL
	// Notes and Published describe the release for the "check for updates" answer.
	Notes     string
	Published time.Time
	URL       string
}

// Version is the tag without "v".
func (r Release) Version() string { return strings.TrimPrefix(r.Tag, "v") }

func (u *Updater) api() string {
	if u.API != "" {
		return strings.TrimRight(u.API, "/")
	}
	return "https://api.github.com"
}

func (u *Updater) goos() string {
	if u.GOOS != "" {
		return u.GOOS
	}
	return runtime.GOOS
}

// ArchiveName is the program archive for the system.
func (u *Updater) ArchiveName() string {
	if u.goos() == "windows" {
		return "omnibox-windows-amd64.zip"
	}
	return "omnibox-" + u.goos() + "-amd64.tar.gz"
}

func (u *Updater) client() *http.Client {
	base := u.HTTP
	if base == nil {
		base = &http.Client{Timeout: 10 * time.Minute}
	}
	// Redirects are followed by hand, so the token never leaves api.github.com.
	c := *base
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

var versionPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+$`)

// Find returns the release with version ("0.19.0"), or the latest one when version is empty.
func (u *Updater) Find(ctx context.Context, version string) (Release, error) {
	path := "/repos/" + Repo + "/releases/latest"
	if version != "" {
		if !versionPattern.MatchString(version) {
			return Release{}, errors.New("Версия: x.y.z, например 0.19.0.")
		}
		path = "/repos/" + Repo + "/releases/tags/v" + strings.TrimPrefix(version, "v")
	}
	req, err := u.request(ctx, u.api()+path, "application/vnd.github+json", true)
	if err != nil {
		return Release{}, err
	}
	resp, err := u.client().Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("GitHub недоступен: %v", err)
	}
	defer resp.Body.Close()
	u.Expires = resp.Header.Get("GitHub-Authentication-Token-Expiration")
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return Release{}, errors.New("GitHub отклонил token: нужен token с правом чтения репозитория aafnx/omnibox-ai (Contents: Read).")
	case http.StatusNotFound:
		if version != "" {
			return Release{}, fmt.Errorf("Выпуска %s нет (или token не видит репозиторий).", version)
		}
		return Release{}, errors.New("Выпусков нет (или token не видит репозиторий).")
	default:
		return Release{}, fmt.Errorf("GitHub ответил %d.", resp.StatusCode)
	}
	var body struct {
		Tag       string    `json:"tag_name"`
		Body      string    `json:"body"`
		Published time.Time `json:"published_at"`
		HTMLURL   string    `json:"html_url"`
		Assets    []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return Release{}, fmt.Errorf("Ответ GitHub не прочитан: %v", err)
	}
	if !versionPattern.MatchString(body.Tag) {
		return Release{}, fmt.Errorf("Неожиданный тег выпуска: %q.", body.Tag)
	}
	r := Release{Tag: body.Tag, Assets: map[string]string{}, Notes: strings.TrimSpace(body.Body), Published: body.Published, URL: body.HTMLURL}
	if len(r.Notes) > 8000 {
		r.Notes = r.Notes[:8000] + "…"
	}
	for _, a := range body.Assets {
		r.Assets[a.Name] = a.URL
	}
	return r, nil
}

func (u *Updater) request(ctx context.Context, url, accept string, auth bool) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "omnibox-update")
	if auth && u.Token != "" {
		req.Header.Set("Authorization", "Bearer "+u.Token)
	}
	return req, nil
}

// download saves one release file; GitHub answers the API with a redirect to storage, which is
// fetched without the token.
func (u *Updater) download(ctx context.Context, r Release, name, path string) error {
	url, ok := r.Assets[name]
	if !ok {
		return fmt.Errorf("В выпуске %s нет файла %s.", r.Tag, name)
	}
	req, err := u.request(ctx, url, "application/octet-stream", true)
	if err != nil {
		return err
	}
	resp, err := u.client().Do(req)
	if err != nil {
		return fmt.Errorf("Не удалось скачать %s: %v", name, err)
	}
	if resp.StatusCode/100 == 3 {
		location := resp.Header.Get("Location")
		resp.Body.Close()
		if !strings.HasPrefix(location, "https://") {
			return fmt.Errorf("GitHub не выдал ссылку на %s.", name)
		}
		if req, err = u.request(ctx, location, "application/octet-stream", false); err != nil {
			return err
		}
		if resp, err = u.client().Do(req); err != nil {
			return fmt.Errorf("Не удалось скачать %s: %v", name, err)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Не удалось скачать %s: ответ %d.", name, resp.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(progress.Reader(ctx, resp.Body, max(resp.ContentLength, 0)), archiveLimit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > archiveLimit {
		err = fmt.Errorf("Файл %s слишком большой.", name)
	}
	return err
}

// Fetch downloads the release into dir, checks the archive against SHA256SUMS, unpacks it and
// returns the path of the new omnibox binary.
func (u *Updater) Fetch(ctx context.Context, r Release, dir string) (string, error) {
	archive := u.ArchiveName()
	sums := filepath.Join(dir, "SHA256SUMS")
	if err := u.download(ctx, r, "SHA256SUMS", sums); err != nil {
		return "", err
	}
	path := filepath.Join(dir, archive)
	if err := u.download(ctx, r, archive, path); err != nil {
		return "", err
	}
	want, err := expectedSum(sums, archive)
	if err != nil {
		return "", err
	}
	got, err := fileSum(path)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(got, want) {
		return "", errors.New("Контрольная сумма выпуска не совпадает; обновление отменено.")
	}
	pkg := filepath.Join(dir, "package")
	if err := enginemgr.Extract(path, pkg); err != nil {
		return "", fmt.Errorf("Архив выпуска не распакован: %v", err)
	}
	exe := filepath.Join(pkg, "omnibox")
	if u.goos() == "windows" {
		exe += ".exe"
	}
	if info, err := os.Stat(exe); err != nil || !info.Mode().IsRegular() {
		return "", errors.New("В архиве выпуска нет программы omnibox.")
	}
	return exe, nil
}

var sumLine = regexp.MustCompile(`^([0-9a-fA-F]{64}) [ *]?(\S+)$`)

func expectedSum(path, name string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if m := sumLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil && m[2] == name {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("В SHA256SUMS нет %s.", name)
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
