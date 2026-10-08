package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/aafnx/omnibox-ai/internal/catalog"
	"github.com/aafnx/omnibox-ai/internal/config"
	"github.com/aafnx/omnibox-ai/internal/engine"
	"github.com/aafnx/omnibox-ai/internal/storage"
)

// DownloadIdle aborts a download that receives nothing for this long.
var DownloadIdle = 2 * time.Minute

// downloadGGUF fetches a model file for PrismML from a Hugging Face link, with resume.
func (c *Controller) downloadGGUF(ctx context.Context, name string, r Request) (string, error) {
	if name != config.PrismML {
		return "", errors.New("GGUF скачивается для PrismML; для Ollama используйте скачивание по имени.")
	}
	u, err := catalog.ModelURL(r.URL)
	if err != nil {
		return "", err
	}
	e, err := c.engine(name)
	if err != nil {
		return "", err
	}
	dir := e.ModelsDirectory
	if dir == "" {
		dir = engine.ModelDirectory(e)
	}
	if dir == "" {
		return "", errors.New("Не задан каталог моделей PrismML.")
	}
	var size int64
	if r.Size != "" {
		if size, err = strconv.ParseInt(r.Size, 10, 64); err != nil || size < 0 {
			return "", errors.New("Размер файла — целое число байт.")
		}
	}
	if r.SHA256 != "" && !sha256Hex(r.SHA256) {
		return "", errors.New("SHA256 должен содержать 64 hex-символа.")
	}
	if free, ok := storage.FreeSpace(dir); ok && size > 0 && free < size+(1<<30) {
		return "", fmt.Errorf("Недостаточно места на диске моделей: свободно %.1f ГБ, нужно %.1f ГБ.", float64(free)/(1<<30), float64(size)/(1<<30))
	}
	d := &catalog.Download{URL: u, Dir: dir, Filename: r.Filename, SHA256: r.SHA256, Size: size, HTTP: c.HTTP, Idle: DownloadIdle,
		Progress: func(done, total int64) { Progress(ctx, done, total) }}
	if err := d.Run(ctx); err != nil {
		return "", err
	}
	c.recordSource(filepath.Join(dir, r.Filename), u)
	return "Скачано: " + r.Filename + ". Выберите модель в настройках; сама она не загружалась.", nil
}

func sha256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// pull downloads an Ollama model by name, streaming its progress. It never starts the server.
func (c *Controller) pull(ctx context.Context, name, model string) (string, error) {
	if name != config.Ollama {
		return "", errors.New("Скачивание по имени доступно для Ollama.")
	}
	if !config.ValidOllamaModel(model) {
		return "", errors.New("Некорректное имя модели Ollama.")
	}
	if !c.running(name) {
		return "", errors.New("Сначала запустите Ollama. Скачивание само сервер не запускает.")
	}
	e, err := c.engine(name)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var stalled atomic.Bool
	timer := time.AfterFunc(DownloadIdle, func() { stalled.Store(true); cancel() })
	defer timer.Stop()
	body, _ := json.Marshal(map[string]any{"model": model, "stream": true})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.client(name, e).BaseURL+"/api/pull", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Loopback only, no redirects; the stream has no overall timeout, only the idle one.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", c.pullFailed(ctx, &stalled, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Ollama ответила %d.", resp.StatusCode)
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	success := false
	for scanner.Scan() {
		timer.Reset(DownloadIdle)
		var line struct {
			Status    string `json:"status"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil {
			continue
		}
		if line.Error != "" {
			return "", errors.New("Ollama: " + line.Error)
		}
		if line.Total > 0 {
			Progress(ctx, line.Completed, line.Total)
		}
		success = success || line.Status == "success"
	}
	if err := scanner.Err(); err != nil {
		return "", c.pullFailed(ctx, &stalled, err)
	}
	if !success {
		return "", c.pullFailed(ctx, &stalled, errors.New("поток завершился без подтверждения"))
	}
	return "Модель " + model + " скачана.", nil
}

func (c *Controller) pullFailed(ctx context.Context, stalled *atomic.Bool, err error) error {
	switch {
	case stalled.Load():
		return errors.New("Скачивание прервано: нет данных дольше допустимого. Повторите — Ollama продолжит с уже скачанных частей.")
	case ctx.Err() != nil:
		return catalog.ErrCanceled
	}
	return fmt.Errorf("Скачивание прервано: %v. Повторите — Ollama продолжит с уже скачанных частей.", err)
}
