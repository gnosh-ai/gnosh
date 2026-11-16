//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// detach starts gnosh with args as a process of its own, in a session of
// its own, so that it goes on when this terminal and this process are
// gone, in the environment env, with nothing to read and what it prints in
// the file at log. It
// returns the new process's id, and does not wait for it.
func detach(self string, args, env []string, log string) (int, error) {
	out, err := os.OpenFile(log, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	cmd := exec.Command(self, args...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	// (Not waited for: it goes on after this process ends.)
	_ = cmd.Process.Release()
	return pid, nil
}
