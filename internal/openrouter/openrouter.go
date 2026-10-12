// Package openrouter is the client of OpenRouter (https://openrouter.ai/api/v1), the cloud models
// used by the panel chat and by /v1 next to the local engines.
//
// The API key (owner's decision): it is stored on the PC only encrypted with platform.Protect
// (DPAPI on Windows; on Linux the file is root-only) in <data>/run/openrouter-key.bin, the
// directory 0700 and the file 0600 (KeyStore). The key is sent only to openrouter.ai, in the
// Authorization header; it is never returned to the panel (only KeyStore's Hint is), never logged,
// never put on a command line or into an error message. The owner removes it with a button in the
// panel, and uninstall removes it with the data directory.
//
// Nothing here runs on a timer: the model list (Catalog), the account and the spend ledger (Spend)
// are read when the panel asks.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aafnx/omnibox-ai/internal/reasoning"
)

// DefaultBaseURL is the OpenRouter API.
const DefaultBaseURL = "https://openrouter.ai/api/v1"

// Attribution headers OpenRouter shows in its activity list.
const (
	referer = "https://github.com/aafnx/omnibox-ai"
	title   = "OmniBox AI"
)

const (
	errorBodyLimit = 64 << 10
	jsonLimit      = 32 << 20 // the model list is a few megabytes
	detailLimit    = 300
)

// defaultTransport waits up to 10 minutes for the first byte: a deep reasoning model may think
// that long before it answers. The whole request has no deadline, so a long stream is not cut.
var defaultTransport = &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: 10 * time.Minute,
	IdleConnTimeout:       90 * time.Second,
	MaxIdleConns:          10,
	ForceAttemptHTTP2:     true,
}

// Client sends requests to OpenRouter with one key.
type Client struct {
	Key string
	// BaseURL is DefaultBaseURL when empty; tests point it at a local server.
	BaseURL string
	// HTTP is a client with sane timeouts when nil. Redirects are never followed, whatever
	// client is given, so the key never leaves OpenRouter.
	HTTP *http.Client
}

// Error is a failed OpenRouter request. Error() is the Russian text for the panel; the gateway
// builds its English /v1 error from Status and Detail.
type Error struct {
	Status  int    // HTTP status, 0 when OpenRouter was not reached
	Message string // Russian, for the panel
	Detail  string // OpenRouter's own message, without the key; may be empty
}

func (e *Error) Error() string {
	switch {
	case e.Detail == "", e.Status == http.StatusUnauthorized, e.Status == http.StatusPaymentRequired:
		return e.Message
	}
	return e.Message + " Ответ OpenRouter: " + e.Detail
}

func (c *Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return DefaultBaseURL
}

func (c *Client) client() *http.Client {
	h := http.Client{Transport: defaultTransport}
	if c.HTTP != nil {
		h = *c.HTTP
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &h
}

// scrub removes the key from s, in case a library message ever carries it.
func (c *Client) scrub(s string) string {
	if c.Key == "" {
		return s
	}
	return strings.ReplaceAll(s, c.Key, "***")
}

// do sends one request and returns a 2xx response; other statuses become *Error.
func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, rd)
	if err != nil {
		return nil, &Error{Message: "Запрос к OpenRouter не создан: " + c.scrub(err.Error())}
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	req.Header.Set("HTTP-Referer", referer)
	req.Header.Set("X-Title", title)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Message: "OpenRouter недоступен: " + c.scrub(err.Error())}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
	return nil, c.statusError(resp.StatusCode, data)
}

// statusError turns a failed status into a Russian message. OpenRouter's own message is kept
// only when it does not contain the key.
func (c *Client) statusError(status int, body []byte) *Error {
	e := &Error{Status: status}
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		d := strings.Join(strings.Fields(parsed.Error.Message), " ")
		if c.Key == "" || !strings.Contains(d, c.Key) {
			if len(d) > detailLimit {
				d = strings.ToValidUTF8(d[:detailLimit], "") + "…"
			}
			e.Detail = d
		}
	}
	switch status {
	case http.StatusUnauthorized:
		e.Message = "Ключ OpenRouter не принят. Проверьте его в настройках."
	case http.StatusPaymentRequired:
		e.Message = "На счёте OpenRouter закончились деньги. Пополните баланс."
	case http.StatusTooManyRequests:
		e.Message = "OpenRouter ограничил частоту запросов. Подождите немного и повторите."
	default:
		e.Message = fmt.Sprintf("OpenRouter ответил %d.", status)
	}
	return e
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, jsonLimit)).Decode(v); err != nil {
		return &Error{Status: resp.StatusCode, Message: "Ответ OpenRouter не прочитан: " + c.scrub(err.Error())}
	}
	return nil
}

// Caps are what a model accepts and produces, for the chat's attachment buttons and badges.
type Caps struct {
	Vision    bool `json:"vision"`
	Audio     bool `json:"audio"`
	Video     bool `json:"video"`
	Files     bool `json:"files"`
	Reasoning bool `json:"reasoning"`
	Tools     bool `json:"tools"`
	Draws     bool `json:"draws"`
}

// Model is one OpenRouter model.
type Model struct {
	ID string `json:"id"`
	// Name is OpenRouter's display name, "Anthropic: Claude Sonnet 4.5".
	Name string `json:"name"`
	// Provider is the part of Name before ":", or the first segment of ID.
	Provider string `json:"provider"`
	Context  int    `json:"context"`
	// PromptUSD and CompletionUSD are dollars per million tokens; negative when the price
	// depends on the routed model (openrouter/auto).
	PromptUSD     float64           `json:"prompt_usd"`
	CompletionUSD float64           `json:"completion_usd"`
	Input         []string          `json:"input"`
	Output        []string          `json:"output"`
	Supported     []string          `json:"supported"`
	Caps          Caps              `json:"caps"`
	Reasoning     reasoning.Profile `json:"reasoning"`
}

