package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// answerWait is how long a question waits for a human before it is denied.
//
// Denied, not allowed: the agent's own default denies everything so that a
// caller who forgets to wire a hook can never silently write files, and a
// daemon nobody is watching is the same situation from further away. A minute
// is long enough to read a prompt and short enough that a forgotten terminal
// does not hold a turn open all afternoon.
const answerWait = time.Minute

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

// Ask puts a question to whoever is watching and waits for the answer. It is
// what a daemon hands the agent as its Confirm hook.
//
// Nothing watching means nothing to ask, and the answer is no. A front end
// that attaches later can still find the question through GET /v1/questions —
// a terminal reopened mid-turn should be able to answer it — but a question
// with nobody to see it is not held open on the chance that someone arrives.
func (s *Server) Ask(ctx context.Context, tool, input string) bool {
	p := s.questions.add(tool, input)
	s.Publish(Event{Kind: EventQuestion, Question: &p.Question})

	select {
	case allowed := <-p.answer:
		return allowed
	case <-ctx.Done():
		s.questions.drop(p.ID)
		return false
	case <-time.After(s.answerWait()):
		s.questions.drop(p.ID)
		return false
	}
}

// answerWait is this daemon's deadline, so a test can shorten it without
// writing to a package variable — which raced against an Ask still running in
// another test, and the detector said so.
func (s *Server) answerWait() time.Duration {
	if s.AnswerWait > 0 {
		return s.AnswerWait
	}
	return answerWait
}

func (s *Server) handleGetQuestions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.questions.list())
}

func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Allow bool `json:"allow"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "an answer is required", http.StatusBadRequest)
		return
	}
	p := s.questions.take(r.PathValue("id"))
	if p == nil {
		// Already answered, or timed out. Not an error the front end can do
		// anything about, but it should not think it decided something.
		http.Error(w, "that question is no longer open", http.StatusGone)
		return
	}
	p.answer <- body.Allow
	writeJSON(w, map[string]any{"answered": true})
}
