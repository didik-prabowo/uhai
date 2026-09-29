package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeServer answers the protocol over a pipe, so the framing and the
// request/answer matching can be tested without a language server installed —
// and without waiting for one to index a module.
//
// It is the transport that is worth testing here. What gopls answers is
// gopls's business; what this package must get right is that an answer reaches
// the caller who asked for it, and that a server which dies does not leave
// that caller waiting.
type fakeServer struct {
	in     *bufio.Reader
	out    io.WriteCloser
	answer func(method string, params json.RawMessage) (any, error)
}

func (f *fakeServer) serve() {
	for {
		size, err := frameSize(f.in)
		if err != nil {
			return
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(f.in, body); err != nil {
			return
		}
		var msg struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		json.Unmarshal(body, &msg)
		if msg.ID == nil {
			continue // a notification wants no answer
		}
		result, err := f.answer(msg.Method, msg.Params)
		reply := map[string]any{"jsonrpc": "2.0", "id": *msg.ID}
		if err != nil {
			reply["error"] = map[string]any{"code": -32000, "message": err.Error()}
		} else {
			reply["result"] = result
		}
		out, _ := json.Marshal(reply)
		fmt.Fprintf(f.out, "Content-Length: %d\r\n\r\n%s", len(out), out)
	}
}

// dial wires a client to a fake server over two in-memory pipes and completes
// the handshake, returning the client the way start would.
func dial(t *testing.T, answer func(string, json.RawMessage) (any, error)) *client {
	t.Helper()
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()

	f := &fakeServer{in: bufio.NewReader(toServer), out: fromServer, answer: answer}
	go f.serve()

	c := &client{in: fromClient, pending: map[int]chan reply{}}
	go c.read(bufio.NewReader(toClient))
	t.Cleanup(func() { fromClient.Close(); fromServer.Close() })
	return c
}

func TestAnAnswerReachesTheCallerThatAskedForIt(t *testing.T) {
	c := dial(t, func(method string, _ json.RawMessage) (any, error) {
		return map[string]any{"method": method}, nil
	})

	var got struct {
		Method string `json:"method"`
	}
	if err := c.call(context.Background(), "workspace/symbol", map[string]any{"query": "X"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Method != "workspace/symbol" {
		t.Fatalf("the answer came back for %q", got.Method)
	}
}

// Two calls in flight, answered out of order. Without the id matching, a
// caller reads the other one's answer — and both look like valid results, so
// nothing would report it.
func TestTwoCallsDoNotReadEachOthersAnswers(t *testing.T) {
	slow := make(chan struct{})
	c := dial(t, func(_ string, params json.RawMessage) (any, error) {
		var p struct {
			Query string `json:"query"`
		}
		json.Unmarshal(params, &p)
		if p.Query == "slow" {
			<-slow // held until the second call has been answered
		}
		return map[string]any{"echo": p.Query}, nil
	})

	type answer struct {
		Echo string `json:"echo"`
	}
	first := make(chan answer, 1)
	go func() {
		var a answer
		c.call(context.Background(), "workspace/symbol", map[string]any{"query": "slow"}, &a)
		first <- a
	}()

	var second answer
	if err := c.call(context.Background(), "workspace/symbol", map[string]any{"query": "quick"}, &second); err != nil {
		t.Fatal(err)
	}
	if second.Echo != "quick" {
		t.Fatalf("the second caller read %q", second.Echo)
	}
	close(slow)
	if a := <-first; a.Echo != "slow" {
		t.Fatalf("the first caller read %q", a.Echo)
	}
}

// A server that dies wakes everyone waiting. Left to their timeouts they would
// each wait the full budget for an answer that is never coming, and a turn
// would stall for as many multiples of it as the model asked questions.
func TestADeadServerWakesTheCallersWaiting(t *testing.T) {
	toServer, fromClient := io.Pipe()
	toClient, fromServer := io.Pipe()
	go func() { io.Copy(io.Discard, toServer) }()

	c := &client{in: fromClient, pending: map[int]chan reply{}}
	go c.read(bufio.NewReader(toClient))

	done := make(chan error, 1)
	go func() {
		done <- c.call(context.Background(), "textDocument/references", map[string]any{}, nil)
	}()

	time.Sleep(20 * time.Millisecond) // let the call register before the pipe dies
	fromServer.Close()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "stopped") {
			t.Fatalf("want a stopped-server error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a caller was left waiting on a server that had gone")
	}
}

// The protocol counts lines from zero and people count from one. The
// conversion happens in one place, and this is it.
func TestPositionsAreOneBasedOutside(t *testing.T) {
	var w wireLocation
	json.Unmarshal([]byte(`{"uri":"file:///tmp/x.go","range":{"start":{"line":243,"character":30}}}`), &w)
	if got := w.location(); got.Line != 244 || got.Column != 31 || got.Path != "/tmp/x.go" {
		t.Fatalf("got %+v", got)
	}
}

func TestURIsSurviveTheRoundTrip(t *testing.T) {
	for _, path := range []string{"/tmp/x.go", "/tmp/a b/c.go", "/tmp/percent%20name.go"} {
		if got := fromURI(fileURI(path)); got != path {
			t.Errorf("%q came back as %q", path, got)
		}
	}
}

// A project says which languages it is in by the files at its root. Without
// that, a question naming no file would start every server uhai knows about
// and pay for an index of each to answer about one language.
func TestLanguagesComeFromTheProjectsOwnMarkers(t *testing.T) {
	dir := t.TempDir()
	if got := forProject(dir); len(got) != 0 {
		t.Fatalf("an empty directory speaks nothing, got %+v", got)
	}
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0644)
	got := forProject(dir)
	if len(got) != 1 || got[0].lang != "go" {
		t.Fatalf("a go.mod means Go, got %+v", got)
	}
}

func TestAFileNamesItsOwnLanguage(t *testing.T) {
	if s, ok := forFile("internal/cli/tea.go"); !ok || s.lang != "go" {
		t.Fatalf("got %+v %v", s, ok)
	}
	if _, ok := forFile("README.md"); ok {
		t.Fatal("a language nobody here speaks must say so rather than guess")
	}
}

// A server that refuses the arguments it was given says so on stderr and
// nowhere else. That output used to be thrown away, so the only symptom was a
// call that never answered — a failure with no cause written down anywhere,
// which is the kind nobody can act on.
func TestAServerThatRefusesToStartSaysWhy(t *testing.T) {
	_, err := start(context.Background(), t.TempDir(),
		[]string{"sh", "-c", "echo 'unknown flag: --stdio' >&2; exit 1"})
	if err == nil {
		t.Fatal("a server that exits immediately did not start")
	}
	if !strings.Contains(err.Error(), "unknown flag: --stdio") {
		t.Fatalf("the error has to carry what the server said, got %v", err)
	}
	// Once, not twice: the handshake and the dead pipe both know about it.
	if strings.Count(err.Error(), "unknown flag") != 1 {
		t.Fatalf("said once, got %v", err)
	}
}

func TestTailKeepsTheEndAndOffersTheFirstLine(t *testing.T) {
	var tl tail
	if got := tl.hint(); got != "" {
		t.Fatalf("a server that said nothing trails no clause, got %q", got)
	}
	tl.Write([]byte("\n\n  first line  \nsecond\n"))
	if got := tl.hint(); got != " — it said: first line" {
		t.Fatalf("got %q", got)
	}

	// A server that logs steadily must not grow it without bound.
	for i := 0; i < 100; i++ {
		tl.Write(make([]byte, 1024))
	}
	tl.mu.Lock()
	held := len(tl.buf)
	tl.mu.Unlock()
	if held > tailMax {
		t.Fatalf("the tail grew to %d, past the %d it keeps", held, tailMax)
	}
}

// A server nobody has asked anything is stopped. Until this existed none of
// them stopped until the process did, and in a daemon that is for as long as
// any terminal stays attached: four checkouts open meant four language servers
// holding four indexes, in a process nobody was watching.
func TestAnIdleServerIsStoppedAndABusyOneIsNot(t *testing.T) {
	t.Cleanup(Close)

	idle, busy := heldOpen(t), heldOpen(t)
	running.Lock()
	running.byKey = map[string]*client{"/one\x00go": idle, "/two\x00go": busy}
	running.used = map[string]time.Time{
		"/one\x00go": time.Now().Add(-time.Hour),
		"/two\x00go": time.Now(),
	}
	running.Unlock()

	if reaped := reapOnce(idleAfter); reaped != 1 {
		t.Fatalf("one server was idle and one was not, %d went", reaped)
	}

	running.Lock()
	_, keptIdle := running.byKey["/one\x00go"]
	_, keptBusy := running.byKey["/two\x00go"]
	_, stampIdle := running.used["/one\x00go"]
	running.Unlock()
	if keptIdle || stampIdle {
		t.Error("the idle server must go, and its timestamp with it")
	}
	if !keptBusy {
		t.Error("a server asked something a moment ago must be left alone")
	}

	// Stopped, not merely forgotten: a forgotten one is a process still
	// holding its index, which is the whole thing being fixed.
	if err := idle.call(context.Background(), "workspace/symbol", nil, nil); err == nil {
		t.Error("the reaped server is still answering")
	}
	if reaped := reapOnce(idleAfter); reaped != 0 {
		t.Errorf("a second sweep found %d more to stop", reaped)
	}
}

// heldOpen is a client with a real process behind it, because ending one is
// close's whole job: cat exits when its stdin does, which is what a language
// server does too.
func heldOpen(t *testing.T) *client {
	t.Helper()
	cmd := exec.Command("cat")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return &client{cmd: cmd, in: in, pending: map[int]chan reply{}}
}
