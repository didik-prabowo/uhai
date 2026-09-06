package daemon

import (
	"context"
	"encoding/json"
	"net/http"
)

// handlePrompt runs one turn of the conversation the daemon holds, and answers
// when the turn is over. What the answer looked like on the way is on the
// event stream: a front end subscribes first, posts second, and draws as it
// goes — which is what it already did when the agent was a function call in
// its own process.
//
// The request's context is deliberately not the turn's. A front end that hangs
// up mid-answer has left the room, not cancelled the work: the conversation is
// the daemon's, the history has to stay consistent, and half a turn written
// into it is worse than a whole one nobody watched. Escape is a different
// thing and will need a route of its own.
func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	if s.Prompt == nil {
		http.Error(w, "this daemon holds no conversation: no provider was connected when it started", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
		http.Error(w, "a prompt is required", http.StatusBadRequest)
		return
	}

	// One turn at a time: a conversation is a single history, and two turns
	// writing to it at once interleave into something neither asked for.
	s.turning.Lock()
	defer s.turning.Unlock()

	// Detached from the request and cancellable on purpose. A front end that
	// hangs up has left the room; a front end that presses Escape has decided
	// something, and those are different enough to need different routes.
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	s.holdTurn(cancel)
	defer s.releaseTurn()

	_, _, err := s.Prompt(ctx, body.Text)
	said := ""
	if err != nil {
		said = err.Error()
	}
	s.Publish(Event{Kind: EventDone, Text: said})
	writeJSON(w, map[string]any{"done": true, "err": said})
}

// holdTurn remembers how to stop the turn now under way.
func (s *Server) holdTurn(cancel context.CancelFunc) {
	s.mu.Lock()
	s.stopTurn = cancel
	s.mu.Unlock()
}

func (s *Server) releaseTurn() {
	s.mu.Lock()
	stop := s.stopTurn
	s.stopTurn = nil
	s.mu.Unlock()
	if stop != nil {
		stop() // releases the context, which is not the same as cancelling the work
	}
}

// handleStopTurn is Escape. It cancels the turn under way, which reaches the
// provider mid-stream and any question still waiting for an answer — a
// question whose turn has been abandoned should not go on holding a terminal
// hostage until its deadline.
//
// Answering 200 when there was nothing to stop: a front end pressing Escape
// twice, or pressing it as the answer lands, has not made a mistake.
func (s *Server) handleStopTurn(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	stop := s.stopTurn
	s.mu.Unlock()

	if stop != nil {
		stop()
	}
	writeJSON(w, map[string]any{"stopped": stop != nil})
}
