package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("could not find uhai to start a daemon: %w", err)
	}

	log, err := os.OpenFile(filepath.Join(filepath.Dir(socket), "daemon.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
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
				startWait, filepath.Join(filepath.Dir(socket), "daemon.log"))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
