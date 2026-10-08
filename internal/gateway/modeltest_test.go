package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

func TestModelTestMeasuresTheRunningModel(t *testing.T) {
	f := newFixture(t, nil)
	r, err := f.g.Test(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Model != "ornith-27b" || r.PromptTokens != 7 || r.OutputTokens != 2 || r.FirstToken < 0 || r.Total < r.FirstToken {
		t.Fatalf("%+v", r)
	}
	f.mu.Lock()
	sent := f.received[len(f.received)-1]
	f.mu.Unlock()
	if sent["max_tokens"] != float64(160) || !strings.Contains(mustJSON(sent["messages"]), "тридцати") {
		t.Fatalf("%v", sent)
	}
	// It is recorded like any request, and needs no API key: it never comes from the network.
	if rec := f.g.Snapshot().Requests[0]; rec.State != "completed" {
		t.Fatalf("%+v", rec)
	}
	if !strings.Contains(r.String(), "первый токен") {
		t.Fatal(r.String())
	}
	// From the network the same request without a key is refused.
	resp, _ := f.request("/v1/chat/completions", chat("ornith-27b"), func(q *http.Request) { q.Header.Del("Authorization") })
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("%d", resp.StatusCode)
	}
}

func TestModelTestNeedsARunningModel(t *testing.T) {
	f := newFixture(t, nil)
	f.services.Set(config.ServiceName(config.PrismML), platform.Stopped)
	f.g.Invalidate()
	if _, err := f.g.Test(context.Background()); err == nil || !strings.Contains(err.Error(), "Нет запущенной модели") {
		t.Fatalf("%v", err)
	}
}
