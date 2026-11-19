//go:build !unix

package app

import "os/exec"

// groupOf leaves cmd as it is: there are no process groups to start it in
// here, and the end of its context kills the command alone.
func groupOf(cmd *exec.Cmd) {}
