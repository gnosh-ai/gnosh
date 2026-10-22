package cmd

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// TestShouldEnableMetrics: this fork sends Charm no usage metrics unless the
// user asks, where upstream Crush sends them unless told not to.
func TestShouldEnableMetrics(t *testing.T) {
	optedIn := &config.Config{Options: &config.Options{DisableMetrics: new(false)}}
	optedOut := &config.Config{Options: &config.Options{DisableMetrics: new(true)}}
	silent := &config.Config{Options: &config.Options{}}

	for _, tt := range []struct {
		name        string
		doNotTrack  string
		disableVar  string
		cfg         *config.Config
		wantEnabled bool
	}{
		{name: "a config that says nothing sends none", cfg: silent, wantEnabled: false},
		{name: "option metrics true opts in", cfg: optedIn, wantEnabled: true},
		{name: "option metrics false stays out", cfg: optedOut, wantEnabled: false},
		{name: "CRASH_DISABLE_METRICS=0 opts in", disableVar: "0", cfg: silent, wantEnabled: true},
		{name: "CRASH_DISABLE_METRICS=1 beats the config", disableVar: "1", cfg: optedIn, wantEnabled: false},
		{name: "DO_NOT_TRACK beats everything", doNotTrack: "1", disableVar: "0", cfg: optedIn, wantEnabled: false},
		{name: "an empty variable is no answer", disableVar: "", cfg: silent, wantEnabled: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DO_NOT_TRACK", tt.doNotTrack)
			t.Setenv("CRASH_DISABLE_METRICS", tt.disableVar)
			t.Setenv("CRUSH_DISABLE_METRICS", "")
			require.Equal(t, tt.wantEnabled, shouldEnableMetrics(tt.cfg))
		})
	}
}
