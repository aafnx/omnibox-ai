package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/platform"
	"github.com/aafnx/omnibox-ai/internal/selfupdate"
)

func TestUpdateTokenIsCheckedSavedAndUsed(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer github_pat_good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("GitHub-Authentication-Token-Expiration", "2027-10-08 10:00:00 UTC")
		if strings.HasSuffix(r.URL.Path, "/releases") {
			io.WriteString(w, `[{"tag_name":"v0.21.0","body":"- Новое"},{"tag_name":"v0.20.2"},{"tag_name":"v0.19.0"}]`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.21.0", "body": "- Новое", "published_at": "2026-10-09T08:00:00Z",
			"html_url": "https://github.com/aafnx/omnibox-ai/releases/tag/v0.21.0", "assets": []any{}})
	}))
	defer srv.Close()
	data := t.TempDir()
	p := &panelUpdate{Services: platform.NewFakeServices(), Data: data, Current: "0.20.2",
		NewUpdater: func(token string) *selfupdate.Updater {
			return &selfupdate.Updater{Token: token, API: srv.URL, HTTP: srv.Client()}
		}}
	ctx := context.Background()
	if _, err := p.Action(ctx, map[string]any{"action": "check"}); err != tokenMissing {
		t.Fatalf("check without a token: %v", err)
	}
	if _, err := p.Action(ctx, map[string]any{"action": "save-token", "token": "github_pat_bad"}); err == nil || fileExists(tokenPath(data)) {
		t.Fatalf("a refused token was saved: %v", err)
	}
	info, err := p.Action(ctx, map[string]any{"action": "save-token", "token": " github_pat_good "})
	if err != nil || info.(map[string]any)["token"] != true || info.(map[string]any)["token_expires"] != "2027-10-08 10:00:00 UTC" {
		t.Fatalf("%v %v", info, err)
	}
	// Neither the state nor the file on disk shows the token in the clear on Windows; the
	// update tab never gets it back.
	if b, _ := json.Marshal(p.UpdateInfo()); strings.Contains(string(b), "github_pat_good") {
		t.Fatal("the token is shown")
	}
	check, err := p.Action(ctx, map[string]any{"action": "check"})
	if r := check.(map[string]any); err != nil || r["latest"] != "0.21.0" || r["newer"] != true || r["notes"] != "- Новое" {
		t.Fatalf("%v %v", check, err)
	}
	// The versions to choose from: the installed one, newer and older ones.
	list, err := p.Action(ctx, map[string]any{"action": "list"})
	rel, _ := list.(map[string]any)["releases"].([]map[string]any)
	if err != nil || len(rel) != 3 || rel[0]["newer"] != true || rel[1]["current"] != true || rel[2]["newer"] != false || rel[2]["current"] != false {
		t.Fatalf("list: %v %v", list, err)
	}
	// The update button uses the saved token: the release has no files here, so the download
	// fails after GitHub accepted the token.
	if _, err := p.Start(ctx, "", ""); err == nil || err == tokenMissing || strings.Contains(err.Error(), "отклонил") {
		t.Fatalf("update with the saved token: %v", err)
	}
	if _, err := p.Action(ctx, map[string]any{"action": "forget-token"}); err != nil || fileExists(tokenPath(data)) {
		t.Fatalf("forget: %v", err)
	}
	if _, err := p.Start(ctx, "", ""); err != tokenMissing {
		t.Fatalf("update after forgetting: %v", err)
	}
	if _, err := p.Action(ctx, map[string]any{"action": "check", "token": "x"}); err == nil {
		t.Fatal("a token was taken with a check")
	}
}

func TestTokenLinkAndVersionOrder(t *testing.T) {
	u, err := url.Parse(tokenLink())
	if err != nil || u.Host != "github.com" || u.Path != "/settings/personal-access-tokens/new" {
		t.Fatal(tokenLink())
	}
	q := u.Query()
	if q.Get("contents") != "read" || q.Get("expires_in") != "365" || q.Get("target_name") != "aafnx" || q.Get("name") == "" {
		t.Fatal(q)
	}
	for _, c := range [][3]string{{"0.21.0", "0.20.9", "y"}, {"0.20.10", "0.20.9", "y"}, {"1.0.0", "0.99.99", "y"}, {"0.20.2", "0.20.2", ""}, {"0.20.1", "0.20.2", ""}} {
		if newer(c[0], c[1]) != (c[2] == "y") {
			t.Fatalf("%v", c)
		}
	}
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
