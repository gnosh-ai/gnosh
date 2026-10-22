package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// crash run reads what is piped into it before the prompt, but a pipe
// that is open and never written to, as an ssh session without -n gives,
// used to keep it waiting forever. Like Claude Code's -p, it now waits a
// few seconds for the first byte, then warns and goes on without stdin.
func TestPrependInput(t *testing.T) {
	old := stdinWait
	stdinWait = 100 * time.Millisecond
	t.Cleanup(func() { stdinWait = old })

	t.Run("piped data comes before the prompt", func(t *testing.T) {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		_, err = w.WriteString("panic: boom\n")
		require.NoError(t, err)
		require.NoError(t, w.Close())

		var warn bytes.Buffer
		got, err := prependInput(r, &warn, "why does this fail?")
		require.NoError(t, err)
		require.Equal(t, "panic: boom\n\n\nwhy does this fail?", got)
		require.Empty(t, warn.String())
	})

	t.Run("a pipe nobody writes to is given up on, with a warning", func(t *testing.T) {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		defer w.Close()

		var warn bytes.Buffer
		start := time.Now()
		got, err := prependInput(r, &warn, "why?")
		require.NoError(t, err)
		require.Equal(t, "why?", got)
		require.Contains(t, warn.String(), "no stdin data received in 100ms, proceeding without it")
		require.Less(t, time.Since(start), 2*time.Second)
	})

	t.Run("a pipe closed with nothing in it", func(t *testing.T) {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		require.NoError(t, w.Close())

		var warn bytes.Buffer
		got, err := prependInput(r, &warn, "why?")
		require.NoError(t, err)
		require.Equal(t, "why?", got)
		require.Empty(t, warn.String())
	})

	t.Run("data that starts in time is read to the end", func(t *testing.T) {
		r, w, err := os.Pipe()
		require.NoError(t, err)
		go func() {
			time.Sleep(20 * time.Millisecond)
			_, _ = w.WriteString("first ")
			// The rest comes after the wait is over; it must not be lost.
			time.Sleep(3 * stdinWait)
			_, _ = w.WriteString("rest")
			_ = w.Close()
		}()

		var warn bytes.Buffer
		got, err := prependInput(r, &warn, "why?")
		require.NoError(t, err)
		require.Equal(t, "first rest\n\nwhy?", got)
		require.Empty(t, warn.String())
	})

	t.Run("a redirected file is read whole", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "in.txt")
		require.NoError(t, os.WriteFile(path, []byte("from a file"), 0o644))
		f, err := os.Open(path)
		require.NoError(t, err)
		defer f.Close()

		var warn bytes.Buffer
		got, err := prependInput(f, &warn, "why?")
		require.NoError(t, err)
		require.Equal(t, "from a file\n\nwhy?", got)
	})
}
