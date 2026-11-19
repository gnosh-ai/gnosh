//go:build linux || darwin

package shelltool

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// readCwdFile reads the file in which a shell said where it ended. The
// command could have put something else in the file's place: a link is
// not followed, a pipe does not hold the read up (nobody may ever write
// to it), only a plain file is read, and only so much of it.
func readCwdFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("not a plain file")
	}
	return io.ReadAll(io.LimitReader(f, maxCwdFile))
}
