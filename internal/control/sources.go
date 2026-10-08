package control

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/platform"
)

// sourcesPath keeps where each downloaded model came from, in the data directory rather than
// next to the models: a shared models folder gets no files of OmniBox's own.
func (c *Controller) sourcesPath() string {
	return filepath.Join(c.DataDir, "run", "model-sources.json")
}

// sources maps model paths to their Hugging Face pages, for the files that still exist.
func (c *Controller) sources() map[string]string {
	out := map[string]string{}
	if c.DataDir == "" {
		return out
	}
	if b, err := platform.ReadShared(c.sourcesPath()); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	for path := range out {
		if _, err := os.Stat(path); err != nil {
			delete(out, path)
		}
	}
	return out
}

// recordSource remembers the repository page of a downloaded model; entries of removed models
// are dropped on the way, so the file holds no more than the models on disk.
func (c *Controller) recordSource(path string, u *url.URL) {
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 3)
	if c.DataDir == "" || len(parts) < 2 {
		return
	}
	list := c.sources()
	list[path] = "https://huggingface.co/" + parts[0] + "/" + parts[1]
	if b, err := json.Marshal(list); err == nil {
		_ = os.MkdirAll(filepath.Dir(c.sourcesPath()), 0o755)
		_ = platform.WriteAtomic(c.sourcesPath(), b, 0o644)
	}
}

// InstalledModels lists an engine's models for the catalog: size, quantization, capabilities,
// source, and whether the model is selected or needed for rollback. Ollama answers only while
// it runs.
func (c *Controller) InstalledModels(ctx context.Context, name string) ([]engine.Details, error) {
	e, err := c.engine(name)
	if err != nil {
		return nil, err
	}
	if name == config.Ollama {
		// A stopped Ollama is not started for a list: its models are read from disk.
		if !c.running(name) {
			out := []engine.Details{}
			if e.ModelsDirectory != "" {
				out = engine.OllamaDisk(e.ModelsDirectory)
			}
			for i := range out {
				out[i].Selected = out[i].ID == e.Model
				out[i].InUse = out[i].Selected
			}
			return out, nil
		}
		client := c.client(name, e)
		models, err := client.OllamaTags(ctx)
		if err != nil {
			return nil, err
		}
		return client.OllamaDetails(ctx, models, e.Model), nil
	}
	models, err := engine.PrismModels(engine.ModelDirectory(e))
	if err != nil {
		return nil, err
	}
	inUse := []string{e.Settings["model"]}
	if e.Previous != nil {
		inUse = append(inUse, e.Previous.Settings["model"])
	}
	return engine.PrismDetails(models, c.sources(), e.Settings["model"], inUse), nil
}
