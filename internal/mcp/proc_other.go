//go:build !unix

package mcp

import "os/exec"

// ownGroup does nothing where there are no process groups.
func ownGroup(cmd *exec.Cmd) {}

// stop ends the server: there is no gentler signal to send here.
func stop(cmd *exec.Cmd, step int, reaped bool) {
	if !reaped {
		_ = cmd.Process.Kill()
	}
}
