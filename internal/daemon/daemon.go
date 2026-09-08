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
	"strings"
	"time"

	"github.com/didik-prabowo/uhai/internal/config"
)

// socketPerm is why the socket is not in /tmp. Anything that can open it can
// ask the agent to run a command, so it is 0600 in the user's own directory
// and never world-readable: a socket that grants a shell is a credential.
// ProtoVersion is what this build speaks over the socket. Bump it whenever a
// change would make an older front end misread a newer daemon, or the other
// way round — a new event kind an old client ignores is not that, a changed
// meaning for an existing one is.
//
// It exists because a daemon outlives the terminal that started it, which is
// the whole point of having one: after `go install`, a week-old daemon is
// still holding the socket and the new binary talks to it. Without a version
// that meets as a confusing failure somewhere downstream instead of a sentence
// at the door. zero sends a CtrlHello frame carrying one; crush serves
// /v1/version; this rides along on health, which every client already calls.
// 2: EventTool carries the tool's arguments. An older daemon still answers
// every call this build makes, so nothing fails — it just publishes the tool's
// name and nothing else, and the terminal draws "⎿ grep" for a search whose
// pattern is the only part worth reading. Silently worse is exactly what the
// version is for.
// 3: health reports the model the daemon answers with. An older one does not,
// and the front end would draw an empty model row while a turn ran against a
// model nobody named.
// 4: /v1/model changes it. An older daemon answers 404, and "404 page not
// found" is not a sentence to put in front of someone who typed /model.
const ProtoVersion = 4

const socketPerm fs.FileMode = 0o600

// SocketPath is where the daemon listens: one per user, serving every project
// on the machine.
//
// It was one socket per project for a while, which made a cross-project mix-up
// impossible by construction. This is the other trade, and the one crush and
// zero both make: one process, one log, one lock, and no daemon left behind
// for every checkout ever opened. The isolation it gives up by construction is
// paid back deliberately — every request names its project, a request that
// does not is refused, and nothing a conversation needs is stored anywhere but
// in that project's own workspace.
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

// SocketHere is the daemon, whichever project the caller is in. Kept as a
// name of its own so the front ends read as asking for the daemon rather than
// building a path.
func SocketHere() (string, error) { return SocketPath() }

// LogPath is where the daemon on one socket writes. Derived from the socket
// rather than fixed, so a project's log is its own: daemons are per project
// now, and one shared file would interleave two of them into something nobody
// can read at the moment they need to.
func LogPath(socket string) string {
	return strings.TrimSuffix(socket, ".sock") + ".log"
}
