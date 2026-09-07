//go:build !windows

// How a shell command is put in a group and taken down with everything it
// started. Split by platform because the two systems disagree about what a
// process group is, not because the idea differs: cancelling has to reach what
// the command spawned, or "go test" carries on compiling after /stop.
package tools

import (
	"context"
	"os/exec"
	"syscall"
)

// ownGroup puts the command in a process group of its own, so one signal
// reaches the whole tree it starts.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills the command's whole process group when ctx ends, and
// returns the function that stops watching once the command is over.
//
// The negative pid is the group: killing the shell alone leaves what it
// started running, which is not what stopping means.
func killGroup(ctx context.Context, cmd *exec.Cmd) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		case <-done:
		}
	}()
	return func() { close(done) }
}
