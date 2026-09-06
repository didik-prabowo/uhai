package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/didik-prabowo/uhai/internal/provider"
	"github.com/didik-prabowo/uhai/internal/session"
	"github.com/didik-prabowo/uhai/internal/session/filestore"
)

// listen starts a daemon on a socket of its own and hands back the client.
func listen(t *testing.T, store session.Store) (*Server, *Client, string) {
	t.Helper()
	// Not t.TempDir(): a macOS temp path is long enough to pass the 104-byte
	// limit a unix socket address has, and the failure is a bind error that
	// says nothing about length.
	dir, err := os.MkdirTemp("", "u")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	socket := filepath.Join(dir, "d.sock")
	s := NewServer(store)
	if err := s.Listen(socket); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })
	return s, Dial(socket), socket
}

func TestDaemonAnswersOnItsSocket(t *testing.T) {
	_, c, socket := listen(t, nil)

	pid, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("health reported pid %d, want %d", pid, os.Getpid())
	}

	// The socket can ask the agent to run commands, so it is a credential and
	// must not be readable by anyone else on the machine.
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != socketPerm {
		t.Errorf("socket is %v, want %v — anything that opens it gets a shell", perm, socketPerm)
	}
}

func TestEventsReachAClientAsTheyHappen(t *testing.T) {
	s, c, _ := listen(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := c.Events(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Published after the subscription, and read before the turn would have
	// ended: the point of a stream is that it arrives while it is written.
	go func() {
		s.Publish(Event{Kind: EventDelta, Text: "ha"})
		s.Publish(Event{Kind: EventDelta, Text: "lo"})
		s.Publish(Event{Kind: EventTaskDone, Task: "t1"})
	}()

	var got string
	for e := range events {
		if e.Kind == EventDelta {
			got += e.Text
		}
		if e.Kind == EventTaskDone {
			if e.Task != "t1" {
				t.Errorf("task id did not survive the wire: %q", e.Task)
			}
			break
		}
	}
	if got != "halo" {
		t.Errorf("the answer arrived as %q, want %q", got, "halo")
	}
}

// A front end that stops reading must not stop the agent: the daemon exists to
// keep working when a terminal goes away.
func TestASlowClientDoesNotHoldTheDaemonUp(t *testing.T) {
	s, c, _ := listen(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := c.Events(ctx); err != nil {
		t.Fatal(err)
	}
	cancel() // subscribed, then gone without draining

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			s.Publish(Event{Kind: EventDelta, Text: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing blocked on a client that went away")
	}
}

func TestSessionsComeBackOverTheSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "u")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	store := filestore.New(filepath.Join(dir, "sessions"))
	s := session.New()
	s.Model = "zai/glm-4.7"
	s.Messages = []provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "halo"}},
	}}
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}

	_, c, _ := listen(t, store)
	var out []session.Session
	if err := c.get(context.Background(), "/v1/sessions", &out); err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if len(out) != 1 || out[0].Model != "zai/glm-4.7" {
		t.Fatalf("want the one saved session, got %+v", out)
	}
}

// A socket left by a daemon that did not shut down is a file, and the next
// Listen fails on it even though nothing is listening.
func TestAStaleSocketIsCleared(t *testing.T) {
	dir, err := os.MkdirTemp("", "u")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	socket := filepath.Join(dir, "d.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewServer(nil)
	if err := s.Listen(socket); err != nil {
		t.Fatalf("a leftover socket must not stop the next daemon: %v", err)
	}
	s.Close()
}
