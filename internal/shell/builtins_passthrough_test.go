package shell

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func init() {
	RegisterBuiltin("gnosh-test-not-a-builtin-here", func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		return ErrNotABuiltinHere
	})
}

// A builtin that does not apply where it was called (a config builtin in
// the bash tool, say) must not swallow the command: the name goes on to
// the PATH, found there or reported missing, as if never registered.
func TestBuiltinThatStepsAsideReachesThePath(t *testing.T) {
	t.Parallel()

	sh := NewShell(&Options{WorkingDir: t.TempDir()})
	_, _, err := sh.Exec(context.Background(), "gnosh-test-not-a-builtin-here")
	require.Error(t, err)
	require.Equal(t, 127, ExitCode(err), "not on the PATH, not silently done: %v", err)
}
