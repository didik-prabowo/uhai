//go:build !windows

// The two things starting a daemon needs from the operating system: a lock
// that a second front end cannot take, and a way to cut the child loose from
// the terminal. Both differ enough between systems to be worth a file each,
// and neither idea does.
package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

// tryLock takes an exclusive lock without waiting, returning the release.
// Non-blocking on purpose: lockSpawn polls so it can be told about ctx, and a
// front end that cannot be interrupted while starting a daemon is worse than
// one that occasionally spawns twice.
func tryLock(f *os.File) (unlock func(), ok bool) {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, false
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }, true
}

// detach cuts the daemon loose from the terminal's session. Without it the
// daemon dies with the terminal, which would leave a background task running
// in a second process that dies at the same moment as the first: the same bug,
// further away.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
