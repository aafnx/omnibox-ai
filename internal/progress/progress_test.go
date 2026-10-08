package progress

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestReaderReportsBytes(t *testing.T) {
	var last [2]int64
	ctx := With(context.Background(), func(done, total int64) { last = [2]int64{done, total} })
	if _, err := io.Copy(io.Discard, Reader(ctx, strings.NewReader(strings.Repeat("x", 70000)), 70000)); err != nil {
		t.Fatal(err)
	}
	if last != [2]int64{70000, 70000} {
		t.Fatalf("%v", last)
	}
	// Without a listener the reader is the original one.
	r := strings.NewReader("y")
	if Reader(context.Background(), r, 1) != io.Reader(r) {
		t.Fatal("wrapped without a listener")
	}
	Report(context.Background(), 1, 2)
}
