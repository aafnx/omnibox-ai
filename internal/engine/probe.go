package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aafnx/omnibox-ai/internal/config"
)

// Client talks to an engine's local API. Probes are short: status must never hang the panel.
type Client struct {
	BaseURL     string
	UpstreamKey string
	HTTP        *http.Client
}

// NewClient returns a client for the engine's loopback address.
func NewClient(e *config.Engine) *Client {
	return &Client{BaseURL: e.BaseURL(), HTTP: &http.Client{Timeout: 2 * time.Second, CheckRedirect: noRedirects}}
}

func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if c.UpstreamKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.UpstreamKey)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// Get decodes a JSON GET response.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// Post sends JSON and decodes the response; timeout overrides the probe timeout for long calls.
func (c *Client) Post(ctx context.Context, path string, body any, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	long := *c
	long.HTTP = &http.Client{CheckRedirect: noRedirects}
	return long.do(ctx, http.MethodPost, path, body, out)
}

// Delete sends a JSON DELETE.
func (c *Client) Delete(ctx context.Context, path string, body any) error {
	return c.do(ctx, http.MethodDelete, path, body, nil)
}

// Ready reports whether the engine's API answers: /health for PrismML, /api/version for Ollama.
func Ready(ctx context.Context, name string, c *Client) bool {
	if name == config.PrismML {
		var h struct {
			Status string `json:"status"`
		}
		return c.Get(ctx, "/health", &h) == nil && h.Status == "ok"
	}
	var v struct {
		Version string `json:"version"`
	}
	return c.Get(ctx, "/api/version", &v) == nil && v.Version != ""
}

// PrismProps is the part of llama-server's /props the panel uses.
type PrismProps struct {
	IsSleeping                bool   `json:"is_sleeping"`
	ModelPath                 string `json:"model_path"`
	DefaultGenerationSettings struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
}

// OllamaLoaded is one model in Ollama's /api/ps.
type OllamaLoaded struct {
	Name          string `json:"name"`
	Size          int64  `json:"size"`
	SizeVRAM      int64  `json:"size_vram"`
	ExpiresAt     string `json:"expires_at"`
	ContextLength int    `json:"context_length"`
}

// OllamaPS returns the loaded models.
func (c *Client) OllamaPS(ctx context.Context) ([]OllamaLoaded, error) {
	var ps struct {
		Models []OllamaLoaded `json:"models"`
	}
	err := c.Get(ctx, "/api/ps", &ps)
	return ps.Models, err
}

// OllamaTags returns the downloaded models.
func (c *Client) OllamaTags(ctx context.Context) ([]Model, error) {
	var tags struct {
		Models []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"models"`
	}
	if err := c.Get(ctx, "/api/tags", &tags); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(tags.Models))
	for _, m := range tags.Models {
		out = append(out, Model{ID: m.Name, Name: m.Name, Bytes: m.Size})
	}
	return out, nil
}
