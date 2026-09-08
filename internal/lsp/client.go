// Package lsp talks to language servers, so the model can ask where a symbol
// is defined and who uses it and get the compiler's answer rather than grep's.
//
// It is deliberately a fraction of what a language server can do. An editor
// client syncs every keystroke, tracks document versions and collects
// diagnostics; none of that is here, because none of it is needed for the one
// question being asked. uhai's edits land on disk before the model can ask
// anything, and a server reads a file it was never told about — verified
// against gopls: initialize, then textDocument/definition with no didOpen at
// all, and it answers. The 300 lines of document bookkeeping the references
// carry are the price of diagnostics, and diagnostics are what `go build`
// already answers here.
//
// This file is the transport: JSON-RPC 2.0 over the server's stdin and stdout,
// framed with Content-Length headers.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// startTimeout bounds the handshake. It is generous because it is not idle
// waiting: gopls indexes the module before it answers, and on a large tree
// that is tens of seconds once, not per call.
//
// callTimeout bounds everything after. Vars so a test can shrink them.
var (
	startTimeout = 90 * time.Second
	callTimeout  = 20 * time.Second
)

// client is one running language server and the pipe to it.
type client struct {
	cmd *exec.Cmd
	in  io.WriteCloser

	mu      sync.Mutex
	nextID  int
	pending map[int]chan reply
	closed  bool
}

// reply is one answer, kept as raw JSON because the caller knows the shape it
// asked for and this file does not.
type reply struct {
	result json.RawMessage
	err    error
}

// start spawns a server for one project and completes the handshake.
//
// rootUri is the project, which is what makes a workspace-wide question
// possible at all: without it the server indexes nothing and answers nothing.
// It comes from the root the tool call carried, so two projects served by one
// uhai get two servers rather than one with the wrong tree.
func start(ctx context.Context, root string, argv []string) (*client, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// Its own errors are not ours to interpret and not the model's to read.
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("could not start %s: %w", argv[0], err)
	}

	c := &client{cmd: cmd, in: stdin, pending: map[int]chan reply{}}
	go c.read(bufio.NewReader(stdout))

	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	if err := c.call(ctx, "initialize", map[string]any{
		"processId": nil,
		"rootUri":   fileURI(root),
		// Asked for by name so a server that offers neither says so at the
		// door rather than by answering nothing to every question.
		"capabilities": map[string]any{
			"textDocument": map[string]any{"definition": map[string]any{}, "references": map[string]any{}, "implementation": map[string]any{}},
			"workspace":    map[string]any{"symbol": map[string]any{}},
		},
	}, nil); err != nil {
		c.close()
		return nil, err
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

// call sends a request and waits for its answer.
func (c *client) call(ctx context.Context, method string, params, into any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("the language server has stopped")
	}
	c.nextID++
	id := c.nextID
	ch := make(chan reply, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if into == nil || len(r.result) == 0 || string(r.result) == "null" {
			return nil
		}
		return json.Unmarshal(r.result, into)
	}
}

// notify sends a message with no id, which is a message with no answer.
func (c *client) notify(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *client) write(msg any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	// One write, so two goroutines cannot interleave a header with another's
	// body — which would desynchronise the stream permanently rather than
	// losing one message.
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.in.Write(append([]byte("Content-Length: "+strconv.Itoa(len(body))+"\r\n\r\n"), body...))
	return err
}

// read is the one reader. Answers are matched to their request by id;
// everything else the server says — progress, logs, diagnostics we never
// asked for — is dropped, which is what makes this a client for one question
// rather than an editor.
func (c *client) read(r *bufio.Reader) {
	for {
		size, err := frameSize(r)
		if err != nil {
			c.fail(err)
			return
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(r, body); err != nil {
			c.fail(err)
			return
		}

		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &msg); err != nil || msg.ID == nil {
			continue // a notification, or a frame we cannot read
		}

		c.mu.Lock()
		ch := c.pending[*msg.ID]
		c.mu.Unlock()
		if ch == nil {
			continue // ours to ignore: the caller gave up, or it is a server request
		}
		if msg.Error != nil {
			ch <- reply{err: fmt.Errorf("language server: %s", msg.Error.Message)}
			continue
		}
		ch <- reply{result: msg.Result}
	}
}

// frameSize reads the headers and returns the body length.
func frameSize(r *bufio.Reader) (int, error) {
	size := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if size < 0 {
				return 0, fmt.Errorf("a message arrived with no Content-Length")
			}
			return size, nil
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue
		}
		if size, err = strconv.Atoi(strings.TrimSpace(value)); err != nil {
			return 0, fmt.Errorf("unreadable Content-Length: %w", err)
		}
	}
}

// fail wakes everyone waiting when the pipe dies, rather than leaving them on
// their timeouts: a server that has exited will not answer, and twenty seconds
// of silence per call is a worse way to learn that.
func (c *client) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for id, ch := range c.pending {
		ch <- reply{err: fmt.Errorf("the language server stopped: %w", err)}
		delete(c.pending, id)
	}
}

// close ends the server. Closing its stdin is what a language server watches
// for, and killing it is the answer for one that does not take the hint.
func (c *client) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()

	c.in.Close()
	done := make(chan struct{})
	go func() { c.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		c.cmd.Process.Kill()
	}
}

// fileURI is the path as the protocol wants it. Every path crossing this
// boundary is absolute already — the tool resolved it against the project
// before we were called — so there is nothing here to guess about.
func fileURI(path string) string {
	return "file://" + (&url.URL{Path: filepath.ToSlash(path)}).EscapedPath()
}

// fromURI is the other direction, and tolerates a server that did not escape
// what it sent.
func fromURI(uri string) string {
	rest, ok := strings.CutPrefix(uri, "file://")
	if !ok {
		return uri
	}
	if unescaped, err := url.PathUnescape(rest); err == nil {
		return filepath.FromSlash(unescaped)
	}
	return filepath.FromSlash(rest)
}
