package task

import "context"

// Runner does the actual work of one task and reports back: the report is what
// the caller gets, tokens is how much context the work used up. It is a
// function rather than an interface because there is one way to run a task and
// no reason to pretend otherwise.
type Runner func(ctx context.Context, t Task) (report string, tokens int, err error)

// Run tracks one task around whatever does the work: it hands out the id,
// waits for a free slot, starts the clock, and records how it ended. The
// caller only has to know how to do the job, never how it is bookkept or when
// it gets its turn.
func (r *Registry) Run(ctx context.Context, description string, run Runner) (Task, string, error) {
	return r.track(ctx, description, true, run)
}

// RunNow is Run for work somebody is waiting on: it takes no slot. The queue
// is there to stop background work crowding the provider, and a task the user
// is watching the spinner for is not background work — queueing it behind two
// long ones would stall the conversation itself, looking for all the world
// like a model thinking very hard.
func (r *Registry) RunNow(ctx context.Context, description string, run Runner) (Task, string, error) {
	return r.track(ctx, description, false, run)
}

func (r *Registry) track(ctx context.Context, description string, queue bool, run Runner) (Task, string, error) {
	if description == "" {
		description = "task"
	}
	t := r.start(description)

	// Its own cancel, so /stop reaches this task alone — and reaches it while
	// it is still queued, where there is nothing running to interrupt.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.hold(t.ID, cancel)
	defer r.release(t.ID)

	if queue {
		slot, err := r.wait(ctx)
		if err != nil {
			// Cancelled before it ever ran: it still has to end somewhere, or
			// it sits in the list as queued for the rest of the session.
			r.finish(t, "", 0, err)
			return *t, "", err
		}
		defer slot()
	}

	r.begin(t)
	report, tokens, err := run(ctx, *t)
	r.finish(t, report, tokens, err)

	return *t, report, err
}
