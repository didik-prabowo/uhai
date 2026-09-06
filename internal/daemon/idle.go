package daemon

import (
	"time"

	"github.com/didik-prabowo/uhai/internal/task"
)

// idleAfter is how long a daemon with nothing to do stays running.
//
// Half an hour rather than minutes, because stopping is not free: the
// conversation each project holds lives in that process, and a daemon that
// exits takes it with it. The turns themselves are on disk — every one is
// saved — but a daemon started again does not read them back, so an attached
// terminal after an idle exit begins a fresh conversation. Long enough to
// survive lunch is the point.
const idleAfter = 30 * time.Minute

// idleCheck is how often to look. A minute is far below the timeout, so the
// cost of asking is nothing and the answer is never much out of date.
const idleCheck = time.Minute

// busy reports whether anything would be lost by stopping now: work running or
// queued in any project, a turn in flight, or a terminal watching.
//
// Watching counts. A terminal sitting at an idle prompt has said nothing for an
// hour and is still the reason the daemon exists — pulling the socket out from
// under it would leave it drawing a session connected to nothing, which is
// exactly the failure the reconnect was written for and not a thing to cause
// on purpose.
func (s *Server) busy() bool {
	s.mu.Lock()
	projects := make([]*workspace, 0, len(s.projects))
	for _, ws := range s.projects {
		projects = append(projects, ws)
	}
	s.mu.Unlock()

	for _, ws := range projects {
		ws.mu.Lock()
		watching, turning := len(ws.watchers) > 0, ws.stopTurn != nil
		ws.mu.Unlock()
		if watching || turning {
			return true
		}
		for _, t := range ws.tasks.Snapshot() {
			if t.Status == task.StatusRunning || t.Status == task.StatusQueued {
				return true
			}
		}
	}
	return false
}

// ReapWhenIdle stops the daemon once it has been doing nothing for long
// enough. It runs until the daemon is closed, and closing is what it does: the
// socket goes with it, so the next front end starts a fresh one.
//
// This is the bill for a daemon that starts itself. Something that appears
// without being asked for has to leave without being asked too, or a machine
// collects them.
func (s *Server) ReapWhenIdle(after time.Duration, stopped func()) {
	if after <= 0 {
		after = idleAfter
	}
	idleSince := time.Now()
	tick := time.NewTicker(min(idleCheck, after))
	defer tick.Stop()

	for range tick.C {
		if s.busy() {
			idleSince = time.Now()
			continue
		}
		if time.Since(idleSince) >= after {
			if stopped != nil {
				stopped()
			}
			s.Close()
			return
		}
	}
}
