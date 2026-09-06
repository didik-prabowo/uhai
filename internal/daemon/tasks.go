package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/didik-prabowo/uhai/internal/task"
)

// TaskView is a task on the wire. task.Task carries an error, which marshals
// to {} and says nothing, so the wire says it in words instead.
type TaskView struct {
	ID          string        `json:"id"`
	Description string        `json:"description"`
	Status      string        `json:"status"`
	Elapsed     time.Duration `json:"elapsed"`
	Tokens      int           `json:"tokens"`
	Report      string        `json:"report,omitempty"`
	Output      string        `json:"output,omitempty"`
	Err         string        `json:"err,omitempty"`
}

func view(t task.Task) TaskView {
	v := TaskView{
		ID: t.ID, Description: t.Description, Status: string(t.Status),
		Elapsed: t.Elapsed, Tokens: t.Tokens, Report: t.Report, Output: t.Output,
	}
	if t.Err != nil {
		v.Err = t.Err.Error()
	}
	return v
}

// Runner is what the daemon does with a prompt. Injected rather than built
// here, so this package never has to know what an agent or a provider is: it
// owns the socket and the lifetime, not the work.
type Runner func(ctx context.Context, prompt string) (report string, tokens int, err error)

func (s *Server) handlePostTasks(w http.ResponseWriter, r *http.Request) {
	if s.Run == nil {
		http.Error(w, "this daemon cannot run tasks: no provider was connected when it started", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Prompt == "" {
		http.Error(w, "a prompt is required", http.StatusBadRequest)
		return
	}

	// Started and not waited for. The whole point is that whoever asked can
	// leave — that is what the front end could not do when the task was a
	// goroutine in its own process.
	started := make(chan task.Task, 1)
	go func() {
		//nolint:errcheck // the task records its own failure; nobody is here to tell
		s.Tasks.Run(context.WithoutCancel(r.Context()), short(body.Prompt),
			func(ctx context.Context, t task.Task) (string, int, error) {
				started <- t
				report, tokens, err := s.Run(ctx, body.Prompt)
				s.Publish(Event{Kind: EventTaskDone, Task: t.ID})
				return report, tokens, err
			})
	}()

	select {
	case t := <-started:
		writeJSON(w, view(t))
	case <-time.After(5 * time.Second):
		// Queued behind other work rather than running. It exists and has an
		// id, but this request has nothing to hand back yet.
		http.Error(w, "the task was accepted but is still queued", http.StatusAccepted)
	}
}

func (s *Server) handleGetTasks(w http.ResponseWriter, r *http.Request) {
	out := []TaskView{}
	for _, t := range s.Tasks.Snapshot() {
		out = append(out, view(t))
	}
	writeJSON(w, out)
}

func (s *Server) handleStopTask(w http.ResponseWriter, r *http.Request) {
	if !s.Tasks.Stop(r.PathValue("id")) {
		http.Error(w, "no such task, or it had already finished", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"stopped": true})
}

// short is a prompt cut down to something a task list can show.
func short(prompt string) string {
	const max = 40
	if len(prompt) <= max {
		return prompt
	}
	return prompt[:max] + "…"
}
