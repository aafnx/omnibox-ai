package panel

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
)

func TestForwardedClientIsTheAddressTheNASSaw(t *testing.T) {
	for in, want := range map[string]string{"10.8.0.5": "10.8.0.5", "1.2.3.4, 10.8.0.5": "10.8.0.5", " 10.8.0.5 ": "10.8.0.5", "x": "", "::ffff:10.8.0.5": "10.8.0.5"} {
		got := forwardedClient([]string{in})
		if (got == nil && want != "") || (got != nil && got.String() != want) {
			t.Fatalf("%q: %v", in, got)
		}
	}
	if got := forwardedClient([]string{"6.6.6.6", "10.8.0.5"}); got.String() != "10.8.0.5" {
		t.Fatalf("two headers: %v", got)
	}
}

func TestForgedForwardingDoesNotLockOutOthers(t *testing.T) {
	s := newServer(t, testConfig(t, func(c *config.Config) {
		c.Panel.PublicOrigin = "https://llm.fnx.red"
		c.Panel.TrustedProxies = []string{"192.168.1.10"}
	}))
	via := func(xff string) func(r *http.Request) {
		return func(r *http.Request) {
			r.RemoteAddr, r.Host = "192.168.1.10:40000", "llm.fnx.red"
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("X-Forwarded-For", xff)
		}
	}
	// An attacker claims the owner's address in front of its own; the NAS appends the real one.
	for range maxAttempts + 1 {
		call(s, "POST", "/api/login", `{"password":"wrong"}`, via("10.8.0.2, 10.8.0.66"))
	}
	if w := call(s, "POST", "/api/login", `{"password":"`+testPassword+`"}`, via("10.8.0.2")); w.Code != 200 {
		t.Fatalf("the owner is locked out by a forged header: %d", w.Code)
	}
}

func TestLoginJournalAndConcurrencyLimit(t *testing.T) {
	c := testConfig(t, nil)
	log := filepath.Join(t.TempDir(), "panel.log")
	s, err := New(Options{Config: func() *config.Config { return c }, LogPath: log})
	if err != nil {
		t.Fatal(err)
	}
	call(s, "POST", "/api/login", `{"password":"wrong-секрет"}`, nil)
	login(t, s, nil)
	logged, _ := os.ReadFile(log)
	if !strings.Contains(string(logged), "неверный пароль с 192.168.1.20") || !strings.Contains(string(logged), "успешно с 192.168.1.20") ||
		strings.Contains(string(logged), "секрет") || strings.Contains(string(logged), testPassword) {
		t.Fatalf("journal: %q", logged)
	}
	for range maxVerifying {
		s.verify <- struct{}{}
	}
	if w := call(s, "POST", "/api/login", `{"password":"`+testPassword+`"}`, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("busy: %d", w.Code)
	}
}
