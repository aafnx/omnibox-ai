package chatgpt

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// conn speaks the app-server protocol: one JSON message a line on stdin and stdout, requests
// with an id, notifications without one. The app-server's own requests — approvals, tool calls,
// questions to the user — are all refused: OmniBox gives Codex nothing to run.
type conn struct {
	w  io.WriteCloser
	mu sync.Mutex // one writer at a time

	pmu     sync.Mutex
	next    int64
	pending map[int64]chan reply
	subs    map[int]func(method string, params json.RawMessage)
	subID   int
	closed  bool
	err     error
	done    chan struct{}
	refused func(method string)
}

type reply struct {
	result json.RawMessage
	err    *rpcError
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return e.Message }

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// maxLine bounds one message; images come back in some notifications.
const maxLine = 64 << 20

func newConn(r io.Reader, w io.WriteCloser) *conn {
	c := &conn{w: w, pending: map[int64]chan reply{}, subs: map[int]func(string, json.RawMessage){}, done: make(chan struct{})}
	go c.read(r)
	return c
}

func (c *conn) read(r io.Reader) {
	br := bufio.NewReaderSize(r, 1<<20)
	var err error
	for {
		var line []byte
		line, err = readLine(br)
		if err != nil {
			break
		}
		var m message
		if json.Unmarshal(line, &m) != nil {
			continue // a stray line (a log) is not a message
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			// Written aside: the reader never waits on the app-server reading its input.
			go c.refuse(m)
		case m.Method != "":
			c.pmu.Lock()
			subs := make([]func(string, json.RawMessage), 0, len(c.subs))
			for _, f := range c.subs {
				subs = append(subs, f)
			}
			c.pmu.Unlock()
			for _, f := range subs {
				f(m.Method, m.Params)
			}
		case len(m.ID) > 0:
			var id int64
			if json.Unmarshal(m.ID, &id) != nil {
				continue
			}
			c.pmu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.pmu.Unlock()
			if ch != nil {
				ch <- reply{m.Result, m.Error}
			}
		}
	}
	c.shut(fmt.Errorf("ChatGPT (Codex) завершился: %v", err))
}

// readLine reads one line of any length up to maxLine.
func readLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, isPrefix, err := br.ReadLine()
		if err != nil {
			return nil, err
		}
		line = append(line, part...)
		if len(line) > maxLine {
			return nil, errors.New("слишком длинное сообщение")
		}
		if !isPrefix {
			return line, nil
		}
	}
}

// refuse answers a request of the app-server with an error: no command runs, no file changes,
// no tool is called and no question reaches the owner.
func (c *conn) refuse(m message) {
	if c.refused != nil {
		c.refused(m.Method)
	}
	_ = c.write(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "OmniBox does not allow tools, commands or approvals"}})
}

func (c *conn) shut(err error) {
	c.pmu.Lock()
	defer c.pmu.Unlock()
	if c.closed {
		return
	}
	c.closed, c.err = true, err
	for id, ch := range c.pending {
		ch <- reply{err: &rpcError{Code: -1, Message: err.Error()}}
		delete(c.pending, id)
	}
	close(c.done)
}

func (c *conn) write(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.w.Write(append(data, '\n'))
	return err
}

// call sends a request and waits for its answer.
func (c *conn) call(ctx context.Context, method string, params, result any) error {
	ch := make(chan reply, 1)
	c.pmu.Lock()
	if c.closed {
		err := c.err
		c.pmu.Unlock()
		return err
	}
	c.next++
	id := c.next
	c.pending[id] = ch
	c.pmu.Unlock()
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if err := c.write(msg); err != nil {
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
		return fmt.Errorf("ChatGPT (Codex) не отвечает: %v", err)
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if result != nil && len(r.result) > 0 {
			return json.Unmarshal(r.result, result)
		}
		return nil
	case <-ctx.Done():
		c.pmu.Lock()
		delete(c.pending, id)
		c.pmu.Unlock()
		return ctx.Err()
	}
}

// notify sends a notification.
func (c *conn) notify(method string, params any) error {
	msg := map[string]any{"method": method}
	if params != nil {
		msg["params"] = params
	}
	return c.write(msg)
}

// subscribe calls f for every notification until the returned function is called. f runs on
// the reading goroutine and must not block.
func (c *conn) subscribe(f func(method string, params json.RawMessage)) func() {
	c.pmu.Lock()
	c.subID++
	id := c.subID
	c.subs[id] = f
	c.pmu.Unlock()
	return func() {
		c.pmu.Lock()
		delete(c.subs, id)
		c.pmu.Unlock()
	}
}

func (c *conn) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}
