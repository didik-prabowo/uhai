package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
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
