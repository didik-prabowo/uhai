package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/didik-prabowo/uhai/internal/session"
)

// Event is one thing the daemon has to tell its front ends. It is deliberately
// a small closed set rather than a mirror of the agent's callbacks: what
// crosses a process boundary has to be versioned and kept, where a Go closure
// can be changed in the same commit as its caller.
type Event struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
	Task string `json:"task,omitempty"`

	// Question is set when Kind is EventQuestion: the daemon needs a human to
	// decide something before it can go on.
	Question *Question `json:"question,omitempty"`

	// Usage is set when Kind is EventUsage: what the last provider call cost.
	Usage *Usage `json:"usage,omitempty"`
}

// Usage on the wire. provider.Usage would do, but a wire type that a front end
// in another language has to match is worth spelling out where it is read.
type Usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

// Event kinds. Adding one is safe; changing what an old one means is not.
const (
	EventDelta    = "delta" // a piece of the answer as it is written
	EventNotice   = "notice"
	EventTaskDone = "task_done"
	EventQuestion = "question"

	// One turn of a conversation, in the order a front end draws them.
	EventText      = "text"      // a finished block of the answer
	EventTool      = "tool"      // a tool was called
	EventReasoning = "reasoning" // the working out, which is not the answer
	EventUsage     = "usage"
	EventDone      = "done" // Text carries the error, if any
)

// Server is the daemon. It owns nothing yet but the socket and the fan-out —
// the agent still lives in the front end — which is deliberate: the transport
// is worth proving on its own before the thing that matters is moved onto it.
type Server struct {
	Sessions session.Store

	// New builds the runners for a project the first time it is heard from.
	// Nil in a daemon that started without a provider: it can still serve
	// sessions and events, and says so when asked to run something.
	New Builder

	// AnswerWait overrides how long a question outlives the last terminal
	// watching it — while one is attached it waits as long as that takes.
	// Zero takes the default.
	AnswerWait time.Duration

	// projects is one workspace per project, created on demand. Everything a
	// conversation needs lives in there rather than here — an agent, its
	// tasks, its questions, its watchers — because one daemon now serves every
	// project on the machine and a field on Server would be a field two
	// projects share.
	mu       sync.Mutex
	projects map[string]*workspace

	listener net.Listener
	http     *http.Server
}

// NewServer builds a daemon. Sessions may be nil in a test that only speaks to
// the socket.
func NewServer(store session.Store, build Builder) *Server {
	return &Server{
		Sessions: store,
		New:      build,
		projects: map[string]*workspace{},
	}
}

