package testenv

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClearConfigVars(t *testing.T) {
	t.Setenv("CRUSH_DISABLE_DEFAULT_PROVIDERS", "1")
	t.Setenv("GNOSH_SOMETHING", "1")
	t.Setenv("KLASH_DISABLE_METRICS", "1")
	t.Setenv("CRUSHED_NOT_OURS", "keep")
	t.Setenv("CRUSH", "1") // the bare marker is not a setting
	t.Setenv("PATH_FOR_TEST", "keep")

	ClearConfigVars()

	_, ok := os.LookupEnv("CRUSH_DISABLE_DEFAULT_PROVIDERS")
	require.False(t, ok, "a CRUSH_ setting must be cleared")
	_, ok = os.LookupEnv("GNOSH_SOMETHING")
	require.False(t, ok, "a GNOSH_ setting must be cleared")
	_, ok = os.LookupEnv("KLASH_DISABLE_METRICS")
	require.False(t, ok, "a KLASH_ setting must be cleared")
	require.Equal(t, "keep", os.Getenv("CRUSHED_NOT_OURS"), "only the exact prefixes are ours")
	require.Equal(t, "1", os.Getenv("CRUSH"), "the bare marker is left alone")
	require.Equal(t, "keep", os.Getenv("PATH_FOR_TEST"))
}
