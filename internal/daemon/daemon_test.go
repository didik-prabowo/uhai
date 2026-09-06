package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	s := NewServer(store, nil)
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
	s := NewServer(nil, nil)
	if err := s.Listen(socket); err != nil {
		t.Fatalf("a leftover socket must not stop the next daemon: %v", err)
	}
	s.Close()
}

// The whole reason the daemon exists: a task started through it keeps running
// after whoever asked for it has gone. As a goroutine in the front end's own
// process it died with the terminal and lost its report.
func TestATaskOutlivesTheClientThatStartedIt(t *testing.T) {
	running := make(chan struct{})
	finish := make(chan struct{})

	dir, err := os.MkdirTemp("", "u")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")

	s := NewServer(nil, func(ctx context.Context, prompt string) (string, int, error) {
		close(running)
		select {
		case <-finish:
			return "selesai: " + prompt, 12, nil
		case <-ctx.Done():
			// The task must not be holding the client's context. An HTTP
			// request's context is cancelled the moment its handler returns,
			// so a task that kept it would die the instant it was accepted —
			// long before the terminal even closed.
			return "", 0, fmt.Errorf("the task was cancelled with its client: %w", ctx.Err())
		}
	})
	if err := s.Listen(socket); err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })

	// A client that asks, and then is gone — its context cancelled, the way a
	// closed terminal takes its process with it.
	asking, hangUp := context.WithCancel(context.Background())
	started, err := Dial(socket).StartTask(asking, "hitung sesuatu")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if started.ID == "" {
		t.Fatal("a task has to come back with an id to be stopped or read later")
	}
	<-running
	hangUp()

	close(finish)

	// A different client entirely, the way a new terminal would be.
	deadline := time.Now().Add(5 * time.Second)
	for {
		list, err := Dial(socket).Tasks(context.Background())
		if err != nil {
			t.Fatalf("tasks: %v", err)
		}
		if len(list) == 1 && list[0].Err != "" {
			t.Fatalf("the task did not outlive its client: %s", list[0].Err)
		}
		if len(list) == 1 && list[0].Report != "" {
			if list[0].Report != "selesai: hitung sesuatu" {
				t.Errorf("the report did not survive: %q", list[0].Report)
			}
			if list[0].Tokens != 12 {
				t.Errorf("tokens = %d, want 12", list[0].Tokens)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the task did not finish after its client left: %+v", list)
		}
	}
}

// A daemon started without a provider still serves sessions and events, and
// says why it cannot run anything rather than failing obscurely.
func TestADaemonWithNoProviderSaysSo(t *testing.T) {
	_, c, _ := listen(t, nil)
	_, err := c.StartTask(context.Background(), "apa saja")
	if err == nil {
		t.Fatal("a daemon with no runner must refuse")
	}
	if !strings.Contains(err.Error(), "no provider") {
		t.Errorf("the refusal has to say why, got %q", err)
	}
}
