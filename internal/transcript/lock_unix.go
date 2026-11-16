//go:build unix

package transcript

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes the advisory lock that marks f's one writer, without
// waiting. It reports false when another open of the file holds it. The
// lock goes when f is closed or the process ends.
func lockFile(f *os.File) (bool, error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK):
			return false, nil
		default:
			return false, err
		}
	}
}

// InUse reports whether a gnosh holds the session file at path open for
// writing now: a session, a run or a sub-agent. It takes and lets go of a
// shared lock, which the writer's lock refuses; it reports false when the
// file cannot be read.
func InUse(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		switch {
		case err == nil:
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			return false
		case errors.Is(err, syscall.EINTR):
			continue
		default:
			return errors.Is(err, syscall.EWOULDBLOCK)
		}
	}
}
