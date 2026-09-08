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
// handleSetModel points this project's conversation at another model. It is a
// route rather than something each front end does for itself because the agent
// that answers lives here: /model in an attached terminal used to swap the
// provider of a process nothing asks, and the turn still ran against the model
// the daemon started with.
func (s *Server) handleSetModel(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if ws.setModel == nil {
		http.Error(w, "this daemon cannot change the model for "+ws.root+
			": no provider was connected, or the project could not be opened", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Model == "" {
		http.Error(w, "a model is required", http.StatusBadRequest)
		return
	}

	// The turn lock, so a switch waits for the answer being written rather
	// than changing the model halfway through it. Whoever asked is a terminal
	// with a spinner on it, and waiting is what a spinner is for.
	ws.turning.Lock()
	defer ws.turning.Unlock()

	name, err := ws.setModel(body.Model)
	if err != nil {
		// The provider's own sentence: a model that does not exist, a key that
		// is missing and a URL that does not answer are three different
		// problems and only it knows which one this is.
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ws.mu.Lock()
	ws.model = name
	ws.mu.Unlock()
	writeJSON(w, map[string]any{"model": name})
}

func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if ws.prompt == nil {
		http.Error(w, "this daemon holds no conversation for "+ws.root+
			": no provider was connected, or the project could not be opened", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
		http.Error(w, "a prompt is required", http.StatusBadRequest)
		return
	}

	// One turn at a time per project, not per daemon: a conversation is a
	// single history, but two projects have two of them and neither should
	// wait for the other.
	ws.turning.Lock()
	defer ws.turning.Unlock()

	// Detached from the request and cancellable on purpose. A front end that
	// hangs up has left the room; a front end that presses Escape has decided
	// something, and those are different enough to need different routes.
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	ws.holdTurn(cancel)
	defer ws.releaseTurn()

	_, _, err = ws.prompt(ctx, body.Text)
	said := ""
	if err != nil {
		said = err.Error()
	}
	ws.publish(Event{Kind: EventDone, Text: said})
	writeJSON(w, map[string]any{"done": true, "err": said})
}

// holdTurn remembers how to stop the turn now under way in this project.
func (w *workspace) holdTurn(cancel context.CancelFunc) {
	w.mu.Lock()
	w.stopTurn = cancel
	w.mu.Unlock()
}

func (w *workspace) releaseTurn() {
	w.mu.Lock()
	stop := w.stopTurn
	w.stopTurn = nil
	w.mu.Unlock()
	if stop != nil {
		stop() // releases the context, which is not the same as cancelling the work
	}
}

// handleStopTurn is Escape, for one project. It cancels the turn under way,
// which reaches the provider mid-stream and any question still waiting for an
// answer — a question whose turn has been abandoned should not go on holding a
// terminal hostage until its deadline.
//
// Answering 200 when there was nothing to stop: a front end pressing Escape
// twice, or pressing it as the answer lands, has not made a mistake.
func (s *Server) handleStopTurn(w http.ResponseWriter, r *http.Request) {
	ws, err := s.workspaceFor(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ws.mu.Lock()
	stop := ws.stopTurn
	ws.mu.Unlock()

	if stop != nil {
		stop()
	}
	writeJSON(w, map[string]any{"stopped": stop != nil})
}
