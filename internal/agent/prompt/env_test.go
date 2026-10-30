package prompt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitEnv(t *testing.T) {
	t.Parallel()
	rest, env := SplitEnv("instructions\n<env>\nWorking directory: /p\n</env>\nskills")
	require.Equal(t, "<env>\nWorking directory: /p\n</env>", env)
	require.Equal(t, "instructions\n"+envPointer+"\nskills", rest)

	// Two sessions in different places share everything but the block.
	a, _ := SplitEnv("same\n<env>dir a, date 1</env>\ntools")
	b, _ := SplitEnv("same\n<env>dir b, date 2</env>\ntools")
	require.Equal(t, a, b)

	rest, env = SplitEnv("no block here")
	require.Equal(t, "no block here", rest)
	require.Empty(t, env)
	rest, env = SplitEnv("<env> unterminated")
	require.Equal(t, "<env> unterminated", rest)
	require.Empty(t, env)
}