// Listen opens the socket. Separate from Serve so a caller — or a test — can
// know the daemon is reachable before anything tries to reach it.
func (s *Server) Listen(path string) error {
	if err := removeStale(path); err != nil {
		return fmt.Errorf("could not clear the old socket: %w", err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	// Before anyone can connect: between Listen and Chmod the socket exists
	// with whatever umask allowed, and it is a socket that can run commands.
	if err := os.Chmod(path, socketPerm); err != nil {
		l.Close()
		return err
	}
	s.listener = l
	s.http = &http.Server{Handler: s.routes()}
	return nil
}

// Addr is where the daemon is listening, for a caller that let Listen choose.
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Serve answers until the listener is closed. The http.Server it serves with
// was built by Listen, not here: building it in Serve meant a Close from
// another goroutine could read the field while this one wrote it, which is a
// race the detector found the moment two tests ran together.
func (s *Server) Serve() error {
	if s.http == nil {
		return errors.New("Serve before Listen")
	}
	err := s.http.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// routes builds the mux. Separate so Listen can wire it before Serve runs.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/sessions", s.handleSessions)
	mux.HandleFunc("GET /v1/events", s.handleEvents)
	mux.HandleFunc("POST /v1/tasks", s.handlePostTasks)
	mux.HandleFunc("GET /v1/tasks", s.handleGetTasks)
	mux.HandleFunc("POST /v1/tasks/{id}/stop", s.handleStopTask)
	mux.HandleFunc("GET /v1/questions", s.handleGetQuestions)
	mux.HandleFunc("POST /v1/questions/{id}", s.handleAnswer)
	mux.HandleFunc("POST /v1/prompt", s.handlePrompt)
	mux.HandleFunc("POST /v1/prompt/stop", s.handleStopTurn)
	mux.HandleFunc("POST /v1/shutdown", s.handleShutdown)
	return mux
}

// Close stops serving and takes the socket away with it, so the next daemon
// does not have to clear it.
func (s *Server) Close() error {
	if s.http != nil {
		return s.http.Close()
	}
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// Publish hands one event to the front ends watching one project. The root is
// required rather than defaulted: an event sent to "the daemon" would reach
// every terminal on the machine, which is the leak this design has to prevent
// and the socket-per-project one could not express.
func (s *Server) Publish(root string, e Event) {
	s.mu.Lock()
	ws := s.projects[root]
	s.mu.Unlock()
	if ws != nil {
		ws.publish(e)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// Whether it holds a conversation, not only whether it is alive: a daemon
	// started without a provider serves sessions and events and nothing else,
	// and a front end deciding where to send a prompt needs to know which it
	// is before it sends one.
	// Whether it can hold a conversation for the project asked about, not
	// only whether it is alive: a front end deciding where to send a prompt
	// needs to know before it sends one.
	// Health refuses a nameless request like every other route. It was the
	// one that swallowed the error and answered 200 anyway, which made the
	// guard look total while leaving a door open — and a front end probing
	// with health would have been told a daemon was ready for a project it
	// had never named.
	ws, err := s.workspaceFor(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	holds := ws.prompt != nil
	writeJSON(w, map[string]any{"ok": true, "pid": os.Getpid(), "conversation": holds, "proto": ProtoVersion})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if s.Sessions == nil {
		writeJSON(w, []session.Session{})
		return
	}
	ws, err := s.workspaceFor(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	all, err := s.Sessions.All()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// This project's, the way -sessions does it. One daemon holding every
	// project must not become the one place they get mixed again.
	mine := []session.Session{}
	for _, c := range all {
		if c.Root != "" && session.SameRoot(c.Root, ws.root) {
			mine = append(mine, c)
		}
	}
	writeJSON(w, mine)
}

// handleEvents is the second stream in uhai, and the first one that exists
// only because there are two processes. The provider already streams the
// answer to the agent; this carries it the rest of the way.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	events, stop := ws.watch()
	defer stop()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	// Before the first event: the client is waiting to know it is subscribed,
	// and without this it waits for the headers instead.
	flusher.Flush()

	// A heartbeat so a front end that has lost the daemon finds out while it
	// is idle, rather than the next time it tries to say something.
	beat := time.NewTicker(20 * time.Second)
	defer beat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-beat.C:
			fmt.Fprint(w, ": beat\n\n")
			flusher.Flush()
		case e, open := <-events:
			if !open {
				return
			}
			body, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", body)
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// handleShutdown stops the daemon. It answers first and closes after, so the
// caller is told rather than seeing its connection cut and having to guess
// whether it worked.
//
// With ?if_idle it stands down only when nothing would be lost — the same
// question ReapWhenIdle asks, and the reason it is asked here too: after a
// `go install` the daemon holding the socket is the old build, and the front
// end that finds it wants it gone. Wanting is not enough. One daemon serves
// every project on the machine, so "this terminal is on a newer build" must
// never cost another project a running task.
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("if_idle") && s.busy() {
		// 409 rather than 200-with-a-field: the caller asked for something
		// that did not happen, and a status it can branch on beats a body it
		// has to read.
		http.Error(w, "the daemon is busy", http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"stopping": true, "pid": os.Getpid()})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(50 * time.Millisecond) // let the answer leave
		s.Close()
	}()
}
