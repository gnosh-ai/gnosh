//go:build !linux && !darwin

package shelltool

import (
	"errors"
	"io"
	"os"
)

// readCwdFile reads the file in which a shell said where it ended: only a
// plain file, and only so much of it.
func readCwdFile(path string) ([]byte, error) {
	if st, err := os.Lstat(path); err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("not a plain file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxCwdFile))
}
