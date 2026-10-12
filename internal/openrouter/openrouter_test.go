package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aafnx/omnibox-ai/internal/reasoning"
)

const testKey = "sk-or-v1-0123456789abcdef7f3a"

const modelsJSON = `{"data":[
 {"id":"anthropic/claude-fable-5","name":"Anthropic: Claude Fable 5","context_length":1000000,
  "architecture":{"input_modalities":["text","image","file"],"output_modalities":["text"]},
  "pricing":{"prompt":"0.000003","completion":"0.000015"},
  "supported_parameters":["max_tokens","tools","tool_choice","reasoning","include_reasoning"]},
 {"id":"google/gemini-2.5-flash-image","name":"Google: Gemini 2.5 Flash Image","context_length":32768,
  "architecture":{"input_modalities":["image","text"],"output_modalities":["image","text"]},
  "pricing":{"prompt":"0.0000003","completion":"0.0000025"},
  "supported_parameters":["max_tokens","temperature","top_p","seed"]},
 {"id":"openai/gpt-4o-audio-preview","name":"OpenAI: GPT-4o Audio","context_length":128000,
  "architecture":{"input_modalities":["audio","text","video"],"output_modalities":["text"]},
  "pricing":{"prompt":"0.0000025","completion":"0.00001"},
  "supported_parameters":["temperature","tools","tool_choice"]},
 {"id":"openrouter/auto","name":"Auto Router","context_length":2000000,
  "architecture":{},"pricing":{"prompt":"-1","completion":"-1"}},
 {"id":"","name":"broken"}
]}`

// fakeRouter is an OpenRouter stand-in that requires the key and the attribution headers.
type fakeRouter struct {
	*httptest.Server
	mu       sync.Mutex
	requests map[string]int
	status   int    // when set, every request fails with it
	errBody  string // the body of the failure
	lastBody []byte
}

