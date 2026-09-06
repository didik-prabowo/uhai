package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// dialHost is a name the daemon never sees. net/http insists on a URL with a
// host, and a unix socket has none, so every request is addressed to this and
// the dialer ignores it.
const dialHost = "http://uhai.local"

// Client talks to a running daemon.
type Client struct {
	http *http.Client
}

// Dial returns a client for the daemon on the given socket. It does not
// connect: whether a daemon is there is answered by Health, which is a request
// the caller can act on, rather than by a constructor that can fail for two
// different reasons.
func Dial(socket string) *Client {
	return &Client{http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}}
}

// Health reports the daemon's pid, and an error when there is no daemon.
func (c *Client) Health(ctx context.Context) (int, error) {
	var out struct {
		OK  bool `json:"ok"`
		PID int  `json:"pid"`
	}
	if err := c.get(ctx, "/v1/health", &out); err != nil {
		return 0, err
	}
	return out.PID, nil
}

func (c *Client) get(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dialHost+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon answered %s to %s", resp.Status, path)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// Events streams what the daemon is doing until ctx ends or the daemon goes
// away. The channel is closed on the way out, so a range over it terminates.
//
// Errors are not returned per event: a front end can do nothing useful about a
// malformed one, and stopping the stream over it would lose the rest.
func (c *Client) Events(ctx context.Context) (<-chan Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dialHost+"/v1/events", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("daemon answered %s to /v1/events", resp.Status)
	}

	out := make(chan Event)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		lines := bufio.NewScanner(resp.Body)
		lines.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for lines.Scan() {
			line := lines.Text()
			// ": beat" is the heartbeat, and a blank line ends an event.
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var e Event
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e) != nil {
				continue
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// StartTask asks the daemon to run a prompt in the background. It returns as
// soon as the task exists, not when it finishes — the daemon is holding it, so
// the caller is free to close its terminal.
func (c *Client) StartTask(ctx context.Context, prompt string) (TaskView, error) {
	var out TaskView
	err := c.post(ctx, "/v1/tasks", map[string]string{"prompt": prompt}, &out)
	return out, err
}

// Tasks is everything the daemon is holding, finished or not.
func (c *Client) Tasks(ctx context.Context) ([]TaskView, error) {
	var out []TaskView
	return out, c.get(ctx, "/v1/tasks", &out)
}

// StopTask cancels one, and reports whether there was one to cancel.
func (c *Client) StopTask(ctx context.Context, id string) error {
	return c.post(ctx, "/v1/tasks/"+id+"/stop", nil, &struct{}{})
}

func (c *Client) post(ctx context.Context, path string, body, into any) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, dialHost+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The daemon's own words: it says why, and the front end shows it
		// rather than inventing a sentence about a status code.
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("%s", strings.TrimSpace(string(said)))
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// Running reports whether a daemon is there to talk to. Front ends ask before
// choosing between the daemon and their own process, and a failure here is an
// answer rather than an error: no daemon is the ordinary case.
func Running(socket string) (*Client, bool) {
	c := Dial(socket)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := c.Health(ctx); err != nil {
		return nil, false
	}
	return c, true
}

// Questions is what the daemon is waiting on. A front end that attaches
// mid-turn asks once, so a terminal reopened while a question is open can
// still answer it rather than leaving the turn to time out.
func (c *Client) Questions(ctx context.Context) ([]Question, error) {
	var out []Question
	return out, c.get(ctx, "/v1/questions", &out)
}

// Answer decides one. An error here means the question is no longer open —
// answered by another terminal, or timed out — which the front end should say
// rather than pretend it decided something.
func (c *Client) Answer(ctx context.Context, id string, allow bool) error {
	return c.post(ctx, "/v1/questions/"+id, map[string]bool{"allow": allow}, &struct{}{})
}
