package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	s := NewServer(store, nil, nil)
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
	s := NewServer(nil, nil, nil)
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
	}, nil)
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

// Front ends start daemons on their own now, so two can reach Listen at once.
// A plain os.Remove would let the loser delete the winner's socket: the winner
// keeps running, reachable by nobody, while the loser listens on a new file —
// and both processes believe they succeeded.
func TestASecondDaemonRefusesInsteadOfClobbering(t *testing.T) {
	first, _, socket := listen(t, nil)

	second := NewServer(nil, nil, nil)
	err := second.Listen(socket)
	if err == nil {
		second.Close()
		t.Fatal("the second daemon took the socket from the first")
	}
	if !strings.Contains(err.Error(), "already listening") {
		t.Errorf("the refusal has to say why, got %q", err)
	}

	// And the first is still reachable, which is the thing that was at risk.
	if _, err := Dial(socket).Health(context.Background()); err != nil {
		t.Errorf("the running daemon was left unreachable: %v", err)
	}
	_ = first
}

// A leftover file from a daemon that did not shut down is still cleared: the
// check is for a live socket, not for any socket.
func TestALeftoverSocketIsStillCleared(t *testing.T) {
	dir, err := os.MkdirTemp("", "u")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	socket := filepath.Join(dir, "d.sock")
	// A real unix socket, then closed: the file survives, nothing accepts.
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	if _, err := os.Stat(socket); err == nil {
		s := NewServer(nil, nil, nil)
		if err := s.Listen(socket); err != nil {
			t.Fatalf("a dead socket must not stop the next daemon: %v", err)
		}
		s.Close()
	}
}

// A binary that does not understand -daemon runs its front end again, which
// calls Ensure, which starts another. Seen once for real, from a stand-in with
// no -daemon flag: the log filled with the same sentence from a new process
// each time. The child is marked so it stops the chain at one.
func TestASpawnedProcessDoesNotSpawnAgain(t *testing.T) {
	t.Setenv(envSpawned, "1")

	dir, err := os.MkdirTemp("", "u")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Nothing is listening here, so Ensure would otherwise reach for exec.
	_, err = Ensure(context.Background(), filepath.Join(dir, "d.sock"))
	if err == nil {
		t.Fatal("a spawned process must not spawn another")
	}
	if !strings.Contains(err.Error(), "refusing to start another") {
		t.Errorf("the refusal has to say what it is refusing, got %q", err)
	}
}

// And an unmarked process still finds a daemon that is already up, without
// starting anything.
func TestEnsureUsesTheDaemonThatIsAlreadyThere(t *testing.T) {
	_, _, socket := listen(t, nil)

	c, err := Ensure(context.Background(), socket)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	pid, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if pid != os.Getpid() {
		t.Errorf("Ensure started a second daemon instead of using the running one")
	}
}

