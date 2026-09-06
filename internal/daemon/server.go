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
}

// Event kinds. Adding one is safe; changing what an old one means is not.
const (
	EventDelta    = "delta" // a piece of the answer as it is written
	EventNotice   = "notice"
	EventTaskDone = "task_done"
)

// Server is the daemon. It owns nothing yet but the socket and the fan-out —
// the agent still lives in the front end — which is deliberate: the transport
// is worth proving on its own before the thing that matters is moved onto it.
type Server struct {
	Sessions session.Store

	mu       sync.Mutex
	watchers map[chan Event]struct{}

	listener net.Listener
	http     *http.Server
}

// NewServer builds a daemon. Sessions may be nil in a test that only speaks to
// the socket.
func NewServer(store session.Store) *Server {
	return &Server{Sessions: store, watchers: map[chan Event]struct{}{}}
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
	return nil
}

// Addr is where the daemon is listening, for a caller that let Listen choose.
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Serve answers until the listener is closed.
func (s *Server) Serve() error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("GET /v1/sessions", s.handleSessions)
	mux.HandleFunc("GET /v1/events", s.handleEvents)

	s.http = &http.Server{Handler: mux}
	err := s.http.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
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

// Publish hands one event to every front end currently watching. It never
// blocks on a slow reader: a front end that has stopped draining is a front
// end that has gone away, and holding the agent up for it would stop the work
// the daemon exists to keep running.
func (s *Server) Publish(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.watchers {
		select {
		case ch <- e:
		default:
		}
	}
}

func (s *Server) watch() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	s.mu.Lock()
	s.watchers[ch] = struct{}{}
	s.mu.Unlock()

	return ch, func() {
		s.mu.Lock()
		delete(s.watchers, ch)
		close(ch)
		s.mu.Unlock()
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "pid": os.Getpid()})
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if s.Sessions == nil {
		writeJSON(w, []session.Session{})
		return
	}
	all, err := s.Sessions.All()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, all)
}

// handleEvents is the second stream in uhai, and the first one that exists
// only because there are two processes. The provider already streams the
// answer to the agent; this carries it the rest of the way.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	events, stop := s.watch()
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
