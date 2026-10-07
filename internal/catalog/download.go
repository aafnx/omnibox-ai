package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ErrCanceled means the owner canceled the download; its partial file is gone.
var ErrCanceled = errors.New("Скачивание отменено.")

// Download fetches one GGUF into a models directory. An interrupted download keeps its
// .part file and continues from the same byte next time, but only while the file on the server
// is provably the same: a link pinned to a commit, or the same ETag (If-Range).
type Download struct {
	URL      *url.URL
	Dir      string
	Filename string
	SHA256   string
	Size     int64 // expected size, 0 if unknown
	HTTP     *http.Client
	// Idle aborts a download that receives nothing for this long (no overall timeout).
	Idle time.Duration
	// Progress receives bytes done and total (0 if unknown).
	Progress func(done, total int64)
}

type partMeta struct {
	URL  string `json:"url"`
	ETag string `json:"etag,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// PartPattern matches the partial files and their metadata in a models directory.
var PartPattern = regexp.MustCompile(`^\.download-[0-9a-f]{32}\.(?:part|json)$`)

func (d *Download) key() string {
	sum := sha256.Sum256([]byte(d.URL.String()))
	return hex.EncodeToString(sum[:16])
}

func (d *Download) paths() (part, meta string) {
	base := filepath.Join(d.Dir, ".download-"+d.key())
	return base + ".part", base + ".json"
}

func (d *Download) reset() {
	part, meta := d.paths()
	_ = os.Remove(part)
	_ = os.Remove(meta)
}

func (d *Download) client() *http.Client {
	c := http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("слишком много перенаправлений")
		}
		if req.URL.Scheme != "https" && d.HTTP == nil {
			return errors.New("перенаправление не по HTTPS")
		}
		return nil
	}}
	if d.HTTP != nil {
		c.Transport = d.HTTP.Transport
	}
	return &c
}

var contentRange = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+|\*)$`)

// Run downloads the file and moves it into place after checking size, GGUF header and SHA256.
func (d *Download) Run(ctx context.Context) error {
	if !FileName.MatchString(d.Filename) || strings.Contains(d.Filename, "..") || projector.MatchString(d.Filename) {
		return errors.New("Имя GGUF: латиница, цифры, дефис, подчёркивание, без пути и projector.")
	}
	target := filepath.Join(d.Dir, d.Filename)
	if _, err := os.Stat(target); err == nil {
		return errors.New("Файл уже существует; перезапись моделей запрещена.")
	}
	if err := os.MkdirAll(d.Dir, 0o755); err != nil {
		return err
	}
	part, metaPath := d.paths()
	var meta partMeta
	if data, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(data, &meta)
	}
	var offset int64
	if info, err := os.Stat(part); err == nil && meta.URL == d.URL.String() && (Pinned(d.URL) || meta.ETag != "") {
		offset = info.Size()
	} else {
		d.reset()
		meta = partMeta{URL: d.URL.String()}
	}
	for attempt := 0; ; attempt++ {
		err := d.fetch(ctx, part, metaPath, &meta, offset)
		var restart errRestart
		if errors.As(err, &restart) && attempt == 0 {
			d.reset()
			meta, offset = partMeta{URL: d.URL.String()}, 0
			continue
		}
		if err != nil {
			if errors.Is(err, ErrCanceled) {
				d.reset()
			}
			return err
		}
		break
	}
	if err := d.verify(part); err != nil {
		d.reset()
		return err
	}
	if _, err := os.Stat(target); err == nil {
		d.reset()
		return errors.New("Файл уже существует; перезапись моделей запрещена.")
	}
	if err := os.Rename(part, target); err != nil {
		return err
	}
	_ = os.Remove(metaPath)
	return nil
}

// errRestart asks for one fresh download from the first byte (the range was refused).
type errRestart struct{}

func (errRestart) Error() string { return "restart" }

func (d *Download) fetch(parent context.Context, part, metaPath string, meta *partMeta, offset int64) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	idle := d.Idle
	if idle == 0 {
		idle = 2 * time.Minute
	}
	var stalled atomic.Bool
	timer := time.AfterFunc(idle, func() { stalled.Store(true); cancel() })
	defer timer.Stop()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.URL.String(), nil)
	req.Header.Set("User-Agent", "omnibox")
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
		if !Pinned(d.URL) {
			req.Header.Set("If-Range", meta.ETag)
		}
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return d.interrupted(parent, stalled.Load(), offset, err)
	}
	defer resp.Body.Close()
	total := d.Size
	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case resp.StatusCode == http.StatusPartialContent && offset > 0:
		m := contentRange.FindStringSubmatch(resp.Header.Get("Content-Range"))
		if m == nil || m[1] != strconv.FormatInt(offset, 10) {
			return errRestart{}
		}
		if m[3] != "*" {
			n, _ := strconv.ParseInt(m[3], 10, 64)
			if total > 0 && n != total {
				return errRestart{}
			}
			total = n
		}
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		// The server sent the whole file (changed file or no range support): start over.
		offset = 0
		flags |= os.O_TRUNC
		if resp.ContentLength > 0 {
			if total > 0 && resp.ContentLength != total {
				return errors.New("Размер файла на сервере не совпадает с каталогом.")
			}
			total = resp.ContentLength
		}
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset > 0:
		return errRestart{}
	default:
		return fmt.Errorf("Сервер ответил %d.", resp.StatusCode)
	}
	etag := resp.Header.Get("X-Linked-Etag")
	if etag == "" {
		etag = resp.Header.Get("ETag")
	}
	*meta = partMeta{URL: d.URL.String(), ETag: etag, Size: total}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(metaPath, data, 0o644); err != nil {
		return err
	}
	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	done, last := offset, time.Time{}
	buf := make([]byte, 1<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			timer.Reset(idle)
			if _, err := f.Write(buf[:n]); err != nil {
				return err
			}
			done += int64(n)
			if d.Progress != nil && time.Since(last) > 500*time.Millisecond {
				d.Progress(done, total)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return d.interrupted(parent, stalled.Load(), done, rerr)
		}
	}
	if d.Progress != nil {
		d.Progress(done, total)
	}
	if total > 0 && done != total {
		return d.interrupted(parent, stalled.Load(), done, fmt.Errorf("получено %d из %d байт", done, total))
	}
	return f.Close()
}

func (d *Download) interrupted(parent context.Context, stalled bool, done int64, err error) error {
	if parent.Err() != nil {
		return ErrCanceled
	}
	reason := err.Error()
	if stalled {
		reason = "нет данных дольше допустимого"
	}
	return fmt.Errorf("Скачивание прервано (%s). Скачано %.1f ГБ; повторите — загрузка продолжится с того же места.", reason, float64(done)/(1<<30))
}

func (d *Download) verify(part string) error {
	f, err := os.Open(part)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil || !bytes.Equal(head, []byte("GGUF")) {
		return errors.New("Скачанный файл не имеет заголовка GGUF.")
	}
	if d.SHA256 == "" {
		return nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), d.SHA256) {
		return errors.New("SHA256 скачанного GGUF не совпадает; файл удалён.")
	}
	return nil
}
