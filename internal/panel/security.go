// Package panel serves the web panel: static files, login, sessions and the panel API.
package panel

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/aafnx/omnibox-ai/internal/config"
)

// VerifyPassword checks a password against the stored PBKDF2-SHA256 hash in constant time.
func VerifyPassword(password string, p config.Password) bool {
	n := len(utf16.Encode([]rune(password)))
	if n < 1 || n > 256 {
		return false
	}
	salt, hash, err := p.Decode()
	if err != nil {
		return false
	}
	actual, err := pbkdf2.Key(sha256.New, password, salt, p.Iterations, 32)
	return err == nil && subtle.ConstantTimeCompare(actual, hash) == 1
}

// NewPassword hashes a new panel password (12 to 256 characters).
func NewPassword(password string) (config.Password, error) {
	n := len(utf16.Encode([]rune(password)))
	if n < 12 || n > 256 {
		return config.Password{}, errors.New("Пароль панели должен содержать от 12 до 256 символов.")
	}
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return config.Password{}, err
	}
	hash, err := pbkdf2.Key(sha256.New, password, salt, 210000, 32)
	if err != nil {
		return config.Password{}, err
	}
	return config.Password{Salt: base64.StdEncoding.EncodeToString(salt), Hash: base64.StdEncoding.EncodeToString(hash), Iterations: 210000}, nil
}

func newNonce() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // the system random source never fails on supported systems
	}
	return hex.EncodeToString(b)
}

var tokenPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// sessionKey stores only a hash of the cookie, so a leaked session store cannot be replayed.
func sessionKey(token string) string {
	if !tokenPattern.MatchString(token) {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// transport describes how the browser reached the panel: directly or through the NAS proxy.
type transport struct {
	secure    bool
	scheme    string
	authority string
	client    string
}

var errProxyHeaders = errors.New("proxy headers")

// requestTransport trusts X-Forwarded-* only from the configured NAS addresses.
func requestTransport(r *http.Request, p *config.Panel) (transport, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote := net.ParseIP(host)
	t := transport{scheme: "http", authority: r.Host, client: host}
	if remote != nil {
		if v4 := remote.To4(); v4 != nil {
			remote = v4
		}
		t.client = remote.String()
	}
	trusted := false
	for _, ip := range p.TrustedProxyIPs() {
		if remote != nil && ip.Equal(remote) {
			trusted = true
			break
		}
	}
	if !trusted {
		return t, nil
	}
	if r.Header.Get("X-Forwarded-Proto") != "https" {
		return t, errors.New("Доверенный reverse proxy должен передавать X-Forwarded-Proto: https.")
	}
	origin, _ := url.Parse(p.PublicOrigin)
	if fh := r.Header.Get("X-Forwarded-Host"); fh != "" && !strings.EqualFold(fh, origin.Host) {
		return t, errors.New("X-Forwarded-Host не совпадает с public_origin.")
	}
	t.secure, t.scheme, t.authority = true, "https", origin.Host
	// The proxy must overwrite this header with ONE address, never append an untrusted chain.
	if ip := net.ParseIP(r.Header.Get("X-Forwarded-For")); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		t.client = ip.String()
	}
	return t, nil
}

// originAllowed accepts no Origin (same-origin form posts) or exactly the panel's origin.
func originAllowed(origin, authority, scheme string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Scheme == scheme && strings.EqualFold(u.Host, authority) && (u.Path == "" || u.Path == "/") &&
		u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

// GeneratePassword returns a random 24-character panel password.
func GeneratePassword() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
