package main

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

// With the default port taken, as it is whenever another klash already
// serves pprof, the profiler still gets a port of its own.
func TestPprofListenFallsBackWhenTheDefaultIsTaken(t *testing.T) {
	t.Parallel()

	taken, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	defer taken.Close()

	ln, err := pprofListen("1", taken.Addr().String())
	require.NoError(t, err)
	defer ln.Close()
	require.NotEqual(t, taken.Addr().String(), ln.Addr().String())
}

// The default port is used when it is free, and an address given in
// KLASH_PROFILE is used as given, with no fallback if it is taken.
func TestPprofListenUsesTheAddressAsked(t *testing.T) {
	t.Parallel()

	probe, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	free := probe.Addr().String()
	require.NoError(t, probe.Close())

	ln, err := pprofListen("1", free)
	require.NoError(t, err)
	require.Equal(t, free, ln.Addr().String())

	_, err = pprofListen(free, "localhost:0")
	require.Error(t, err, "an address KLASH_PROFILE names is not swapped for another")
	require.NoError(t, ln.Close())
}
