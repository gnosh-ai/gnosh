//go:build unix

package app

import (
	"os/exec"
	"syscall"
)

// groupOf starts cmd in a process group of its own, and has the end of its
// context kill the whole group: what the command started ends with it.
func groupOf(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
