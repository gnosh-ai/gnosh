//go:build unix

package mcp

import (
	"os/exec"
	"syscall"
)

// ownGroup starts the server in a process group of its own.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// stop sends the server's group the signal of one step of stopping it:
// SIGINT, then SIGTERM, then SIGKILL. reaped says the server itself has
// been waited for: its number then still names the group for as long as
// the group has a member left, but once a process answers to it again the
// group is gone and the number someone else's, who is not signalled.
func stop(cmd *exec.Cmd, step int, reaped bool) {
	pid := cmd.Process.Pid
	if reaped && syscall.Kill(pid, 0) == nil {
		return
	}
	sig := []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL}[min(step, 2)]
	_ = syscall.Kill(-pid, sig)
}
