// Package progress carries the progress of a long operation (a download, an install) from the
// code doing it to the panel's operation list, through the context.
package progress

import (
	"context"
	"io"
)

type key struct{}

// With returns a context whose operations report bytes done and total (0 if unknown) to f.
func With(ctx context.Context, f func(done, total int64)) context.Context {
	return context.WithValue(ctx, key{}, f)
}

// Report tells the operation running in ctx how far it is.
func Report(ctx context.Context, done, total int64) {
	if f, ok := ctx.Value(key{}).(func(int64, int64)); ok {
		f(done, total)
	}
}

// Reader reports the bytes read through it.
func Reader(ctx context.Context, r io.Reader, total int64) io.Reader {
	if _, ok := ctx.Value(key{}).(func(int64, int64)); !ok {
		return r
	}
	return &reader{ctx: ctx, r: r, total: total}
}

type reader struct {
	ctx   context.Context
	r     io.Reader
	done  int64
	total int64
}

func (p *reader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.done += int64(n)
		Report(p.ctx, p.done, p.total)
	}
	return n, err
}
