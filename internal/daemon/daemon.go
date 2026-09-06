// Package daemon runs the agent as a process that outlives the terminal, and
// speaks to the front ends over a unix socket.
//
// It exists for one thing that can be seen today: a /bg task is a goroutine in
// the front end's own process, so closing the tab kills the work mid-flight
// and loses its report. Everything else a daemon is usually built for — an
// editor client, LSP, a second terminal on the same conversation — is a reason
// to keep it, not the reason to start it.
//
// The wire is HTTP over a unix socket rather than a protocol of its own: it
// costs nothing to speak, every language has a client, and curl can drive it
// while it is being written. Events go out as text/event-stream for the same
// reason the providers use it — the answer arrives while it is being written,
// and the front end has nothing else to wait for.
package daemon

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/didik-prabowo/uhai/internal/config"
)

// socketPerm is why the socket is not in /tmp. Anything that can open it can
// ask the agent to run a command, so it is 0600 in the user's own directory
// and never world-readable: a socket that grants a shell is a credential.
const socketPerm fs.FileMode = 0o600

// SocketPath is where the daemon listens. One per user rather than one per
// project: the process holds sessions and tasks, which are already per-user,
// and a socket per working directory would leave one behind in every folder
// uhai was ever run in.
func SocketPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.sock"), nil
}

// removeStale clears a socket left behind by a daemon that did not shut down,
// and refuses when the socket is live.
//
// The check matters now that front ends start daemons on their own: two of
// them can reach this at once, and a plain os.Remove would let the loser
// delete the winner's socket. The winner would go on running, reachable by
// nobody, while the loser listened on a new file — the worst kind of failure,
// because both processes believe they succeeded.
//
// A live daemon answers a dial. A leftover file does not: the address exists
// but nothing is accepting, and connect fails immediately.
func removeStale(path string) error {
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	conn, err := net.DialTimeout("unix", path, dialProbe)
	if err == nil {
		conn.Close()
		return fmt.Errorf("a daemon is already listening on %s", path)
	}
	return os.Remove(path)
}

// dialProbe is how long to wait for an answer before calling a socket dead.
// Generous for a connect to something on the same machine, which either
// answers at once or is not there.
const dialProbe = 250 * time.Millisecond
