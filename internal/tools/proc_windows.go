//go:build windows

package tools

import (
	"context"
	"os/exec"
	"syscall"
)

// ownGroup asks Windows for a new process group, which is the nearest thing it
// has to Setpgid: CREATE_NEW_PROCESS_GROUP makes the child the root of a group
// so a signal can be aimed at the tree rather than at the shell alone.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killGroup takes the command down when ctx ends.
//
// ponytail: Process.Kill ends the shell and not what it started, where the
// unix half ends the whole group. Windows has no kill(-pid); doing this
// properly wants a Job Object, which is a chunk of syscall work for a platform
// nothing here has been able to test on. A "go test" started by a cancelled
// command therefore keeps compiling. Upgrade to CreateJobObject +
// AssignProcessToJobObject with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE when
// somebody actually runs uhai on Windows and notices.
func killGroup(ctx context.Context, cmd *exec.Cmd) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		case <-done:
		}
	}()
	return func() { close(done) }
}
