package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/didik-prabowo/uhai/internal/provider"
	"github.com/didik-prabowo/uhai/internal/session"
	"github.com/didik-prabowo/uhai/internal/session/filestore"
)

// listen starts a daemon and hands back a client for one project. The project
// is a directory of its own so two of them in one test are genuinely two
// projects, the way they are on a real machine.
func listen(t *testing.T, store session.Store) (*Server, *Client, string) {
	t.Helper()
	s, dir := serve(t, store, nil)
	return s, s.clientFor(t, dir, "proyek"), s.Addr()
}

// serve starts a daemon and returns it with the directory its projects live
// under.
func serve(t *testing.T, store session.Store, build Builder) (*Server, string) {
	t.Helper()
	// Not t.TempDir(): a macOS temp path is long enough to pass the 104-byte
	// limit a unix socket address has, and the failure is a bind error that
	// says nothing about length.
	dir, err := os.MkdirTemp("", "u")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	s := NewServer(store, build)
	if err := s.Listen(filepath.Join(dir, "d.sock")); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Close() })
	return s, dir
}

// clientFor is a client talking about one project under dir.
func (s *Server) clientFor(t *testing.T, dir, project string) *Client {
	t.Helper()
	root := filepath.Join(dir, project)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	return DialFor(s.Addr(), root)
}

// rootOf is the resolved path the daemon keys a project by, which is what
// Publish and Ask take.
func rootOf(t *testing.T, dir, project string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(filepath.Join(dir, project))
	if err != nil {
		t.Fatal(err)
	}
	return real
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
	s, dir := serve(t, nil, nil)
	c := s.clientFor(t, dir, "proyek")
	root := rootOf(t, dir, "proyek")
	// The workspace has to exist before anything can be published into it,
	// which is what the first request does.
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := c.Events(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// Published after the subscription, and read before the turn would have
	// ended: the point of a stream is that it arrives while it is written.
	go func() {
		s.Publish(root, Event{Kind: EventDelta, Text: "ha"})
		s.Publish(root, Event{Kind: EventDelta, Text: "lo"})
		s.Publish(root, Event{Kind: EventTaskDone, Task: "t1"})
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
	s, dir := serve(t, nil, nil)
	c := s.clientFor(t, dir, "proyek")
	root := rootOf(t, dir, "proyek")

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := c.Events(ctx); err != nil {
		t.Fatal(err)
	}
	cancel() // subscribed, then gone without draining

	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			s.Publish(root, Event{Kind: EventDelta, Text: "x"})
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
	// The listing is this project's, so the fixture has to be in it.
	s.Root = filepath.Join(dir, "proyek")
	s.Messages = []provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.ContentBlock{{Type: provider.BlockText, Text: "halo"}},
	}}
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}

	srv, sdir := serve(t, store, nil)
	_ = sdir
	c := DialFor(srv.Addr(), filepath.Join(dir, "proyek"))
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

	s := NewServer(nil, taskBuilder(func(ctx context.Context, prompt string) (string, int, error) {
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
	}))
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

	second := NewServer(nil, nil)
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
		s := NewServer(nil, nil)
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
	s, dir := serve(t, nil, nil)
	c := s.clientFor(t, dir, "proyek")
	root := rootOf(t, dir, "proyek")
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatal(err) // opens the workspace
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}

	allowed := make(chan bool, 1)
	go func() { allowed <- s.Ask(context.Background(), root, "write_file", `{"path":"main.go"}`) }()

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
	s, dir := serve(t, nil, nil)
	c := s.clientFor(t, dir, "proyek")
	root := rootOf(t, dir, "proyek")
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The grace, not a deadline: nothing is watching, so it never restarts.
	s.AnswerWait = 150 * time.Millisecond
	if s.Ask(context.Background(), root, "run_bash", `{"command":"rm -rf /"}`) {
		t.Error("a question with nobody to answer it must not be allowed")
	}
	// And it is not left open for the next terminal to trip over.
	if open := s.workspace(root).questions.list(); len(open) != 0 {
		t.Errorf("a question that was given up on is still open: %+v", open)
	}
}

