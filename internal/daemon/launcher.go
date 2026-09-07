package daemon

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// startWait is how long to give a daemon to open its socket. Long enough for a
// cold process on a loaded machine, short enough that a front end waiting on it
// has not visibly stopped.
const startWait = 3 * time.Second

// envSpawned marks a process this package started, so one that lands back in
// Ensure knows it is the child and stops rather than spawning again.
const envSpawned = "UHAI_DAEMON_SPAWNED"

// lockWait is how long to queue behind another front end's spawn before giving
// up on being polite. Longer than startWait, because the wait that matters is
// the winner's whole spawn plus its readiness loop, and losing the lock race
// is not a reason to fail.
const lockWait = startWait + 2*time.Second

// lockPath is the file that serialises spawning, beside the socket. A separate
// file rather than the socket itself: locking the socket would tie the right
// to *start* a daemon to a file the daemon deletes when it stops.
func lockPath(socket string) string {
	return strings.TrimSuffix(socket, ".sock") + ".lock"
}

// lockSpawn takes an exclusive lock on the spawn, so two front ends reaching
// Ensure at the same moment do not both start a daemon. They already survived
// it — the loser fails to bind, exits, and its client finds the winner on the
// next poll — but through a failure written to the log rather than by design,
// and a log full of "a daemon is already listening" teaches the next reader
// that something is wrong when nothing is.
//
// Non-blocking flock in a poll loop rather than a blocking one: a blocking
// call cannot be told about ctx, and a front end that cannot be interrupted
// while starting a daemon is worse than one that occasionally spawns twice.
//
// A lock that cannot be taken at all is not fatal — crush makes the same call.
// The unsynchronised path is what this has always done, and being unable to
// create a file is a reason to be careful, not a reason to refuse to work.
func lockSpawn(ctx context.Context, socket string) (release func(), ok bool) {
	f, err := os.OpenFile(lockPath(socket), os.O_CREATE|os.O_RDWR, socketPerm)
	if err != nil {
		return func() {}, false
	}
	deadline := time.Now().Add(lockWait)
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				f.Close()
			}, true
		}
		if time.Now().After(deadline) {
			f.Close()
			return func() {}, false
		}
		select {
		case <-ctx.Done():
			f.Close()
			return func() {}, false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Ensure returns a client for the daemon, starting one if none is running.
//
// Started rather than required: a daemon nobody remembers to run is a daemon
// that never runs, and the feature it carries — background work outliving the
// terminal — is exactly the one whose absence is invisible until the moment it
// matters.
//
// The started process is detached with Setsid. Without it the daemon joins the
// terminal's session and dies with it, which would leave a background task
// running in a second process that dies at the same moment as the first: the
// same bug, further away.
func Ensure(ctx context.Context, socket string) (*Client, error) {
	if c, ok := Running(socket); ok {
		return c, nil
	}
	// A daemon that is there and speaks another version holds the socket, so
	// a second one could not bind — it has to go before this one can start.
	// It is asked to stand down rather than told to: one daemon serves every
	// project on the machine, and a build mismatch in this terminal is not a
	// reason to end another project's running task.
	//
	// Refusing leaves the old daemon running and this front end without one,
	// which is what it did before it could ask at all. The sentence names the
	// mismatch either way, since that is the thing nothing else reports.
	probe := Dial(socket)
	if _, err := probe.status(ctx); err != nil {
		var wrong ErrWrongVersion
		if errors.As(err, &wrong) {
			if err := probe.ShutdownIfIdle(ctx); err != nil {
				return nil, fmt.Errorf("%w (it would not stand down: %v)", wrong, err)
			}
			// The socket goes with the daemon, and until it does a new one
			// cannot bind. Waiting for it beats racing it.
			if err := awaitSocketGone(ctx, socket); err != nil {
				return nil, fmt.Errorf("%w (it agreed to stop and did not: %v)", wrong, err)
			}
		}
	}
	// A binary that does not understand -daemon runs its front end again,
	// which calls Ensure, which starts another — a chain with no end. Seen
	// exactly once, from a stand-in that had no -daemon flag, and the log
	// filled with the same sentence from a new process each time.
	//
	// The child is marked, so a child that finds itself here knows the spawn
	// did not take and refuses to make it worse.
	if os.Getenv(envSpawned) != "" {
		return nil, fmt.Errorf("this process was started as a daemon but is not serving — " +
			"refusing to start another")
	}

	// From here on only one front end at a time, and the first thing the
	// winner does is look again: the daemon it was about to start may have
	// been started by whoever held the lock a moment ago.
	release, locked := lockSpawn(ctx, socket)
	defer release()
	if locked {
		if c, ok := Running(socket); ok {
			return c, nil
		}
	}

	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("could not find uhai to start a daemon: %w", err)
	}

	log, err := os.OpenFile(LogPath(socket), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	defer log.Close()

	cmd := exec.Command(self, "-daemon")
	cmd.Env = append(os.Environ(), envSpawned+"=1")
	cmd.Stdout, cmd.Stderr = log, log
	// Nothing to read: a daemon that inherits the terminal's stdin competes
	// with the front end for keystrokes.
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("could not start a daemon: %w", err)
	}
	// Released rather than waited for. That is the point of it.
	_ = cmd.Process.Release()

	deadline := time.Now().Add(startWait)
	for {
		if c, ok := Running(socket); ok {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("a daemon was started but did not answer within %s — see %s",
				startWait, LogPath(socket))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// awaitSocketGone waits for a stopping daemon to take its socket with it. The
// file is the lock on being the daemon: while it is there, Listen fails.
func awaitSocketGone(ctx context.Context, socket string) error {
	deadline := time.Now().Add(startWait)
	for {
		if _, err := os.Stat(socket); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("its socket is still there after %s", startWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