func newRouter(t *testing.T) *fakeRouter {
	t.Helper()
	f := &fakeRouter{requests: map[string]int{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests[r.Method+" "+r.URL.Path]++
		status, errBody := f.status, f.errBody
		f.mu.Unlock()
		if r.Header.Get("HTTP-Referer") != "https://github.com/aafnx/omnibox-ai" || r.Header.Get("X-Title") != "OmniBox AI" {
			http.Error(w, `{"error":{"message":"no attribution"}}`, http.StatusBadRequest)
			return
		}
		if status != 0 {
			w.WriteHeader(status)
			io.WriteString(w, errBody)
			return
		}
		if r.URL.Path != "/models" && r.Header.Get("Authorization") != "Bearer "+testKey {
			http.Error(w, `{"error":{"message":"No auth credentials found"}}`, http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/models":
			io.WriteString(w, modelsJSON)
		case "/key":
			io.WriteString(w, `{"data":{"label":"sk-or-v1-012...7f3a","usage":12.5,"limit":null,"usage_daily":0.25,"usage_weekly":1.5,"usage_monthly":6,"is_free_tier":false}}`)
		case "/credits":
			io.WriteString(w, `{"data":{"total_credits":20,"total_usage":12.75}}`)
		case "/chat/completions":
			body, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.lastBody = body
			f.mu.Unlock()
			if r.Header.Get("Content-Type") != "application/json" {
				http.Error(w, "content type", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeRouter) fail(status int, body string) {
	f.mu.Lock()
	f.status, f.errBody = status, body
	f.mu.Unlock()
}

func (f *fakeRouter) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[key]
}

func (f *fakeRouter) client() *Client { return &Client{Key: testKey, BaseURL: f.URL + "/"} }

func TestModels(t *testing.T) {
	f := newRouter(t)
	models, err := f.client().Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 4 {
		t.Fatalf("got %d models, want 4 (the one without id is skipped)", len(models))
	}
	fable := models[0]
	if fable.ID != "anthropic/claude-fable-5" || fable.Name != "Anthropic: Claude Fable 5" || fable.Provider != "Anthropic" || fable.Context != 1000000 {
		t.Errorf("fable: %+v", fable)
	}
	if fable.PromptUSD != 3 || fable.CompletionUSD != 15 {
		t.Errorf("fable prices: %v / %v per 1M", fable.PromptUSD, fable.CompletionUSD)
	}
	if want := (Caps{Vision: true, Files: true, Tools: true, Reasoning: true}); fable.Caps != want {
		t.Errorf("fable caps: %+v", fable.Caps)
	}
	if fable.Reasoning.Mode != reasoning.ModeAlways || !fable.Reasoning.NoSampling || !fable.Reasoning.NoForcedTools {
		t.Errorf("fable reasoning: %+v", fable.Reasoning)
	}
	image := models[1]
	if want := (Caps{Vision: true, Draws: true}); image.Caps != want || image.Reasoning.Mode != reasoning.ModeNone {
		t.Errorf("image model: %+v %+v", image.Caps, image.Reasoning)
	}
	if !image.Reasoning.NoForcedTools || image.Reasoning.NoSampling {
		t.Errorf("image model restrictions from supported parameters: %+v", image.Reasoning)
	}
	if image.PromptUSD != 0.3 || image.CompletionUSD != 2.5 {
		t.Errorf("image prices: %v / %v", image.PromptUSD, image.CompletionUSD)
	}
	audio := models[2]
	if want := (Caps{Audio: true, Video: true, Tools: true}); audio.Caps != want || audio.Provider != "OpenAI" {
		t.Errorf("audio model: %+v", audio)
	}
	auto := models[3]
	if auto.Provider != "openrouter" || auto.PromptUSD != -1 || auto.Caps != (Caps{}) {
		t.Errorf("router: %+v", auto)
	}
}

func TestCatalog(t *testing.T) {
	f := newRouter(t)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	cat := &Catalog{Path: filepath.Join(t.TempDir(), "run", "openrouter-models.json"), Client: f.client, Now: func() time.Time { return now }}
	ctx := context.Background()

	models, fetched, err := cat.Get(ctx, false)
	if err != nil || len(models) != 4 || !fetched.Equal(now) || f.count("GET /models") != 1 {
		t.Fatalf("first fetch: %d models, %v, %v, %d requests", len(models), fetched, err, f.count("GET /models"))
	}
	// Fresh: from the file, with derived fields restored.
	now = now.Add(59 * time.Minute)
	models, fetched, err = cat.Get(ctx, false)
	if err != nil || f.count("GET /models") != 1 || !fetched.Equal(now.Add(-59*time.Minute)) {
		t.Fatalf("fresh cache refetched: %v, %d requests", err, f.count("GET /models"))
	}
	if models[0].Reasoning.Mode != reasoning.ModeAlways || !models[0].Caps.Vision {
		t.Errorf("cached model lost derived fields: %+v", models[0])
	}
	// Forced.
	if _, _, err := cat.Get(ctx, true); err != nil || f.count("GET /models") != 2 {
		t.Fatalf("force: %v, %d requests", err, f.count("GET /models"))
	}
	// Stale and OpenRouter fails: the old list comes back with the error.
	now = now.Add(2 * time.Hour)
	f.fail(http.StatusServiceUnavailable, `{"error":{"message":"down"}}`)
	models, fetched, err = cat.Get(ctx, false)
	if err == nil || len(models) != 4 || fetched.IsZero() || f.count("GET /models") != 3 {
		t.Fatalf("stale: %d models, %v, %v", len(models), fetched, err)
	}
	// Stale and OpenRouter answers: refreshed.
	f.fail(0, "")
	if _, fetched, err = cat.Get(ctx, false); err != nil || !fetched.Equal(now) {
		t.Fatalf("refresh: %v %v", fetched, err)
	}
	// No cache and no answer: an error only.
	f.fail(http.StatusBadGateway, "")
	empty := &Catalog{Path: filepath.Join(t.TempDir(), "models.json"), Client: f.client}
	if models, _, err := empty.Get(ctx, false); err == nil || models != nil {
		t.Fatalf("no cache: %v %v", models, err)
	}
}

func TestAccount(t *testing.T) {
	f := newRouter(t)
	acc, err := f.client().Account(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Account{Label: "sk-or-v1-012...7f3a", TotalCredits: 20, TotalUsage: 12.75, Balance: 7.25, Usage: 12.5, Daily: 0.25, Weekly: 1.5, Monthly: 6}
	if !reflect.DeepEqual(acc, want) {
		t.Errorf("account %+v, want %+v", acc, want)
	}
	// Missing fields are zero; a limit is a pointer.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/key" {
			io.WriteString(w, `{"data":{"limit":5,"limit_remaining":1.5,"limit_reset":"monthly","expires_at":"2027-01-12T09:30:00Z"}}`)
		} else {
			io.WriteString(w, `{"data":{}}`)
		}
	}))
	defer srv.Close()
	acc, err = (&Client{Key: testKey, BaseURL: srv.URL}).Account(context.Background())
	if err != nil || acc.Limit == nil || *acc.Limit != 5 || *acc.LimitRemaining != 1.5 || acc.Balance != 0 {
		t.Errorf("partial account: %+v %v", acc, err)
	}
	// The key's limit period and expiry, for the panel to show what is left of the key.
	if acc.LimitReset != "monthly" || acc.ExpiresAt == nil || !acc.ExpiresAt.Equal(time.Date(2027, 1, 12, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("limit reset and expiry: %+v", acc)
	}
}

func TestChat(t *testing.T) {
	f := newRouter(t)
	body := []byte(`{"model":"anthropic/claude-fable-5","stream":true,"messages":[]}`)
	resp, err := f.client().Chat(context.Background(), body)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(data), "[DONE]") || string(f.lastBody) != string(body) {
		t.Errorf("stream %q, sent %q", data, f.lastBody)
	}
}

func TestErrors(t *testing.T) {
	f := newRouter(t)
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{401, `{"error":{"message":"User not found."}}`, "Ключ OpenRouter не принят. Проверьте его в настройках."},
		{402, `{"error":{"message":"Insufficient credits"}}`, "На счёте OpenRouter закончились деньги. Пополните баланс."},
		{429, `{"error":{"message":"Rate limit exceeded"}}`, "OpenRouter ограничил частоту запросов. Подождите немного и повторите. Ответ OpenRouter: Rate limit exceeded"},
		{400, `{"error":{"message":"temperature is not supported"}}`, "OpenRouter ответил 400. Ответ OpenRouter: temperature is not supported"},
		{500, "<html>oops</html>", "OpenRouter ответил 500."},
		// OpenRouter's message is dropped when it quotes the key.
		{400, `{"error":{"message":"bad key ` + testKey + `"}}`, "OpenRouter ответил 400."},
	}
	for _, c := range cases {
		f.fail(c.status, c.body)
		_, err := f.client().Chat(context.Background(), []byte(`{}`))
		var e *Error
		if !errors.As(err, &e) || e.Status != c.status || err.Error() != c.want {
			t.Errorf("%d: %v, want %q", c.status, err, c.want)
		}
		if strings.Contains(err.Error(), testKey) || strings.Contains(e.Detail, testKey) {
			t.Errorf("%d: the key leaked into the error", c.status)
		}
		if _, err := f.client().Models(context.Background()); err == nil || err.Error() != c.want {
			t.Errorf("models %d: %v", c.status, err)
		}
		if _, err := f.client().Account(context.Background()); err == nil || err.Error() != c.want {
			t.Errorf("account %d: %v", c.status, err)
		}
	}
	// A long message is cut.
	f.fail(400, `{"error":{"message":"`+strings.Repeat("я", 400)+`"}}`)
	_, err := f.client().Chat(context.Background(), []byte(`{}`))
	var e *Error
	if !errors.As(err, &e) || len(e.Detail) > detailLimit+len("…") || !strings.HasSuffix(e.Detail, "…") {
		t.Errorf("long detail: %q", e.Detail)
	}
	// Unreachable server.
	f.Close()
	_, err = f.client().Chat(context.Background(), []byte(`{}`))
	if !errors.As(err, &e) || e.Status != 0 || !strings.HasPrefix(err.Error(), "OpenRouter недоступен") || strings.Contains(err.Error(), testKey) {
		t.Errorf("unreachable: %v", err)
	}
}

func TestNoRedirects(t *testing.T) {
	var leaked sync.Map
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store("hit", r.Header.Get("Authorization"))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	// Both the default client and a given one must stop at the redirect.
	for _, h := range []*http.Client{nil, {}} {
		c := &Client{Key: testKey, BaseURL: srv.URL, HTTP: h}
		_, err := c.Chat(context.Background(), []byte(`{}`))
		var e *Error
		if !errors.As(err, &e) || e.Status != http.StatusTemporaryRedirect {
			t.Errorf("redirect: %v", err)
		}
		if _, err := c.Models(context.Background()); err == nil {
			t.Error("a redirected model list must fail")
		}
	}
	if _, ok := leaked.Load("hit"); ok {
		t.Fatal("a redirect was followed")
	}
}

func TestKeyStore(t *testing.T) {
	dir := t.TempDir()
	ks := KeyStore{Path: filepath.Join(dir, "run", "openrouter-key.bin")}
	if _, ok := ks.Load(); ok {
		t.Fatal("no key yet")
	}
	if err := ks.Forget(); err != nil {
		t.Fatalf("forget without a key: %v", err)
	}
	bad := map[string]string{
		"sk-proj-0123456789abcdef":          "начинаться с sk-or-",
		"sk-or-short":                       "от 20 до 200",
		"sk-or-" + strings.Repeat("a", 195): "от 20 до 200",
		"sk-or-v1-0123 456789abcdef":        "пробелов",
		"sk-or-v1-0123\t456789abcdef":       "пробелов",
		"sk-or-v1-0123456789abcdefключ":     "пробелов",
		"":                                  "начинаться с sk-or-",
	}
	for key, want := range bad {
		err := ks.Save(key, time.Now())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Save(%q) = %v, want %q", key, err, want)
			continue
		}
		if key != "" && strings.Contains(err.Error(), key) {
			t.Errorf("error quotes the key: %v", err)
		}
	}
	if _, err := os.Stat(ks.Path); !os.IsNotExist(err) {
		t.Fatal("an invalid key was written")
	}
	saved := time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)
	if err := ks.Save("  "+testKey+"\n", saved); err != nil {
		t.Fatal(err)
	}
	got, ok := ks.Load()
	if !ok || got.Key != testKey || !got.Saved.Equal(saved) || got.Hint != "…7f3a" {
		t.Fatalf("load: %+v %v", got, ok)
	}
	if info, err := os.Stat(ks.Path); err != nil || (isUnix() && info.Mode().Perm() != 0o600) {
		t.Errorf("key file mode: %v %v", info.Mode(), err)
	}
	if info, err := os.Stat(filepath.Dir(ks.Path)); err != nil || (isUnix() && info.Mode().Perm() != 0o700) {
		t.Errorf("key dir mode: %v %v", info.Mode(), err)
	}
	// A damaged file reads as no key.
	os.WriteFile(ks.Path, []byte("garbage"), 0o600)
	if _, ok := ks.Load(); ok {
		t.Error("garbage loaded as a key")
	}
	if err := ks.Save(testKey, saved); err != nil {
		t.Fatal(err)
	}
	if err := ks.Forget(); err != nil {
		t.Fatal(err)
	}
	if _, ok := ks.Load(); ok {
		t.Error("key still there after Forget")
	}
}