// In one process, asking permission is a function call that returns a bool.
// Across two it is an event out and a POST back, and that round trip is the
// reason moving the conversation into the daemon is a piece of work rather
// than a move.
func TestAQuestionGoesOutAndTheAnswerComesBack(t *testing.T) {
	s, c, _ := listen(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}

	allowed := make(chan bool, 1)
	go func() { allowed <- s.Ask(context.Background(), "write_file", `{"path":"main.go"}`) }()

	// The question reaches the terminal as it is asked, not when the turn ends.
	var asked Question
	for e := range events {
		if e.Kind == EventQuestion && e.Question != nil {
			asked = *e.Question
			break
		}
	}
	if asked.Tool != "write_file" || asked.ID == "" {
		t.Fatalf("the question did not survive the wire: %+v", asked)
	}

	if err := c.Answer(context.Background(), asked.ID, true); err != nil {
		t.Fatalf("answer: %v", err)
	}
	select {
	case ok := <-allowed:
		if !ok {
			t.Error("the answer was yes and arrived as no")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the agent was never told what the human decided")
	}
}

// The agent's own default denies everything so a caller who forgets the hook
// cannot silently write files. A daemon nobody is watching is that situation
// from further away, and answers the same way.
func TestAQuestionNobodyAnswersIsDenied(t *testing.T) {
	s, _, _ := listen(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if s.Ask(ctx, "run_bash", `{"command":"rm -rf /"}`) {
		t.Error("a question with nobody to answer it must not be allowed")
	}
	// And it is not left open for the next terminal to trip over.
	if open := s.questions.list(); len(open) != 0 {
		t.Errorf("a question that was given up on is still open: %+v", open)
	}
}

// A terminal that attaches mid-turn can still find the question, rather than
// leaving the agent to wait out the timeout for nothing.
func TestAQuestionIsVisibleToATerminalThatArrivesLate(t *testing.T) {
	s, c, _ := listen(t, nil)

	go s.Ask(context.Background(), "edit_file", `{"path":"go.mod"}`)
	deadline := time.Now().Add(3 * time.Second)
	for {
		open, err := c.Questions(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(open) == 1 {
			if open[0].Tool != "edit_file" {
				t.Errorf("want the open question, got %+v", open[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a question in flight was invisible to a new terminal")
		}
	}
}

// Answering twice must not look like it worked twice: the second terminal has
// to be told the decision was already made.
func TestAnAlreadyAnsweredQuestionSaysSo(t *testing.T) {
	s, c, _ := listen(t, nil)

	go s.Ask(context.Background(), "write_file", "{}")
	var id string
	for id == "" {
		if open, _ := c.Questions(context.Background()); len(open) == 1 {
			id = open[0].ID
		}
	}
	if err := c.Answer(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}
	err := c.Answer(context.Background(), id, true)
	if err == nil {
		t.Fatal("the second answer must not be accepted")
	}
	if !strings.Contains(err.Error(), "no longer open") {
		t.Errorf("the refusal has to say why, got %q", err)
	}
}

// The other way nobody answers: a terminal is watching, sees the question, and
// says nothing. The context stays open, so only the deadline can end it — and
// it has to end it with a no.
func TestAQuestionNobodyAnswersInTimeIsDenied(t *testing.T) {
	s, c, _ := listen(t, nil)
	s.AnswerWait = 150 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := c.Events(ctx); err != nil { // watching, and silent
		t.Fatal(err)
	}

	if s.Ask(context.Background(), "run_bash", `{"command":"rm -rf /"}`) {
		t.Error("silence is not consent")
	}
	if open := s.questions.list(); len(open) != 0 {
		t.Errorf("a question that timed out is still open: %+v", open)
	}
}

// A conversation is a single history, so two turns must not write to it at
// once: interleaved, they produce something neither caller asked for.
func TestOneTurnAtATime(t *testing.T) {
	var mu sync.Mutex
	var overlapping, most int

	dir, _ := os.MkdirTemp("", "u")
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")

	s := NewServer(nil, nil, func(ctx context.Context, text string) (string, int, error) {
		mu.Lock()
		overlapping++
		if overlapping > most {
			most = overlapping
		}
		mu.Unlock()
		time.Sleep(80 * time.Millisecond)
		mu.Lock()
		overlapping--
		mu.Unlock()
		return "", 0, nil
	})
	if err := s.Listen(socket); err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			Dial(socket).Prompt(context.Background(), "halo")
		}()
	}
	wg.Wait()

	if most > 1 {
		t.Errorf("%d turns ran at once — the history is one thing", most)
	}
}

// A front end that hangs up mid-answer has left the room, not cancelled the
// work: the conversation belongs to the daemon, and half a turn written into
// its history is worse than a whole one nobody watched.
func TestATurnFinishesAfterItsFrontEndHangsUp(t *testing.T) {
	finished := make(chan error, 1)

	dir, _ := os.MkdirTemp("", "u")
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")

	gone := make(chan struct{})
	s := NewServer(nil, nil, func(ctx context.Context, text string) (string, int, error) {
		<-gone
		select {
		case <-ctx.Done():
			finished <- fmt.Errorf("the turn was cancelled with its front end: %w", ctx.Err())
		default:
			finished <- nil
		}
		return "", 0, nil
	})
	if err := s.Listen(socket); err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })

	asking, hangUp := context.WithCancel(context.Background())
	go Dial(socket).Prompt(asking, "halo")
	time.Sleep(100 * time.Millisecond) // the turn is under way
	hangUp()
	// The cancellation has to reach the server before the runner looks:
	// closing gone in the same breath let the runner check a context that had
	// not been cancelled yet, and the test passed while holding the front
	// end's context — the exact thing it exists to forbid.
	time.Sleep(200 * time.Millisecond)
	close(gone)

	select {
	case err := <-finished:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never finished")
	}
}

// A turn deliberately outlives the front end that asked for it, which left no
// way to stop one on purpose. Hanging up and pressing Escape are different
// decisions and need different routes.
func TestEscapeStopsTheTurnThatHangingUpDoesNot(t *testing.T) {
	reached := make(chan struct{})
	ended := make(chan error, 1)

	dir, _ := os.MkdirTemp("", "u")
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")

	s := NewServer(nil, nil, func(ctx context.Context, text string) (string, int, error) {
		close(reached)
		<-ctx.Done() // a turn that would otherwise run forever
		ended <- ctx.Err()
		return "", 0, ctx.Err()
	})
	if err := s.Listen(socket); err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })

	c := Dial(socket)
	go c.Prompt(context.Background(), "sesuatu yang panjang")
	<-reached

	if err := c.StopTurn(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case err := <-ended:
		if err == nil {
			t.Error("the turn ended without being cancelled")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Escape did not reach the turn")
	}
}

// Pressing Escape when nothing is running is not a mistake, and must not read
// as one.
func TestEscapeWithNothingRunningIsFine(t *testing.T) {
	_, c, _ := listen(t, nil)
	if err := c.StopTurn(context.Background()); err != nil {
		t.Errorf("Escape on an idle daemon must not be an error: %v", err)
	}
}

// A question whose turn was abandoned must not hold a terminal hostage until
// its deadline: cancelling the turn cancels what it was waiting for.
func TestEscapeReleasesAQuestionWaitingForAnAnswer(t *testing.T) {
	dir, _ := os.MkdirTemp("", "u")
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")

	answered := make(chan bool, 1)
	asking := make(chan struct{})

	var s *Server
	s = NewServer(nil, nil, func(ctx context.Context, text string) (string, int, error) {
		close(asking)
		answered <- s.Ask(ctx, "write_file", `{"path":"main.go"}`)
		return "", 0, nil
	})
	s.AnswerWait = time.Hour // only Escape can end this
	if err := s.Listen(socket); err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })

	c := Dial(socket)
	go c.Prompt(context.Background(), "tulis berkas")
	<-asking

	if err := c.StopTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case allowed := <-answered:
		if allowed {
			t.Error("an abandoned question must not come back as yes")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the question was left waiting after its turn was abandoned")
	}
}
