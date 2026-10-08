package openrouter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aafnx/omnibox-ai/internal/platform"
)

// CatalogFile is the cached model list under the data directory.
const CatalogFile = "run/openrouter-models.json"

// DefaultTTL is how long the cached model list is used without asking OpenRouter.
const DefaultTTL = time.Hour

// Catalog is the OpenRouter model list cached in one file. It is refreshed only when asked for
// and older than TTL, never on a timer.
type Catalog struct {
	// Path is <data>/run/openrouter-models.json.
	Path string
	// Client returns the client to fetch with; nil (or a nil result) fetches without a key,
	// which the model list allows.
	Client func() *Client
	// Now is time.Now when nil.
	Now func() time.Time
	// TTL is DefaultTTL when zero.
	TTL time.Duration

	mu sync.Mutex
}

type catalogFile struct {
	Fetched time.Time `json:"fetched"`
	Models  []Model   `json:"models"`
}

func (c *Catalog) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Get returns the models and when they were fetched. A cached list younger than TTL is returned
// as is unless force; otherwise the list is fetched and cached. When fetching fails, the cached
// list (if any) is returned together with the error.
func (c *Catalog) Get(ctx context.Context, force bool) ([]Model, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := c.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	cached, ok := c.load()
	now := c.now()
	if ok && !force && now.Sub(cached.Fetched) < ttl && !cached.Fetched.After(now) {
		return cached.Models, cached.Fetched, nil
	}
	var client *Client
	if c.Client != nil {
		client = c.Client()
	}
	if client == nil {
		client = &Client{}
	}
	models, err := client.Models(ctx)
	if err != nil {
		if ok {
			return cached.Models, cached.Fetched, err
		}
		return nil, time.Time{}, err
	}
	fresh := catalogFile{Fetched: now, Models: models}
	if data, err := json.Marshal(fresh); err == nil {
		// A cache that cannot be written only means fetching again next time.
		if os.MkdirAll(filepath.Dir(c.Path), 0o700) == nil {
			_ = platform.WriteAtomic(c.Path, data, 0o600)
		}
	}
	return models, now, nil
}

// load reads the cache and derives each model's capabilities and reasoning again, so the rules of
// the running version apply.
func (c *Catalog) load() (catalogFile, bool) {
	data, err := platform.ReadShared(c.Path)
	if err != nil {
		return catalogFile{}, false
	}
	var f catalogFile
	if json.Unmarshal(data, &f) != nil || f.Fetched.IsZero() {
		return catalogFile{}, false
	}
	for i := range f.Models {
		f.Models[i].finish()
	}
	return f, true
}
