package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aafnx/omnibox-ai/internal/platform"
)

// The panel service runs as SYSTEM and cannot use the owner's gh login, which Windows keeps per
// user. By the owner's decision the panel keeps one GitHub token for updates: encrypted (DPAPI on
// Windows) in the data directory, readable by administrators only, used for GitHub releases and
// nothing else, never shown, logged or put on a command line. Uninstalling removes it.
type savedToken struct {
	Token   string    `json:"token"`
	Saved   time.Time `json:"saved"`
	Expires string    `json:"expires,omitempty"`
}

func tokenPath(data string) string { return filepath.Join(data, "run", "github-token.bin") }

func loadToken(data string) (savedToken, bool) {
	b, err := platform.ReadShared(tokenPath(data))
	if err != nil {
		return savedToken{}, false
	}
	plain, err := platform.Unprotect(b)
	if err != nil {
		return savedToken{}, false
	}
	var t savedToken
	if json.Unmarshal(plain, &t) != nil || t.Token == "" {
		return savedToken{}, false
	}
	return t, true
}

func saveToken(data string, t savedToken) error {
	plain, err := json.Marshal(t)
	if err != nil {
		return err
	}
	sealed, err := platform.Protect(plain)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(tokenPath(data)), 0o700); err != nil {
		return err
	}
	return platform.WriteAtomic(tokenPath(data), sealed, 0o600)
}

func forgetToken(data string) error {
	if err := os.Remove(tokenPath(data)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// tokenLink opens GitHub's form for a fine-grained token with this setup filled in: read-only
// access to repository contents, the owner aafnx, a year. Choosing the repository is left to the
// owner: GitHub does not take it from the link.
func tokenLink() string {
	q := url.Values{"name": {"OmniBox AI updates"}, "description": {"Проверка и скачивание выпусков OmniBox AI из панели (только чтение)"},
		"target_name": {"aafnx"}, "expires_in": {"365"}, "contents": {"read"}}
	return "https://github.com/settings/personal-access-tokens/new?" + q.Encode()
}

// newer reports whether version a is later than b (x.y.z).
func newer(a, b string) bool {
	pa, pb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < 3; i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// tokenMissing is what the panel says when it has no token yet.
var tokenMissing = errors.New("Нет GitHub token для обновлений: создайте его по ссылке «Создать токен на GitHub» и сохраните в панели. Без токена работает команда ai update в терминале (через вход в gh).")

// UpdateInfo is the update tab's state: the installed version and the saved token, never the token.
func (p *panelUpdate) UpdateInfo() map[string]any {
	info := map[string]any{"current": p.Current, "token": false, "token_link": tokenLink()}
	if t, ok := loadToken(p.Data); ok {
		info["token"], info["token_saved"], info["token_expires"] = true, t.Saved, t.Expires
	}
	return info
}

// Check asks GitHub for the latest release with the saved token.
func (p *panelUpdate) Check(ctx context.Context) (map[string]any, error) {
	t, ok := loadToken(p.Data)
	if !ok {
		return nil, tokenMissing
	}
	u := p.NewUpdater(t.Token)
	r, err := u.Find(ctx, "")
	if err != nil {
		return nil, err
	}
	if u.Expires != "" && u.Expires != t.Expires {
		t.Expires = u.Expires
		_ = saveToken(p.Data, t)
	}
	return map[string]any{"current": p.Current, "latest": r.Version(), "newer": newer(r.Version(), p.Current), "notes": r.Notes,
		"published": r.Published, "url": r.URL, "token_expires": t.Expires}, nil
}

// Versions lists the published releases for the "other version or rollback" list: each marked
// as the installed one, newer or older.
func (p *panelUpdate) Versions(ctx context.Context) (map[string]any, error) {
	t, ok := loadToken(p.Data)
	if !ok {
		return nil, tokenMissing
	}
	list, err := p.NewUpdater(t.Token).List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(list))
	for _, r := range list {
		out = append(out, map[string]any{"version": r.Version, "published": r.Published, "summary": r.Summary, "prerelease": r.Prerelease, "url": r.URL,
			"current": r.Version == p.Current, "newer": newer(r.Version, p.Current)})
	}
	return map[string]any{"current": p.Current, "releases": out}, nil
}

// SaveToken keeps a token after GitHub accepted it for the repository's releases.
func (p *panelUpdate) SaveToken(ctx context.Context, token string) (map[string]any, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 400 || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("Вставьте GitHub token целиком.")
	}
	u := p.NewUpdater(token)
	if _, err := u.Find(ctx, ""); err != nil {
		return nil, fmt.Errorf("Токен не сохранён: %v", err)
	}
	if err := saveToken(p.Data, savedToken{Token: token, Saved: time.Now().UTC(), Expires: u.Expires}); err != nil {
		return nil, err
	}
	return p.UpdateInfo(), nil
}

// Action handles the update tab's requests: check, save-token, forget-token.
func (p *panelUpdate) Action(ctx context.Context, body map[string]any) (any, error) {
	action, _ := body["action"].(string)
	for key := range body {
		if key != "action" && !(key == "token" && action == "save-token") {
			return nil, errors.New("Неизвестное поле: " + key)
		}
	}
	switch action {
	case "check":
		return p.Check(ctx)
	case "list":
		return p.Versions(ctx)
	case "save-token":
		token, _ := body["token"].(string)
		return p.SaveToken(ctx, token)
	case "forget-token":
		if err := forgetToken(p.Data); err != nil {
			return nil, err
		}
		return p.UpdateInfo(), nil
	}
	return nil, errors.New("Неизвестное действие обновления.")
}
