package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// Account is the signed-in ChatGPT account.
type Account struct {
	Email string `json:"email"`
	Plan  string `json:"plan"`
}

// Login is a device-code sign-in in progress: the owner opens URL and enters Code.
type Login struct {
	ID      string    `json:"-"`
	URL     string    `json:"url"`
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
	Done    bool      `json:"done"`
	Error   string    `json:"error,omitempty"`
}

// loginTTL is how long OpenAI keeps a device code.
const loginTTL = 15 * time.Minute

// Window is one limit of the plan: the share used and when it resets.
type Window struct {
	UsedPercent int        `json:"used_percent"`
	Minutes     int64      `json:"window_minutes,omitempty"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
}

// Bucket is a set of limits; the plan has the main one and may have others for some models.
type Bucket struct {
	ID        string  `json:"id,omitempty"`
	Name      string  `json:"name,omitempty"`
	Primary   *Window `json:"primary,omitempty"`
	Secondary *Window `json:"secondary,omitempty"`
}

// Limits are the subscription's limits as OpenAI last reported them.
type Limits struct {
	Plan string `json:"plan,omitempty"`
	Bucket
	Others []Bucket `json:"others,omitempty"`
	// Credits are bought on top of the plan.
	Credits *Credits `json:"credits,omitempty"`
	// Reached names the limit that stopped answers; empty while answers are allowed.
	Reached string    `json:"reached,omitempty"`
	Blocked bool      `json:"blocked,omitempty"`
	Updated time.Time `json:"updated"`
}

// Credits are the account's extra credits.
type Credits struct {
	HasCredits bool   `json:"has_credits"`
	Unlimited  bool   `json:"unlimited"`
	Balance    string `json:"balance,omitempty"`
}

type window struct {
	UsedPercent int    `json:"usedPercent"`
	Minutes     *int64 `json:"windowDurationMins"`
	ResetsAt    *int64 `json:"resetsAt"`
}

type snapshot struct {
	LimitID   string  `json:"limitId"`
	LimitName string  `json:"limitName"`
	Primary   *window `json:"primary"`
	Secondary *window `json:"secondary"`
	Credits   *struct {
		HasCredits bool   `json:"hasCredits"`
		Unlimited  bool   `json:"unlimited"`
		Balance    string `json:"balance"`
	} `json:"credits"`
	SpendControlReached *bool  `json:"spendControlReached"`
	PlanType            string `json:"planType"`
	ReachedType         string `json:"rateLimitReachedType"`
}

func (w *window) public() *Window {
	if w == nil {
		return nil
	}
	out := &Window{UsedPercent: min(max(w.UsedPercent, 0), 100)}
	if w.Minutes != nil {
		out.Minutes = *w.Minutes
	}
	if w.ResetsAt != nil && *w.ResetsAt > 0 {
		t := time.Unix(*w.ResetsAt, 0).UTC()
		out.ResetsAt = &t
	}
	return out
}

func (b snapshot) bucket() Bucket {
	return Bucket{ID: b.LimitID, Name: b.LimitName, Primary: b.Primary.public(), Secondary: b.Secondary.public()}
}

// mergeLimits applies a snapshot: the main bucket ("codex") replaces the main limits, another
// bucket replaces its own entry. A snapshot without windows (a turn of another provider) keeps
// what was known.
func mergeLimits(old *Limits, s snapshot, now time.Time) *Limits {
	out := &Limits{}
	if old != nil {
		c := *old
		c.Others = slices.Clone(old.Others)
		out = &c
	}
	b := s.bucket()
	if b.Primary == nil && b.Secondary == nil && s.Credits == nil && s.ReachedType == "" {
		return old
	}
	if s.LimitID == "" || s.LimitID == "codex" || (out.Primary == nil && out.Secondary == nil) {
		out.Bucket = b
		if s.PlanType != "" {
			out.Plan = s.PlanType
		}
		if s.Credits != nil {
			out.Credits = &Credits{HasCredits: s.Credits.HasCredits, Unlimited: s.Credits.Unlimited, Balance: s.Credits.Balance}
		}
		out.Reached = s.ReachedType
		out.Blocked = s.SpendControlReached != nil && *s.SpendControlReached
	} else {
		i := slices.IndexFunc(out.Others, func(o Bucket) bool { return o.ID == b.ID })
		if i >= 0 {
			out.Others[i] = b
		} else {
			out.Others = append(out.Others, b)
		}
	}
	out.Updated = now.UTC()
	return out
}

// Status is the ChatGPT section of the panel.
type Status struct {
	Installed bool     `json:"installed"`
	Version   string   `json:"version,omitempty"`
	Running   bool     `json:"running"`
	Account   *Account `json:"account,omitempty"`
	Login     *Login   `json:"login,omitempty"`
	Limits    *Limits  `json:"limits,omitempty"`
	// Installing is a download in progress or the failure of the last one.
	Installing *Installing `json:"installing,omitempty"`
}

// Quick is what is known without starting anything.
func (s *Service) Quick() Status {
	_, tag := s.Installed()
	st := Status{Installed: tag != "", Version: tag}
	s.mu.Lock()
	st.Running = s.c != nil && s.c.alive()
	s.mu.Unlock()
	s.state.Lock()
	defer s.state.Unlock()
	if s.account != nil {
		a := *s.account
		st.Account = &a
	}
	if s.login != nil {
		l := *s.login
		st.Login = &l
	}
	if s.limits != nil {
		l := *s.limits
		st.Limits = &l
	}
	if s.installing != nil {
		i := *s.installing
		st.Installing = &i
	}
	return st
}

// SignedIn reports whether an account was seen signed in; it does not start the app-server.
func (s *Service) SignedIn() bool {
	if exe, _ := s.Installed(); exe == "" {
		return false
	}
	s.state.Lock()
	known := s.account != nil
	s.state.Unlock()
	if known {
		return true
	}
	// After a restart of the service the sign-in is in Codex's home until it is read again.
	_, err := os.Stat(filepath.Join(s.home(), "auth.json"))
	return err == nil
}

// Account asks the app-server who is signed in; nil means nobody.
func (s *Service) Account(ctx context.Context) (*Account, error) {
	c, release, err := s.use(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var out struct {
		Account *struct {
			Type     string `json:"type"`
			Email    string `json:"email"`
			PlanType string `json:"planType"`
		} `json:"account"`
	}
	if err := c.call(ctx, "account/read", map[string]any{"refreshToken": false}, &out); err != nil {
		return nil, explain(err)
	}
	s.state.Lock()
	defer s.state.Unlock()
	if out.Account == nil || out.Account.Type != "chatgpt" {
		s.account = nil
		return nil, nil
	}
	s.account = &Account{Email: out.Account.Email, Plan: out.Account.PlanType}
	a := *s.account
	return &a, nil
}

// StartLogin asks OpenAI for a device code; the owner signs in on any device.
func (s *Service) StartLogin(ctx context.Context) (*Login, error) {
	c, release, err := s.use(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var out struct {
		LoginID string `json:"loginId"`
		URL     string `json:"verificationUrl"`
		Code    string `json:"userCode"`
	}
	if err := c.call(ctx, "account/login/start", map[string]any{"type": "chatgptDeviceCode"}, &out); err != nil {
		return nil, explain(err)
	}
	if !strings.HasPrefix(out.URL, "https://") || out.Code == "" {
		return nil, errors.New("OpenAI не выдал код входа. Повторите позже.")
	}
	l := &Login{ID: out.LoginID, URL: out.URL, Code: out.Code, Expires: s.now().Add(loginTTL).UTC()}
	s.state.Lock()
	s.login = l
	if s.early != nil && s.early.LoginID == l.ID {
		s.early.apply(l)
	}
	s.early = nil
	shown := *l
	s.state.Unlock()
	return &shown, nil
}

// CancelLogin drops a sign-in in progress.
func (s *Service) CancelLogin(ctx context.Context) error {
	s.state.Lock()
	l := s.login
	s.login = nil
	s.state.Unlock()
	if l == nil || l.Done {
		return nil
	}
	s.mu.Lock()
	c := s.c
	s.mu.Unlock()
	if c == nil || !c.alive() {
		return nil
	}
	return explain(c.call(ctx, "account/login/cancel", map[string]any{"loginId": l.ID}, nil))
}

// Logout signs the account out; Codex deletes its tokens.
func (s *Service) Logout(ctx context.Context) error {
	c, release, err := s.use(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := c.call(ctx, "account/logout", nil, nil); err != nil {
		return explain(err)
	}
	s.state.Lock()
	s.account, s.limits, s.login = nil, nil, nil
	s.state.Unlock()
	return nil
}

// Limits asks OpenAI for the subscription's limits.
func (s *Service) Limits(ctx context.Context) (*Limits, error) {
	c, release, err := s.use(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var out struct {
		Allowed  *bool               `json:"ordinaryUsageAllowed"`
		Main     snapshot            `json:"rateLimits"`
		ByID     map[string]snapshot `json:"rateLimitsByLimitId"`
		Accounts string              `json:"accountId"`
	}
	if err := c.call(ctx, "account/rateLimits/read", nil, &out); err != nil {
		return nil, explain(err)
	}
	now := s.now()
	l := mergeLimits(nil, out.Main, now)
	if l == nil {
		l = &Limits{Plan: out.Main.PlanType, Updated: now.UTC()}
	}
	ids := make([]string, 0, len(out.ByID))
	for id := range out.ByID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		b := out.ByID[id]
		if id == out.Main.LimitID || b.LimitID == out.Main.LimitID || (b.Primary == nil && b.Secondary == nil) {
			continue
		}
		if b.LimitID == "" {
			b.LimitID = id
		}
		l.Others = append(l.Others, b.bucket())
	}
	if out.Allowed != nil && !*out.Allowed && l.Reached == "" {
		l.Reached = "limit"
	}
	s.state.Lock()
	s.limits = l
	shown := *l
	s.state.Unlock()
	return &shown, nil
}

// Model is a model of the subscription.
type Model struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Efforts     []string `json:"efforts,omitempty"`
	Default     string   `json:"default_effort,omitempty"`
	Images      bool     `json:"images"`
	IsDefault   bool     `json:"is_default,omitempty"`
}

// noEffort are reasoning levels the chat does not offer: "ultra" hands the task to sub-agents.
var noEffort = map[string]bool{"ultra": true}

// Models lists the models of the subscription; the list is kept while the app-server runs.
func (s *Service) Models(ctx context.Context) ([]Model, error) {
	s.state.Lock()
	cached := s.models
	s.state.Unlock()
	if cached != nil {
		return cached, nil
	}
	c, release, err := s.use(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var out []Model
	cursor := ""
	for page := 0; page < 10; page++ {
		params := map[string]any{"includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var res struct {
			Data []struct {
				ID          string `json:"id"`
				Model       string `json:"model"`
				DisplayName string `json:"displayName"`
				Description string `json:"description"`
				Hidden      bool   `json:"hidden"`
				Efforts     []struct {
					Effort string `json:"reasoningEffort"`
				} `json:"supportedReasoningEfforts"`
				Default    string   `json:"defaultReasoningEffort"`
				Modalities []string `json:"inputModalities"`
				IsDefault  bool     `json:"isDefault"`
			} `json:"data"`
			Next string `json:"nextCursor"`
		}
		if err := c.call(ctx, "model/list", params, &res); err != nil {
			return nil, explain(err)
		}
		for _, m := range res.Data {
			id := m.Model
			if id == "" {
				id = m.ID
			}
			if m.Hidden || !ValidModel(id) {
				continue
			}
			x := Model{ID: id, Name: m.DisplayName, Description: m.Description, Default: m.Default,
				Images: slices.Contains(m.Modalities, "image"), IsDefault: m.IsDefault}
			if x.Name == "" {
				x.Name = id
			}
			for _, e := range m.Efforts {
				if e.Effort != "" && !noEffort[e.Effort] {
					x.Efforts = append(x.Efforts, e.Effort)
				}
			}
			out = append(out, x)
		}
		if res.Next == "" {
			break
		}
		cursor = res.Next
	}
	if out == nil {
		out = []Model{}
	}
	s.state.Lock()
	s.models = out
	s.state.Unlock()
	if data, err := json.Marshal(out); err == nil {
		if err := platform.WriteAtomic(filepath.Join(s.Dir, modelsFile), data, 0o600); err != nil {
			s.logf("chatgpt: model list not kept: %v", err)
		}
	}
	return out, nil
}

// modelsFile keeps the last model list, so the panel lists the models without starting Codex.
const modelsFile = "models.json"

// CachedModels is the model list without starting anything: the one read while Codex last ran.
func (s *Service) CachedModels() []Model {
	s.state.Lock()
	cached := s.models
	s.state.Unlock()
	if cached != nil {
		return cached
	}
	data, err := platform.ReadShared(filepath.Join(s.Dir, modelsFile))
	var out []Model
	if err != nil || json.Unmarshal(data, &out) != nil {
		return nil
	}
	return slices.DeleteFunc(out, func(m Model) bool { return !ValidModel(m.ID) })
}

// ValidModel accepts a Codex model id such as gpt-5.5 or gpt-6.1-sol.
func ValidModel(id string) bool { return config.ValidChatGPTModel(id) }

// explain turns an app-server error into words for the owner.
func explain(err error) error {
	if err == nil {
		return nil
	}
	var re *rpcError
	if errors.As(err, &re) {
		msg := re.Message
		low := strings.ToLower(msg)
		switch {
		case strings.Contains(low, "authentication required"), strings.Contains(low, "not logged in"), strings.Contains(low, "unauthorized"):
			return ErrSignedOut
		case strings.Contains(low, "device code"):
			return errors.New("Не удалось получить код входа у OpenAI: " + clip(msg, 200))
		}
		return errors.New("ChatGPT: " + clip(msg, 300))
	}
	return err
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
