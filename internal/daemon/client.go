package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
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

	// base is the URL requests are addressed to. Only a test sets it: over a
	// unix socket the host is a fiction the dialer ignores.
	base string

	// root is the project every request from this client is about. Held on
	// the client rather than passed to each call: a caller that had to
	// remember it on every call is a caller that will forget once, and
	// forgetting is how the wrong project gets answered.
	root string
}

// Dial returns a client for the daemon on the given socket. It does not
// connect: whether a daemon is there is answered by Health, which is a request
// the caller can act on, rather than by a constructor that can fail for two
// different reasons.
func Dial(socket string) *Client {
	root, _ := os.Getwd()
	return DialFor(socket, root)
}

// DialFor is Dial for a named project, which is what a test needs and what a
// front end working somewhere other than its own directory would need.
func DialFor(socket, root string) *Client {
	return &Client{root: root, http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
	}}
}

// Health reports the daemon's pid, and an error when there is no daemon.
func (c *Client) Health(ctx context.Context) (int, error) {
	h, err := c.status(ctx)
	return h.PID, err
}

// HoldsConversation reports whether this daemon can run a turn. One started
// without a provider cannot, and says so rather than failing at the prompt.
func (c *Client) HoldsConversation(ctx context.Context) bool {
	h, err := c.status(ctx)
	return err == nil && h.Conversation
}

type health struct {
	OK           bool `json:"ok"`
	PID          int  `json:"pid"`
	Conversation bool `json:"conversation"`
	Proto        int  `json:"proto"`
}

// ErrWrongVersion is a daemon this build cannot talk to. It is worth telling
// apart from "no daemon": one means start one, the other means the one that is
// there has to go first, and a front end that confused them would start a
// second daemon that could not bind the socket.
type ErrWrongVersion struct{ Daemon, Mine int }

func (e ErrWrongVersion) Error() string {
	return fmt.Sprintf("the running daemon speaks version %d and this build speaks %d — "+
		"stop it with `uhai -daemon-stop` and it will start again on the next use",
		e.Daemon, e.Mine)
}

func (c *Client) status(ctx context.Context) (health, error) {
	var out health
	if err := c.get(ctx, "/v1/health", &out); err != nil {
		return out, err
	}
	// Checked here rather than at each call site, so no route can be added
	// that forgets: health is the first thing every client asks.
	//
	// A daemon too old to report a version at all answers 0, which is a
	// mismatch and reads as one.
	if out.Proto != ProtoVersion {
		return out, ErrWrongVersion{Daemon: out.Proto, Mine: ProtoVersion}
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, path string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.host()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set(projectHeader, c.root)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The daemon's own words, the way post already did it. A status code
		// alone tells a user nothing they can act on, and this route's most
		// likely refusal — a request that did not name its project — is
		// exactly the kind that needs saying.
		said, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if trimmed := strings.TrimSpace(string(said)); trimmed != "" {
			return errors.New(trimmed)
		}
		return fmt.Errorf("daemon answered %s to %s", resp.Status, path)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// Events streams what the daemon is doing until ctx ends. The channel is
// closed on the way out, so a range over it terminates.
//
// It reconnects. A daemon that restarts — upgraded, crashed, stopped by hand —
// used to leave every attached terminal silent with nothing on screen to say
// so: the stream simply ended and the front end went on drawing a session that
// was no longer connected to anything.
//
// The first connection is not retried. A caller asking to watch a daemon that
// is not there should be told now, not left waiting on a channel that may
// never produce anything.
//
// Events published while it was away are lost. Buffering them would need the
// daemon to know who had been listening and how far behind they were, which is
// a sequence number and a per-client queue; a notice saying the gap happened is
// the honest smaller answer.
func (c *Client) Events(ctx context.Context) (<-chan Event, error) {
	body, err := c.openEvents(ctx)
	if err != nil {
		return nil, err
	}

	out := make(chan Event)
	go func() {
		defer close(out)
		wait := reconnectFirst
		for {
			c.pump(ctx, body, out)
			body.Close()
			if ctx.Err() != nil {
				return
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			if wait *= 2; wait > reconnectCap {
				wait = reconnectCap
			}

			next, err := c.openEvents(ctx)
			if err != nil {
				continue // still down; wait longer and try again
			}
			body, wait = next, reconnectFirst
			select {
			case out <- Event{Kind: EventNotice, Text: "reconnected to the daemon — anything it said while this terminal was away is lost"}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// reconnectFirst and reconnectCap bound the wait between attempts: quick
// enough that a daemon restarting under a front end is barely noticed, slow
// enough that one which is gone for good is not hammered.
const (
	reconnectFirst = 100 * time.Millisecond
	reconnectCap   = 5 * time.Second
)

func (c *Client) openEvents(ctx context.Context) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.host()+"/v1/events", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(projectHeader, c.root)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("daemon answered %s to /v1/events", resp.Status)
	}
	return resp.Body, nil
}

// pump reads one connection until it ends. Errors are not reported per event:
// a front end can do nothing useful about a malformed one, and stopping over
// it would lose the rest.
func (c *Client) pump(ctx context.Context, body io.Reader, out chan<- Event) {
	lines := bufio.NewScanner(body)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host()+path, payload)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(projectHeader, c.root)

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

// Prompt runs one turn of the daemon's conversation and returns when the turn
// is over. What it looked like on the way is on the event stream: subscribe
// first, prompt second.
func (c *Client) Prompt(ctx context.Context, text string) error {
	var out struct {
		Err string `json:"err"`
	}
	if err := c.post(ctx, "/v1/prompt", map[string]string{"text": text}, &out); err != nil {
		return err
	}
	if out.Err != "" {
		return errors.New(out.Err)
	}
	return nil
}

// StopTurn is Escape: it cancels the turn under way. It is not an error to
// press it when nothing is running.
func (c *Client) StopTurn(ctx context.Context) error {
	return c.post(ctx, "/v1/prompt/stop", nil, &struct{}{})
}

// Shutdown asks the daemon to stop, whatever it is doing. A person's command:
// it takes every project's running work down with it.
func (c *Client) Shutdown(ctx context.Context) error {
	return c.post(ctx, "/v1/shutdown", nil, &struct{}{})
}

// ErrBusy is a daemon that was asked to stand down and had work to do. Its own
// type because the caller has something to say about it — "it is still holding
// something, so this terminal keeps the old build" — which is different from
// the request having failed.
var ErrBusy = errors.New("the daemon has work in flight")

// ShutdownIfIdle asks the daemon to stop only if nothing would be lost. It is
// what a front end may do on its own behalf after an upgrade, where Shutdown
// is not: one daemon serves every project, and a build mismatch in this
// terminal is not a reason to end another project's task.
//
// A daemon too old to know the query answers by stopping anyway, which is the
// one case this cannot make safe — the question is newer than the daemons that
// most need to be asked it. The caller checks idleness itself first for that
// reason.
func (c *Client) ShutdownIfIdle(ctx context.Context) error {
	// Its own request rather than post's: post returns the daemon's words and
	// drops the status, and "busy" has to be told from "failed" by something
	// sturdier than matching a sentence.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host()+"/v1/shutdown?if_idle=1", nil)
	if err != nil {
		return err
	}
	req.Header.Set(projectHeader, c.root)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusConflict:
		return ErrBusy
	}
	said, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return fmt.Errorf("%s", strings.TrimSpace(string(said)))
}

func (c *Client) host() string {
	if c.base != "" {
		return c.base
	}
	return dialHost
}
