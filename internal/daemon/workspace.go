package daemon

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"

	"github.com/didik-prabowo/uhai/internal/task"
)

// projectHeader names the project a request is about. Every route carries it,
// because one daemon now serves every project on the machine and nothing else
// in a request says which tree it means.
//
// A header rather than a path segment or a body field: it applies uniformly to
// GET and POST alike, and a route that forgot it would have to forget it
// somewhere visible rather than by leaving a JSON key unset.
const projectHeader = "X-Uhai-Project"

// workspace is everything the daemon holds for one project. Every field here
// was a field on Server when there was one project per daemon, and moving them
// is the whole change: an agent, its tasks, its questions, its watchers, and
// the lock that keeps its conversation to one turn at a time.
//
// Nothing is shared between two of these on purpose. A cross-project leak is
// this design's characteristic failure — it is what the socket-per-project
// version could not express at all — so the isolation is per-object rather
// than per-lookup, and the tests below name it directly.
type workspace struct {
	root string

	tasks     *task.Registry
	run       Runner // what a background task does
	prompt    Runner // one turn of this project's conversation
	questions questions

	turning  sync.Mutex
	stopTurn context.CancelFunc

	mu       sync.Mutex
	watchers map[chan Event]struct{}
}

// Builder makes the two runners for one project. The daemon calls it the first
// time a project is heard from, so a machine with ten checkouts pays for the
// ones actually used.
type Builder func(root string) (run, prompt Runner, err error)

// workspaceFor is the project a request is about, created if this is the first
// time it has been heard from.
//
// A missing header is an error rather than a default. Defaulting to the
// daemon's own directory is exactly how the socket-per-user version answered
// project B with project A's instructions, and it did it silently: the caller
// has to say which tree it means, every time.
func (s *Server) workspaceFor(r *http.Request) (*workspace, error) {
	root := r.Header.Get(projectHeader)
	if root == "" {
		return nil, fmt.Errorf("no %s header: every request has to say which project it is about", projectHeader)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// Symlinks resolved, so /tmp and /private/tmp are one project rather than
	// two workspaces that cannot see each other's work. Same rule the session
	// store uses, and the same reason.
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if ws, ok := s.projects[abs]; ok {
		return ws, nil
	}

	ws := &workspace{root: abs, tasks: &task.Registry{}, watchers: map[chan Event]struct{}{}}
	if s.New != nil {
		run, prompt, err := s.New(abs)
		if err != nil {
			// Remembered anyway, without runners: the project exists and can
			// be listed and watched, and asking it to run something says why.
			s.projects[abs] = ws
			return ws, nil
		}
		ws.run, ws.prompt = run, prompt
	}
	s.projects[abs] = ws
	return ws, nil
}

// publish hands one event to the front ends watching this project, and only
// this project. A terminal attached to project A must never see project B's
// answer being written.
func (w *workspace) publish(e Event) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for ch := range w.watchers {
		select {
		case ch <- e:
		default: // a front end that has stopped draining has gone away
		}
	}
}

func (w *workspace) watch() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	w.mu.Lock()
	w.watchers[ch] = struct{}{}
	w.mu.Unlock()

	return ch, func() {
		w.mu.Lock()
		delete(w.watchers, ch)
		close(ch)
		w.mu.Unlock()
	}
}

// workspace is one project's, by resolved root, or nil. For tests and for
// Publish, which both know the root and not the request.
func (s *Server) workspace(root string) *workspace {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projects[root]
}