func isUnix() bool { return os.PathSeparator == '/' }

func TestSpend(t *testing.T) {
	now := time.Date(2026, 10, 8, 18, 0, 0, 0, time.Local)
	s := Spend{Path: filepath.Join(t.TempDir(), "run", "openrouter-spend.json"), Now: func() time.Time { return now }}
	if days := s.Days(3); !reflect.DeepEqual(days, []Day{{"2026-10-06", 0}, {"2026-10-07", 0}, {"2026-10-08", 0}}) {
		t.Fatalf("empty ledger: %v", days)
	}
	if err := s.Add("a", 0.5, "web"); err == nil {
		t.Error("unknown source accepted")
	}
	if err := s.Add("a", -1, SourceChat); err == nil {
		t.Error("negative cost accepted")
	}
	if err := s.Add("free", 0, SourceChat); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Error("a zero cost was written")
	}

	// 100 days ago, 89 days ago (the oldest kept), yesterday and today.
	add := func(daysAgo int, model string, usd float64, source string) {
		t.Helper()
		saved := now
		now = saved.AddDate(0, 0, -daysAgo)
		if err := s.Add(model, usd, source); err != nil {
			t.Fatal(err)
		}
		now = saved
	}
	add(100, "old", 9, SourceAPI)
	add(89, "anthropic/claude-fable-5", 1, SourceAPI)
	add(1, "openai/gpt-5", 0.25, SourceChat)
	add(0, "anthropic/claude-fable-5", 0.5, SourceChat)
	add(0, "anthropic/claude-fable-5", 0.25, SourceAPI)
	add(0, "openai/gpt-5", 0.125, SourceAPI)

	if days := s.Days(2); !reflect.DeepEqual(days, []Day{{"2026-10-07", 0.25}, {"2026-10-08", 0.875}}) {
		t.Errorf("Days(2) = %v", days)
	}
	if days := s.Days(1000); len(days) != 90 || days[0].Date != "2026-07-11" || days[0].USD != 1 {
		t.Errorf("Days(1000): %d days from %v", len(days), days[0])
	}
	if got := s.ByModel(1); !reflect.DeepEqual(got, []ModelSpend{{"anthropic/claude-fable-5", 0.75}, {"openai/gpt-5", 0.125}}) {
		t.Errorf("ByModel(1) = %v", got)
	}
	if got := s.ByModel(90); !reflect.DeepEqual(got, []ModelSpend{{"anthropic/claude-fable-5", 1.75}, {"openai/gpt-5", 0.375}}) {
		t.Errorf("ByModel(90) = %v", got)
	}
	if got := s.BySource(2); !reflect.DeepEqual(got, map[string]float64{SourceChat: 0.75, SourceAPI: 0.375}) {
		t.Errorf("BySource(2) = %v", got)
	}
	if got := s.ByModel(0); len(got) != 0 {
		t.Errorf("ByModel(0) = %v", got)
	}
	// The day 100 days ago was dropped from the file when a newer day was written.
	data, _ := os.ReadFile(s.Path)
	var f spendFile
	if err := json.Unmarshal(data, &f); err != nil || f.Days["2026-06-30"] != nil || len(f.Days) != 3 {
		t.Errorf("ledger days: %v %v", f.Days, err)
	}
	// A new day moves the window: the oldest day drops out.
	now = now.AddDate(0, 0, 1)
	if err := s.Add("openai/gpt-5", 0.5, SourceAPI); err != nil {
		t.Fatal(err)
	}
	if got := s.ByModel(90); !reflect.DeepEqual(got, []ModelSpend{{"openai/gpt-5", 0.875}, {"anthropic/claude-fable-5", 0.75}}) {
		t.Errorf("after rolling: %v", got)
	}
	data, _ = os.ReadFile(s.Path)
	f = spendFile{}
	if err := json.Unmarshal(data, &f); err != nil || f.Days["2026-07-11"] != nil || len(f.Days) != 3 {
		t.Errorf("ledger days after rolling: %v %v", f.Days, err)
	}
}

func TestSpendConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spend.json")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Separate values for the same file, as the chat and the gateway hold.
			if err := (Spend{Path: path}).Add("m", 0.25, SourceAPI); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := (Spend{Path: path}).ByModel(1); len(got) != 1 || got[0].USD != 5 {
		t.Errorf("concurrent adds: %v", got)
	}
}
