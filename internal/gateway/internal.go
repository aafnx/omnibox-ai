package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
)

// Chat sends the panel chat's request through /v1, with the queue and the generation binding of
// every client, but without an API key: it comes from inside the service. session groups the
// chat's requests in the activity list. The response streams as the engine answers; the caller
// closes its body, which also ends the request.
func (g *Gateway) Chat(ctx context.Context, session string, body []byte) *http.Response {
	ctx, cancel := context.WithCancel(ctx)
	req, _ := http.NewRequestWithContext(context.WithValue(ctx, internalRequest{}, "chat"), http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Omnibox-Session", session)
	req.ContentLength = int64(len(body))
	pr, pw := io.Pipe()
	w := &pipeWriter{header: http.Header{}, pipe: pw, ready: make(chan struct{})}
	go func() {
		g.ServeHTTP(w, req)
		w.WriteHeader(http.StatusOK)
		pw.Close()
	}()
	<-w.ready
	return &http.Response{StatusCode: w.status, Header: w.header, Body: &cancelBody{ReadCloser: pr, cancel: cancel}}
}

// pipeWriter hands a handler's response to a reader as it is written.
type pipeWriter struct {
	header http.Header
	pipe   *io.PipeWriter
	status int
	once   sync.Once
	ready  chan struct{}
}

func (w *pipeWriter) Header() http.Header { return w.header }

func (w *pipeWriter) WriteHeader(status int) {
	w.once.Do(func() {
		w.status = status
		close(w.ready)
	})
}

func (w *pipeWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.pipe.Write(b)
}

func (w *pipeWriter) Flush() {}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelBody) Close() error {
	b.cancel()
	return b.ReadCloser.Close()
}
