package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetricsConsentRoundTrip(t *testing.T) {
	// Point the global data dir at a temp dir so the real one is untouched.
	t.Setenv("KLASH_GLOBAL_DATA", filepath.Join(t.TempDir(), "klash"))

	decided, _ := MetricsConsent()
	require.False(t, decided, "a fresh machine has not answered, so metrics stay off")
	require.False(t, MetricsConsentDecided())

	require.NoError(t, RecordMetricsConsent(false))
	decided, allow := MetricsConsent()
	require.True(t, decided)
	require.False(t, allow, "declining is remembered")

	require.NoError(t, RecordMetricsConsent(true))
	decided, allow = MetricsConsent()
	require.True(t, decided)
	require.True(t, allow, "opting in is remembered")
}
