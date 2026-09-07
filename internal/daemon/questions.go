package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// answerWait is how long a question outlives the last terminal watching it.
//
// It used to be the whole deadline, and that was wrong: a minute is what it
// takes to read a diff properly, so the tool was denied out from under someone
// who was still deciding, and the turn carried on as if they had said no. What
// actually ends the wait is nobody being there to answer — so that is what is
// waited for, and this is only the grace after the last watcher goes, since an
// SSE connection that drops and comes back is not somebody leaving.
//
// Denied when it does run out, not allowed: the agent's own default denies
// everything so that a caller who forgets to wire a hook can never silently
// write files, and a daemon nobody is watching is the same situation from
// further away.
const answerWait = time.Minute

// watchPoll is how often the wait looks up to see whether anyone is still
// there. Bounded to a quarter of the grace so a test can shorten both with one
// field, and to a second at the top because nothing here is in a hurry.
func watchPoll(grace time.Duration) time.Duration {
	return min(max(grace/4, 10*time.Millisecond), time.Second)
}

// Question is one thing the daemon needs a human to decide. It leaves as an
// event and comes back as a POST, which is the whole reason moving the
// conversation into a daemon is a piece of work rather than a move: in one
// process this is a function call that returns a bool.
type Question struct {
	ID    string    `json:"id"`
	Tool  string    `json:"tool"`
	Input string    `json:"input"`
	Asked time.Time `json:"asked"`
}

type pending struct {
	Question
	answer chan bool
}

type questions struct {
	mu   sync.Mutex
	seq  int
	open map[string]*pending
}

func (q *questions) add(tool, input string) *pending {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.open == nil {
		q.open = map[string]*pending{}
	}
	q.seq++
	p := &pending{
		Question: Question{ID: fmt.Sprintf("q%d", q.seq), Tool: tool, Input: input, Asked: time.Now()},
		// Buffered, so answering never blocks on an asker that has already
		// given up and gone.
		answer: make(chan bool, 1),
	}
	q.open[p.ID] = p
	return p
}

func (q *questions) take(id string) *pending {
	q.mu.Lock()
	defer q.mu.Unlock()
	p := q.open[id]
	delete(q.open, id)
	return p
}

func (q *questions) drop(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.open, id)
}

func (q *questions) list() []Question {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := []Question{}
	for _, p := range q.open {
		out = append(out, p.Question)
	}
	return out
}

// Ask puts a question to whoever is watching this project and waits for the
// answer. It is what a daemon hands one project's agent as its Confirm hook.
//
// Nothing watching means nothing to ask, and the answer is no. A front end
// that attaches later can still find the question through GET /v1/questions —
// a terminal reopened mid-turn should be able to answer it — but a question
// with nobody to see it is not held open on the chance that someone arrives.
//
// While somebody is watching, it waits as long as they take.
func (s *Server) Ask(ctx context.Context, root, tool, input string) bool {
	s.mu.Lock()
	ws := s.projects[root]
	s.mu.Unlock()
	if ws == nil {
		return false // a project nobody has opened has nobody to ask
	}
	return ws.ask(ctx, s.answerWait(), tool, input)
}

func (w *workspace) ask(ctx context.Context, grace time.Duration, tool, input string) bool {
	p := w.questions.add(tool, input)
	w.publish(Event{Kind: EventQuestion, Question: &p.Question})

	tick := time.NewTicker(watchPoll(grace))
	defer tick.Stop()

	// When the watchers last ran out, zero while somebody is there. Polled
	// rather than signalled: watch() would have to broadcast on unwatch and
	// every question would need to be listening, which is machinery for a
	// question that has to be answered within a second of the terminal
	// closing — and nothing is waiting on that second.
	var alone time.Time
	for {
		select {
		case allowed := <-p.answer:
			return allowed
		case <-ctx.Done():
			w.questions.drop(p.ID)
			return false
		case <-tick.C:
			if w.watched() {
				alone = time.Time{}
				continue
			}
			if alone.IsZero() {
				alone = time.Now()
			}
			if time.Since(alone) >= grace {
				w.questions.drop(p.ID)
				return false
			}
		}
	}
}

// answerWait is this daemon's grace, so a test can shorten it without writing
// to a package variable — which raced against an Ask still running in another
// test, and the detector said so.
func (s *Server) answerWait() time.Duration {
	if s.AnswerWait > 0 {
		return s.AnswerWait
	}
	return answerWait
}

func (s *Server) handleGetQuestions(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, ws.questions.list())
}

func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var body struct {
		Allow bool `json:"allow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "an answer is required", http.StatusBadRequest)
		return
	}
	// Taken from this project's questions only, so a terminal in one project
	// cannot answer another's — the ids are per project and would otherwise
	// collide at q1.
	p := ws.questions.take(r.PathValue("id"))
	if p == nil {
		http.Error(w, "that question is no longer open", http.StatusGone)
		return
	}
	p.answer <- body.Allow
	writeJSON(w, map[string]any{"answered": true})
}
