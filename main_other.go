//go:build !linux && !darwin

package main

import "errors"

// detach is not built here: gnosh cannot start a process that outlives
// its terminal on this system.
func detach(self string, args, env []string, log string) (int, error) {
	return 0, errors.New("a session cannot go on without its terminal on this system")
}
