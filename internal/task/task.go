// Package task tracks the jobs an agent spawns: what was asked, how it went,
// how long it took. It knows nothing about agents or models — only about
// bookkeeping — so anything that can produce a report can be run as a task.
package task

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// A task is one nested run of the agent, with its own history. The point is
// context: a side quest that reads ten files costs the main conversation one
// tool result — the report — instead of ten file dumps.
//
// Tasks are tracked in a registry so the user can see what ran, and so running
// one in the background later is a matter of not waiting for it, rather than a
// redesign.

// Status is where a task got to.
type Status string

const (
	StatusQueued  Status = "queued"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

// Task is one spawned run. It is read by the UI, so everything on it is either
// set once at creation or under the registry's lock.
type Task struct {
	ID          string
	Description string
	Status      Status
	Queued      time.Time
	Started     time.Time
	Elapsed     time.Duration
	Tokens      int    // how much context the task used up, estimated
	Report      string // what the task came back with
	Err         error  // set when Status is StatusFailed

	// Output is what the task has printed so far, for reading while it is
	// still working. The report is what it decided in the end; this is the
	// noise on the way there, and only the tail of it is kept.
	Output string
}

// maxOutput caps what is kept of a task's running output. A test suite prints
// thousands of lines and the end is the part worth watching.
const maxOutput = 8_000

// Registry holds the tasks of one session, and decides how many of them may
// talk to the provider at once. Without that limit, ten background tasks are
// ten simultaneous requests on one key, and the provider answers 429 to all of
// them — the work is not faster for being started at the same time.
type Registry struct {
	mu   sync.Mutex
	seq  int
	list []*Task

	// slots is the queue: a token is taken before the work runs and put back
	// after, so tasks past the limit wait their turn rather than being
	// refused. Created on first use, since the zero Registry is the one every
	// caller builds.
	slots chan struct{}

	// stops holds the cancel of every task still in flight, queued ones
	// included, so /stop reaches work that has not started yet.
	stops map[string]context.CancelFunc
}

// MaxRunning is how many tasks may run at once. Two is enough to keep a wait
// on one from stalling another, and few enough that a free-tier key survives
// it.
//
// ponytail: one number for every provider. Read it per provider only when a
// key that allows more is being wasted.
const MaxRunning = 2

// wait takes a slot, blocking while the limit is reached, and returns the
// function that puts it back.
func (r *Registry) wait(ctx context.Context) (release func(), err error) {
	r.mu.Lock()
	if r.slots == nil {
		r.slots = make(chan struct{}, MaxRunning)
	}
	slots := r.slots
	r.mu.Unlock()

	select {
	case slots <- struct{}{}:
		return func() { <-slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *Registry) start(description string) *Task {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	t := &Task{
		ID:          fmt.Sprintf("t%d", r.seq),
		Description: description,
		Status:      StatusQueued,
		Queued:      time.Now(),
	}
	r.list = append(r.list, t)
	return t
}

// Progress adds to what a task has printed. It is called from the work
// itself, often line by line, so the user can watch a long job rather than
// waiting for its verdict in silence.
func (r *Registry) Progress(id, chunk string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, t := range r.list {
		if t.ID != id {
			continue
		}
		t.Output += chunk
		if len(t.Output) > maxOutput {
			// ToValidUTF8 because the cut is by byte and a character is not
			// one: landing inside a multi-byte rune left a broken glyph at the
			// head of every trimmed output, and the output of a task is where
			// non-English text is most likely to be. Same answer Execute gives
			// when it truncates a tool result.
			t.Output = "…" + strings.ToValidUTF8(t.Output[len(t.Output)-maxOutput:], "")
		}
		return
	}
}

// Stop cancels one task, whether it is running or still queued, and reports
// whether there was one to stop.
func (r *Registry) Stop(id string) bool {
	r.mu.Lock()
	cancel := r.stops[id]
	r.mu.Unlock()

	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (r *Registry) hold(id string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.stops == nil {
		r.stops = map[string]context.CancelFunc{}
	}
	r.stops[id] = cancel
}

func (r *Registry) release(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.stops, id)
}

// begin marks a queued task as running, once it has a slot.
func (r *Registry) begin(t *Task) {
	r.mu.Lock()
	defer r.mu.Unlock()

	t.Status, t.Started = StatusRunning, time.Now()
}

func (r *Registry) finish(t *Task, report string, tokens int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Elapsed is time spent working, not time spent waiting: a task that sat
	// in the queue for a minute did not take a minute.
	t.Elapsed, t.Tokens, t.Report, t.Err = time.Since(t.Started), tokens, report, err
	t.Status = StatusDone
	if err != nil {
		t.Status = StatusFailed
	}
}

// Snapshot copies the tasks for display, newest last.
func (r *Registry) Snapshot() []Task {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Task, 0, len(r.list))
	for _, t := range r.list {
		out = append(out, *t)
	}
	return out
}