// finish derives Provider, Caps and Reasoning from the raw fields. It runs on every load too, so
// a cached list follows the rules of the running version.
func (m *Model) finish() {
	if before, _, ok := strings.Cut(m.Name, ":"); ok && strings.TrimSpace(before) != "" {
		m.Provider = strings.TrimSpace(before)
	} else {
		m.Provider, _, _ = strings.Cut(m.ID, "/")
	}
	m.Reasoning = reasoning.ForCloud(m.ID, m.Supported)
	has := func(list []string, v string) bool {
		for _, s := range list {
			if s == v {
				return true
			}
		}
		return false
	}
	m.Caps = Caps{
		Vision:    has(m.Input, "image"),
		Audio:     has(m.Input, "audio"),
		Video:     has(m.Input, "video"),
		Files:     has(m.Input, "file"),
		Draws:     has(m.Output, "image"),
		Tools:     has(m.Supported, "tools"),
		Reasoning: m.Reasoning.Mode != reasoning.ModeNone,
	}
}

// perMillion turns OpenRouter's price per token ("0.000003") into dollars per million tokens.
func perMillion(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	if v < 0 {
		return -1
	}
	// Rounded to a millionth of a dollar to hide float noise (3.0000000000000004).
	return math.Round(v*1e12) / 1e6
}

// Models returns the models OpenRouter offers now (GET /models).
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	var body struct {
		Data []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Context      int    `json:"context_length"`
			Architecture struct {
				Input  []string `json:"input_modalities"`
				Output []string `json:"output_modalities"`
			} `json:"architecture"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
			Supported []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, "/models", &body); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(body.Data))
	for _, d := range body.Data {
		if d.ID == "" {
			continue
		}
		m := Model{ID: d.ID, Name: d.Name, Context: d.Context, Input: d.Architecture.Input, Output: d.Architecture.Output,
			Supported: d.Supported, PromptUSD: perMillion(d.Pricing.Prompt), CompletionUSD: perMillion(d.Pricing.Completion)}
		if m.Name == "" {
			m.Name = m.ID
		}
		m.finish()
		models = append(models, m)
	}
	return models, nil
}

// Account is the key's spending and the account balance, in dollars.
type Account struct {
	Label        string  `json:"label"`
	TotalCredits float64 `json:"total_credits"`
	TotalUsage   float64 `json:"total_usage"`
	// Balance is TotalCredits - TotalUsage.
	Balance float64 `json:"balance"`
	// Usage is all spending through this key; Daily, Weekly and Monthly are the current UTC
	// day, week and month.
	Usage   float64 `json:"usage"`
	Daily   float64 `json:"daily"`
	Weekly  float64 `json:"weekly"`
	Monthly float64 `json:"monthly"`
	// Limit is the key's spending limit, nil when unlimited; LimitRemaining is what is left of it.
	Limit          *float64 `json:"limit"`
	LimitRemaining *float64 `json:"limit_remaining"`
	// LimitReset is how often the limit starts over (daily, weekly, monthly), empty when never.
	LimitReset string `json:"limit_reset,omitempty"`
	// ExpiresAt is when the key stops working, nil when it does not expire.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	FreeTier  bool       `json:"free_tier"`
}

// Account reads the key (GET /key) and the account credits (GET /credits).
func (c *Client) Account(ctx context.Context) (Account, error) {
	var key struct {
		Data struct {
			Label          string   `json:"label"`
			Usage          float64  `json:"usage"`
			Limit          *float64 `json:"limit"`
			LimitRemaining *float64 `json:"limit_remaining"`
			LimitReset     *string  `json:"limit_reset"`
			ExpiresAt      *string  `json:"expires_at"`
			Daily          float64  `json:"usage_daily"`
			Weekly         float64  `json:"usage_weekly"`
			Monthly        float64  `json:"usage_monthly"`
			FreeTier       bool     `json:"is_free_tier"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, "/key", &key); err != nil {
		return Account{}, err
	}
	var credits struct {
		Data struct {
			Total float64 `json:"total_credits"`
			Usage float64 `json:"total_usage"`
		} `json:"data"`
	}
	if err := c.getJSON(ctx, "/credits", &credits); err != nil {
		return Account{}, err
	}
	k := key.Data
	acc := Account{
		Label: k.Label, Usage: k.Usage, Daily: k.Daily, Weekly: k.Weekly, Monthly: k.Monthly,
		Limit: k.Limit, LimitRemaining: k.LimitRemaining, FreeTier: k.FreeTier,
		TotalCredits: credits.Data.Total, TotalUsage: credits.Data.Usage, Balance: credits.Data.Total - credits.Data.Usage,
	}
	if k.LimitReset != nil {
		acc.LimitReset = *k.LimitReset
	}
	// An expiry OpenRouter writes in a form we cannot read is left out rather than guessed.
	if k.ExpiresAt != nil {
		if t, err := time.Parse(time.RFC3339, *k.ExpiresAt); err == nil {
			acc.ExpiresAt = &t
		}
	}
	return acc, nil
}

// Chat sends a chat completion request (POST /chat/completions) as is, streaming or not, and
// returns the 2xx response for the caller to read and close. Other statuses become *Error.
func (c *Client) Chat(ctx context.Context, body []byte) (*http.Response, error) {
	if body == nil {
		return nil, errors.New("Пустой запрос к OpenRouter.")
	}
	return c.do(ctx, http.MethodPost, "/chat/completions", body)
}
