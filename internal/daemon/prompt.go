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

	_, _, err := s.Prompt(context.WithoutCancel(r.Context()), body.Text)
	said := ""
	if err != nil {
		said = err.Error()
	}
	s.Publish(Event{Kind: EventDone, Text: said})
	writeJSON(w, map[string]any{"done": true, "err": said})
}
