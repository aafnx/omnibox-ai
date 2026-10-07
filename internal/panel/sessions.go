package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Sessions survive a restart of the service (an update restarts it): the file holds only hashes
// of the cookies, never the cookies, and is encrypted with DPAPI on Windows.
type savedSessions struct {
	// Password marks the password the sessions were opened with; after a reset the file is void.
	Password string         `json:"password"`
	Sessions []savedSession `json:"sessions"`
}

type savedSession struct {
	Key     string    `json:"key"`
	CSRF    string    `json:"csrf"`
	Secure  bool      `json:"secure"`
	Expires time.Time `json:"expires"`
}

func passwordMark(c *config.Config) string {
	sum := sha256.Sum256([]byte(c.Panel.Password.Salt + "/" + c.Panel.Password.Hash))
	return hex.EncodeToString(sum[:16])
}

// loadSessions restores the saved sessions; a damaged, foreign or outdated file means the owner
// signs in again, never a panel error.
func (s *Server) loadSessions() {
	if s.opt.SessionsPath == "" {
		return
	}
	sealed, err := platform.ReadShared(s.opt.SessionsPath)
	if err != nil {
		return
	}
	plain, err := platform.Unprotect(sealed)
	if err != nil {
		s.logf("сессии панели не прочитаны: %v", err)
		return
	}
	var saved savedSessions
	if json.Unmarshal(plain, &saved) != nil || saved.Password != passwordMark(s.opt.Config()) {
		return
	}
	now := s.opt.Now().UTC()
	for _, v := range saved.Sessions {
		// The absolute expiry is kept: a restart never makes a session last longer.
		if !tokenPattern.MatchString(v.Key) || !tokenPattern.MatchString(v.CSRF) || !v.Expires.After(now) || v.Expires.After(now.Add(sessionLifetime)) {
			continue
		}
		if len(s.sessions) < maxSessions {
			s.sessions[v.Key] = &session{csrf: v.CSRF, secure: v.Secure, expires: v.Expires}
		}
	}
}

// saveSessionsLocked writes the current sessions; the caller holds s.mu.
func (s *Server) saveSessionsLocked() {
	if s.opt.SessionsPath == "" {
		return
	}
	saved := savedSessions{Password: passwordMark(s.opt.Config()), Sessions: []savedSession{}}
	for k, v := range s.sessions {
		saved.Sessions = append(saved.Sessions, savedSession{Key: k, CSRF: v.csrf, Secure: v.secure, Expires: v.expires})
	}
	plain, err := json.Marshal(saved)
	if err == nil {
		var sealed []byte
		if sealed, err = platform.Protect(plain); err == nil {
			if err = os.MkdirAll(filepath.Dir(s.opt.SessionsPath), 0o700); err == nil {
				err = platform.WriteAtomic(s.opt.SessionsPath, sealed, 0o600)
			}
		}
	}
	if err != nil {
		s.logf("сессии панели не сохранены: %v", err)
	}
}
