//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

// detachedProcess is Windows' "do not give this child my console". Spelled out
// rather than taken from a package, because x/sys is an indirect dependency
// here and one constant is not a reason to make it a direct one.
const detachedProcess = 0x00000008

// tryLock does not lock on Windows, and says so by failing.
//
// ponytail: no single-flight on Windows, so two front ends starting a daemon
// together both spawn. They already survive that — the loser cannot bind,
// exits, and finds the winner on its next poll — which is exactly what this
// whole package did before the lock existed; the lock only bought a quieter
// log. Upgrade to LockFileEx from golang.org/x/sys/windows when somebody runs
// uhai on Windows, since guessing at a locking API on a platform nothing here
// can run is how you ship a deadlock instead of a lock.
func tryLock(*os.File) (unlock func(), ok bool) { return nil, false }

// detach cuts the daemon loose from the console that started it, which is what
// Setsid does on unix: DETACHED_PROCESS gives it none, and a new process group
// keeps a Ctrl+C in the terminal from reaching it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | syscall.CREATE_NEW_PROCESS_GROUP,
	}
}