// A terminal that attaches mid-turn can still find the question, rather than
// leaving the agent to wait out the timeout for nothing.
func TestAQuestionIsVisibleToATerminalThatArrivesLate(t *testing.T) {
	s, dir := serve(t, nil, nil)
	c := s.clientFor(t, dir, "proyek")
	root := rootOf(t, dir, "proyek")
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}

	go s.Ask(context.Background(), root, "edit_file", `{"path":"go.mod"}`)
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
	s, dir := serve(t, nil, nil)
	c := s.clientFor(t, dir, "proyek")
	root := rootOf(t, dir, "proyek")
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}

	go s.Ask(context.Background(), root, "write_file", "{}")
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
// says nothing. It gets as long as it likes — a deadline here denied the tool
// out from under somebody still reading the diff, and the turn carried on as
// if they had said no. What ends the wait is the terminal going away, and then
// it ends with a no.
func TestAQuestionWaitsWhileSomebodyIsWatching(t *testing.T) {
	s, dir := serve(t, nil, nil)
	s.AnswerWait = 150 * time.Millisecond // the grace after the last one leaves
	c := s.clientFor(t, dir, "proyek")
	root := rootOf(t, dir, "proyek")
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	watching, leave := context.WithCancel(context.Background())
	defer leave()
	if _, err := c.Events(watching); err != nil { // watching, and silent
		t.Fatal(err)
	}

	answered := make(chan bool, 1)
	go func() { answered <- s.Ask(context.Background(), root, "run_bash", `{"command":"rm -rf /"}`) }()

	// Many times the grace, and still open, because somebody is still there.
	select {
	case <-answered:
		t.Fatal("a question was denied while somebody was there to answer it")
	case <-time.After(time.Second):
	}

	leave()
	select {
	case allowed := <-answered:
		if allowed {
			t.Error("silence is not consent")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a question nobody can answer any more must not be held open")
	}
	if open := s.workspace(root).questions.list(); len(open) != 0 {
		t.Errorf("a question that was given up on is still open: %+v", open)
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

	s := NewServer(nil, promptBuilder(func(ctx context.Context, text string) (string, int, error) {
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
	}))
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
	s := NewServer(nil, promptBuilder(func(ctx context.Context, text string) (string, int, error) {
		<-gone
		select {
		case <-ctx.Done():
			finished <- fmt.Errorf("the turn was cancelled with its front end: %w", ctx.Err())
		default:
			finished <- nil
		}
		return "", 0, nil
	}))
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

	s := NewServer(nil, promptBuilder(func(ctx context.Context, text string) (string, int, error) {
		close(reached)
		<-ctx.Done() // a turn that would otherwise run forever
		ended <- ctx.Err()
		return "", 0, ctx.Err()
	}))
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

	// The builder is told which project it is for, which is exactly what Ask
	// needs — no test-only wiring to carry the root around.
	var s *Server
	s = NewServer(nil, func(root string) (Conversation, error) {
		return Conversation{Prompt: func(ctx context.Context, text string) (string, int, error) {
			close(asking)
			answered <- s.Ask(ctx, root, "write_file", `{"path":"main.go"}`)
			return "", 0, nil
		}}, nil
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

// taskBuilder and promptBuilder turn one runner into a Builder, so a test that
// cares about tasks does not have to say anything about conversations, and the
// other way round.
func taskBuilder(run Runner) Builder {
	return func(string) (Conversation, error) { return Conversation{Run: run}, nil }
}

func promptBuilder(prompt Runner) Builder {
	return func(string) (Conversation, error) { return Conversation{Prompt: prompt}, nil }
}

// One daemon now serves every project, which is the trade crush and zero both
// make: one process and one log, against isolation that has to be built rather
// than being free. These are the tests that pay for it.
//
// A request that does not name its project is refused. Defaulting to anything
// is how the per-user daemon answered project B with project A's instructions,
// and it did it silently.
func TestARequestWithoutAProjectIsRefused(t *testing.T) {
	s, _ := serve(t, nil, nil)

	// A client that sends no header at all, which no real one does — the
	// point is what the daemon does when one is written that way.
	bare := DialFor(s.Addr(), "")
	_, err := bare.Health(context.Background())
	if err == nil {
		t.Fatal("a request with no project must be refused, not guessed at")
	}
	if !strings.Contains(err.Error(), "which project") {
		t.Errorf("the refusal has to say what is missing, got %q", err)
	}
}

// The characteristic failure of one shared daemon: a terminal watching one
// project seeing another's answer being written.
func TestEventsDoNotLeakBetweenProjects(t *testing.T) {
	s, dir := serve(t, nil, nil)
	a := s.clientFor(t, dir, "proyek-a")
	b := s.clientFor(t, dir, "proyek-b")
	for _, c := range []*Client{a, b} {
		if _, err := c.Health(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchingB, err := b.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}

	s.Publish(rootOf(t, dir, "proyek-a"), Event{Kind: EventDelta, Text: "rahasia proyek a"})
	s.Publish(rootOf(t, dir, "proyek-b"), Event{Kind: EventDelta, Text: "milik b"})

	// B's watcher must see B's event, and it must be the first thing it sees:
	// anything of A's arriving before it is the leak.
	select {
	case e := <-watchingB:
		if e.Text != "milik b" {
			t.Errorf("a terminal watching b saw %q", e.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("b never saw its own event")
	}
}

// Task ids are per project and both registries number from t1, so one
// project's terminal must not be able to stop another's t1.
func TestTasksDoNotLeakBetweenProjects(t *testing.T) {
	release := make(chan struct{})
	s, dir := serve(t, nil, func(root string) (Conversation, error) {
		return Conversation{Run: func(ctx context.Context, prompt string) (string, int, error) {
			<-release
			return "selesai di " + filepath.Base(root), 0, nil
		}}, nil
	})
	a := s.clientFor(t, dir, "proyek-a")
	b := s.clientFor(t, dir, "proyek-b")

	if _, err := a.StartTask(context.Background(), "kerja a"); err != nil {
		t.Fatal(err)
	}
	// B sees none of it.
	if list, err := b.Tasks(context.Background()); err != nil || len(list) != 0 {
		t.Fatalf("b can see a's tasks: %+v (%v)", list, err)
	}
	// And cannot stop it, though the id is the same t1 in both.
	if err := b.StopTask(context.Background(), "t1"); err == nil {
		t.Error("b stopped a task belonging to a")
	}
	close(release)
}

// A question is answered by the project it was asked in. Ids start at q1 in
// both, so without the split a terminal in one could decide the other's.
func TestQuestionsDoNotLeakBetweenProjects(t *testing.T) {
	s, dir := serve(t, nil, nil)
	a := s.clientFor(t, dir, "proyek-a")
	b := s.clientFor(t, dir, "proyek-b")
	for _, c := range []*Client{a, b} {
		if _, err := c.Health(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	s.AnswerWait = 2 * time.Second

	answered := make(chan bool, 1)
	go func() { answered <- s.Ask(context.Background(), rootOf(t, dir, "proyek-a"), "write_file", "{}") }()

	// B sees no question, and answering "q1" there decides nothing of a's.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if open, _ := b.Questions(context.Background()); len(open) != 0 {
			t.Fatalf("b can see a's question: %+v", open)
		}
		if open, _ := a.Questions(context.Background()); len(open) == 1 {
			break
		}
	}
	if err := b.Answer(context.Background(), "q1", true); err == nil {
		t.Error("b answered a question belonging to a")
	}
	if allowed := <-answered; allowed {
		t.Error("a's question came back allowed after only b was asked")
	}
}

// One daemon serving every project means one panic can cost every project. A
// task runs in a goroutine of its own, where an unrecovered panic takes the
// process down — and it did, until this: the daemon died and every other
// project's terminal lost it with no idea why.
//
// zero avoids this by supervising worker processes, so a worker can die alone.
// The model is the daemon's to report, not the terminal's to guess. A front
// end reads the config as it is now; the daemon built its agent from the
// config as it was when it started, and /model in an attached terminal changes
// the first and not the second. The status row named one model while the turn
// ran against another, which is the one kind of wrong nothing on screen
// contradicts.
func TestHealthNamesTheModelTheDaemonAnswersWith(t *testing.T) {
	s, dir := serve(t, nil, func(root string) (Conversation, error) {
		return Conversation{
			Prompt: func(ctx context.Context, text string) (string, int, error) { return "", 0, nil },
			Model:  "9router/scan-cheap",
		}, nil
	})
	c := s.clientFor(t, dir, "proyek")

	held, holds := c.Conversation(context.Background())
	if !holds {
		t.Fatal("a daemon with a prompt runner holds a conversation")
	}
	if held.Model != "9router/scan-cheap" {
		t.Fatalf("health must name the model that answers, got %q", held.Model)
	}
}

// A daemon that picked a conversation up off disk has to say so, because the
// terminal drawing it cannot tell. The daemon leaves after half an hour idle
// and reads the newest session back when it returns, so the model remembers a
// morning that no attached screen ever drew — and the first prompt after that
// is answered with a context the person typing it has no way to see.
func TestHealthSaysWhatTheDaemonPickedUp(t *testing.T) {
	s, dir := serve(t, nil, func(root string) (Conversation, error) {
		return Conversation{
			Prompt:  func(ctx context.Context, text string) (string, int, error) { return "", 0, nil },
			Model:   "9router/scan-cheap",
			Resumed: 12, ResumedTokens: 3400,
		}, nil
	})
	c := s.clientFor(t, dir, "proyek")

	held, holds := c.Conversation(context.Background())
	if !holds {
		t.Fatal("a daemon with a prompt runner holds a conversation")
	}
	if held.Resumed != 12 || held.ResumedTokens != 3400 {
		t.Fatalf("health must report what was picked up, got %d messages and %d tokens",
			held.Resumed, held.ResumedTokens)
	}
}

// Changing the model is the daemon's to do, because the agent that answers is
// the daemon's. It used to be done in the terminal, on a provider nothing
// consults, and the turn still ran against the model the daemon started with
// while the status row named the new one.
func TestSetModelChangesWhatTheDaemonAnswersWith(t *testing.T) {
	answered := make(chan string, 1)
	s, dir := serve(t, nil, func(root string) (Conversation, error) {
		model := "9router/scan-cheap"
		return Conversation{
			Prompt: func(ctx context.Context, text string) (string, int, error) {
				answered <- model
				return "", 0, nil
			},
			Model: model,
			SetModel: func(setting string) (string, error) {
				if setting == "tidakada/apa" {
					return "", errors.New("no such model")
				}
				model = setting
				return model, nil
			},
		}, nil
	})
	c := s.clientFor(t, dir, "proyek")

	name, err := c.SetModel(context.Background(), "9router/cc/claude-opus-5")
	if err != nil {
		t.Fatalf("set model: %v", err)
	}
	if name != "9router/cc/claude-opus-5" {
		t.Fatalf("the daemon names what it resolved, got %q", name)
	}
	if held, _ := c.Conversation(context.Background()); held.Model != name {
		t.Fatalf("health must agree with the switch, got %q", held.Model)
	}
	if err := c.Prompt(context.Background(), "halo"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if ran := <-answered; ran != name {
		t.Fatalf("the turn ran against %q, not the model that was chosen", ran)
	}

	// A model the provider refuses leaves the conversation on the one that
	// works. Refusing and keeping is the whole point: the alternative is a
	// daemon with no provider because a name was mistyped.
	if _, err := c.SetModel(context.Background(), "tidakada/apa"); err == nil {
		t.Fatal("a model the provider refuses has to come back as an error")
	}
	if held, _ := c.Conversation(context.Background()); held.Model != name {
		t.Fatalf("a refused switch must change nothing, got %q", held.Model)
	}
}

// Recovering is the cheaper answer for a daemon that runs the work itself.
func TestAPanicInOneProjectDoesNotTakeTheDaemon(t *testing.T) {
	s, dir := serve(t, nil, func(root string) (Conversation, error) {
		return Conversation{Run: func(ctx context.Context, prompt string) (string, int, error) {
			panic("the model did something unexpected")
		}}, nil
	})
	a := s.clientFor(t, dir, "proyek-a")
	b := s.clientFor(t, dir, "proyek-b")
	if _, err := b.Health(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := a.StartTask(context.Background(), "sesuatu"); err != nil {
		t.Fatal(err)
	}

	// The other project is still served.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := b.Health(context.Background()); err != nil {
			t.Fatalf("project B lost its daemon because project A's task panicked: %v", err)
		}
		list, err := a.Tasks(context.Background())
		if err != nil {
			t.Fatalf("project A lost its daemon: %v", err)
		}
		if len(list) == 1 && list[0].Status == "failed" {
			// And it says what happened rather than ending as a silent nothing.
			if !strings.Contains(list[0].Err, "crashed") {
				t.Errorf("a crashed task has to say so, got %q", list[0].Err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the crashed task never ended: %+v", list)
		}
	}
}

// A daemon that restarts — upgraded, crashed, stopped by hand — used to leave
// every attached terminal silent, with nothing on screen to say so: the stream
// simply ended and the front end went on drawing a session connected to
// nothing.
func TestAWatcherSurvivesTheDaemonRestarting(t *testing.T) {
	dir, err := os.MkdirTemp("", "u")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	root := filepath.Join(dir, "proyek")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	first := NewServer(nil, nil)
	if err := first.Listen(socket); err != nil {
		t.Fatal(err)
	}
	go first.Serve()

	c := DialFor(socket, root)
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// The daemon goes away, socket and all.
	first.Close()
	os.Remove(socket)

	// And comes back, the way a restarted one does.
	second := NewServer(nil, nil)
	if err := second.Listen(socket); err != nil {
		t.Fatalf("the replacement could not listen: %v", err)
	}
	go second.Serve()
	t.Cleanup(func() { second.Close() })

	// The terminal reconnects, is told it missed something, and goes on
	// receiving.
	deadline := time.After(15 * time.Second)
	var told bool
	for {
		select {
		case e, open := <-events:
			if !open {
				t.Fatal("the watcher gave up instead of reconnecting")
			}
			if e.Kind == EventNotice && strings.Contains(e.Text, "reconnected") {
				told = true
				// Publishing needs the workspace, which the reconnect's own
				// request created. The root is resolved here rather than in
				// the goroutine: calling t from one that outlives the test
				// panics the whole package, which is what it did.
				resolved := rootOf(t, dir, "proyek")
				go func() {
					for {
						select {
						case <-ctx.Done():
							return
						case <-time.After(50 * time.Millisecond):
							second.Publish(resolved, Event{Kind: EventDelta, Text: "setelah restart"})
						}
					}
				}()
			}
			if e.Kind == EventDelta && e.Text == "setelah restart" {
				if !told {
					t.Error("the terminal reconnected without being told it had missed anything")
				}
				return
			}
		case <-deadline:
			t.Fatal("the watcher never came back")
		}
	}
}

// Asking to watch a daemon that is not there is answered now, not by a channel
// that may never produce anything.
func TestWatchingANonexistentDaemonFailsAtOnce(t *testing.T) {
	dir, err := os.MkdirTemp("", "u")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	c := DialFor(filepath.Join(dir, "tidak-ada.sock"), dir)
	if _, err := c.Events(context.Background()); err == nil {
		t.Error("watching nothing must be an error, not a wait")
	}
}

// A daemon outlives the terminal that started it, which is the point of having
// one — so after `go install` a week-old daemon is still holding the socket and
// the new binary talks to it. Without a version that meets as a confusing
// failure somewhere downstream instead of a sentence at the door.
func TestAnOlderDaemonIsNamedRatherThanTalkedTo(t *testing.T) {
	s, dir := serve(t, nil, nil)
	c := s.clientFor(t, dir, "proyek")

	// The daemon of the day: same build, so it answers.
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("a matching daemon must answer: %v", err)
	}

	// And one from before versions existed, which reports none at all.
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true,"pid":1,"conversation":true}`)
	}))
	defer old.Close()

	stale := &Client{root: dir, http: old.Client()}
	stale.base = old.URL
	_, err := stale.Health(context.Background())
	var wrong ErrWrongVersion
	if !errors.As(err, &wrong) {
		t.Fatalf("an unversioned daemon must be named as the wrong version, got %v", err)
	}
	if wrong.Daemon != 0 || wrong.Mine != ProtoVersion {
		t.Errorf("the error has to carry both versions, got %+v", wrong)
	}
	// And say what to do about it, because the fix is not obvious.
	if !strings.Contains(err.Error(), "-daemon-stop") {
		t.Errorf("the message has to say how to fix it, got %q", err)
	}
}

// Stopping is a person's decision: one daemon serves every project now, so it
// ends background work everywhere.
func TestShutdownStopsTheDaemon(t *testing.T) {
	s, dir := serve(t, nil, nil)
	c := s.clientFor(t, dir, "proyek")

	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := c.Health(context.Background()); err != nil {
			return // gone
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon was asked to stop and did not")
		}
	}
}

// A daemon that starts itself has to leave by itself, or a machine collects
// them — which this one already did: four orphaned processes turned up from
// runs days earlier, before there was a daemon at all.
func TestAnIdleDaemonStops(t *testing.T) {
	s, _ := serve(t, nil, nil)

	gone := make(chan struct{})
	go s.ReapWhenIdle(80*time.Millisecond, func() { close(gone) })

	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("an idle daemon stayed running")
	}
}

// Nothing that would be lost is lost. A task still running holds it open, and
// so does a terminal sitting at an idle prompt: pulling the socket from under
// one would leave it drawing a session connected to nothing.
func TestABusyDaemonStaysUp(t *testing.T) {
	release := make(chan struct{})
	s, dir := serve(t, nil, func(root string) (Conversation, error) {
		return Conversation{Run: func(ctx context.Context, prompt string) (string, int, error) {
			<-release
			return "", 0, nil
		}}, nil
	})
	c := s.clientFor(t, dir, "proyek")

	if _, err := c.StartTask(context.Background(), "kerja panjang"); err != nil {
		t.Fatal(err)
	}
	if !s.busy() {
		t.Fatal("a running task has to hold the daemon open")
	}

	gone := make(chan struct{})
	go s.ReapWhenIdle(50*time.Millisecond, func() { close(gone) })
	select {
	case <-gone:
		t.Fatal("the daemon stopped with a task still running")
	case <-time.After(400 * time.Millisecond):
	}

	// A watcher holds it open too, even with nothing running.
	close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := c.Events(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !s.busy() {
		if time.Now().After(deadline) {
			t.Fatal("a watching terminal has to hold the daemon open")
		}
	}
	select {
	case <-gone:
		t.Fatal("the daemon stopped with a terminal attached")
	case <-time.After(300 * time.Millisecond):
	}
}

// Two front ends reaching Ensure at once used to both spawn: the loser failed
// to bind, wrote "a daemon is already listening" to the log and exited, and
// its client found the winner on the next poll. It worked, and it taught
// anyone reading the log that something was wrong when nothing was.
func TestSpawnLockIsExclusive(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "daemon.sock")

	release, ok := lockSpawn(context.Background(), socket)
	if !ok {
		t.Fatal("could not take the lock at all")
	}

	// A second attempt gives up rather than queueing forever, and the ctx is
	// what stops it — a front end starting a daemon stays interruptible.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, ok := lockSpawn(ctx, socket); ok {
		t.Error("two spawns held the lock at the same time")
	}

	// And it is a lock, not a one-shot: released, the next one gets it.
	release()
	second, ok := lockSpawn(context.Background(), socket)
	if !ok {
		t.Error("the lock stayed held after being released")
	}
	second()
}

// The lock lives beside the socket, not on it. Locking the socket would tie
// the right to start a daemon to a file the daemon deletes when it stops.
func TestSpawnLockIsNotTheSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "daemon.sock")
	if got := lockPath(socket); got == socket {
		t.Fatalf("the lock is the socket: %q", got)
	}
	release, ok := lockSpawn(context.Background(), socket)
	if !ok {
		t.Fatal("could not take the lock")
	}
	defer release()
	if _, err := os.Stat(socket); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("taking the lock created something at the socket path: %v", err)
	}
}

// After a go install the daemon holding the socket is the old build, and the
// front end that finds it wants it gone. Wanting is not enough: one daemon
// serves every project, so it stands down only when nothing would be lost.
func TestShutdownIfIdleRefusesWhileBusy(t *testing.T) {
	release := make(chan struct{})
	run := func(ctx context.Context, prompt string) (string, int, error) {
		<-release
		return "sudah", 1, nil
	}
	s, dir := serve(t, nil, func(string) (Conversation, error) { return Conversation{Run: run, Prompt: run}, nil })
	c := s.clientFor(t, dir, "proyek")

	if _, err := c.StartTask(context.Background(), "kerja"); err != nil {
		t.Fatal(err)
	}
	// The task has to be picked up before the daemon counts as busy.
	deadline := time.Now().Add(2 * time.Second)
	for !s.busy() {
		if time.Now().After(deadline) {
			t.Fatal("the task never started")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := c.ShutdownIfIdle(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("a daemon with a task running must refuse, got %v", err)
	}
	// And it is still there, which is the whole point of refusing.
	if _, err := c.Health(context.Background()); err != nil {
		t.Errorf("it stopped anyway: %v", err)
	}

	// Idle again, it goes.
	close(release)
	for s.busy() {
		if time.Now().After(deadline.Add(2 * time.Second)) {
			t.Fatal("the task never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := c.ShutdownIfIdle(context.Background()); err != nil {
		t.Fatalf("an idle daemon must stand down, got %v", err)
	}
	gone := time.Now().Add(2 * time.Second)
	for {
		if _, err := c.Health(context.Background()); err != nil {
			return
		}
		if time.Now().After(gone) {
			t.Fatal("it agreed to stop and kept answering")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Shutdown without the query is a person's command and does not ask.
func TestShutdownDoesNotAskWhetherItIsBusy(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	run := func(ctx context.Context, prompt string) (string, int, error) {
		<-release
		return "", 0, nil
	}
	s, dir := serve(t, nil, func(string) (Conversation, error) { return Conversation{Run: run, Prompt: run}, nil })
	c := s.clientFor(t, dir, "proyek")

	if _, err := c.StartTask(context.Background(), "kerja"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !s.busy() {
		if time.Now().After(deadline) {
			t.Fatal("the task never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatalf("a person's stop takes the work with it, got %v", err)
	}
}

// Stopping a daemon that is not running is what the caller wanted: none is
// left running, and that is already true. It used to come back as an error,
// which made `make install` fail its build on any machine that had never
// started one — the step runs after every install, and the message it printed
// named a missing socket rather than anything the user had done wrong.
//
// A daemon that answers and then refuses is a different thing and still an
// error, which is why this is not an ignored exit code in the Makefile: that
// would have swallowed both.
func TestNothingListeningOnASocketThatIsNotThere(t *testing.T) {
	if Listening(filepath.Join(t.TempDir(), "nothing.sock")) {
		t.Fatal("a socket that was never created has nobody on it")
	}

	// A file where the socket should be, with no daemon behind it — what a
	// daemon that died without tidying up leaves. It has to read the same.
	stale := filepath.Join(t.TempDir(), "stale.sock")
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if Listening(stale) {
		t.Fatal("a socket nobody is bound to has nobody on it")
	}
}

func TestListeningSeesARunningDaemon(t *testing.T) {
	s, _ := serve(t, nil, nil)
	if !Listening(s.Addr()) {
		t.Fatal("a daemon that is serving has to be seen")
	}
}

// A front end that falls behind loses events — there is no alternative that
// does not have the turn waiting for a terminal — but it is told what it
// missed. Dropping in silence meant a terminal that stalled for a moment lost
// pieces of an answer and drew the rest as if it were whole.
func TestAFrontEndIsToldWhatItMissed(t *testing.T) {
	ws := &workspace{watchers: map[chan Event]int{}}
	events, stop := ws.watch()
	defer stop()

	// More than the buffer holds, with nobody reading.
	const lost = 5
	for i := 0; i < eventBuffer+lost; i++ {
		ws.publish(Event{Kind: EventDelta, Text: "x"})
	}
	for i := 0; i < eventBuffer; i++ {
		<-events
	}

	// Draining again, the gap is the first thing said, before the event that
	// got through.
	ws.publish(Event{Kind: EventDelta, Text: "the one after the gap"})
	got := <-events
	if got.Kind != EventNotice || !strings.Contains(got.Text, fmt.Sprintf("%d event(s) were lost", lost)) {
		t.Fatalf("the gap was not reported: %+v", got)
	}
	if next := <-events; next.Text != "the one after the gap" {
		t.Fatalf("and the event itself must follow it: %+v", next)
	}

	// Said once, not on every event after it.
	ws.publish(Event{Kind: EventDelta, Text: "and the next"})
	if again := <-events; again.Kind == EventNotice {
		t.Errorf("the gap was reported twice: %+v", again)
	}
}

// The description a task list shows is cut by byte, and a character is not
// one: a prompt written in anything but English was as likely as not to end in
// half a rune, which every front end then drew as a replacement glyph.
func TestAShortenedPromptIsStillValidText(t *testing.T) {
	long := strings.Repeat("perbaiki ini 日本語 ", 10)
	got := short(long)

	if got == long {
		t.Fatal("the prompt was never shortened, so the test proves nothing")
	}
	if !utf8.ValidString(got) {
		t.Errorf("the shortened prompt is not valid text: %q", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Errorf("the cut left a replacement glyph behind: %q", got)
	}
	// A prompt that already fits is handed back as it is.
	if short("singkat") != "singkat" {
		t.Error("a short prompt must not be touched")
	}
}
