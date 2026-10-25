package agent

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/stretchr/testify/require"
)

// Claude Code's auto-compaction variables bring compaction earlier and
// never later: CLAUDE_CODE_AUTO_COMPACT_WINDOW is how full the context may
// get (100k to 1M tokens, with k and M suffixes, capped at the model's
// window), and CLAUDE_AUTOCOMPACT_PCT_OVERRIDE the percentage of that
// window at which to compact. A value that is no number is ignored.
func TestAutoCompactVariables(t *testing.T) {
	const qwen = 262_144 // the AI box's Qwen3.8-27B
	at := func(window, percent string, cw int64) int64 {
		t.Helper()
		t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", window)
		t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", percent)
		return compactAt(cw, 0, true)
	}

	require.EqualValues(t, qwen-20_000, at("", "", qwen), "by default, 20k short of a large window")
	require.EqualValues(t, 65_536-13_107, at("", "", 65_536), "and 20% short of a small one")
	require.EqualValues(t, 32_768-6_553, at("", "", 0), "a window not known is taken as 32k")

	require.EqualValues(t, qwen/2, at("", "50", qwen))
	require.EqualValues(t, 32_768, at("", "50", 65_536))
	require.EqualValues(t, qwen-20_000, at("", "95", qwen), "a percentage cannot bring compaction later")
	for _, bad := range []string{"0", "101", "half", "-5"} {
		require.EqualValues(t, qwen-20_000, at("", bad, qwen), bad)
	}

	require.EqualValues(t, 150_000, at("150k", "", qwen))
	require.EqualValues(t, 150_000, at("150000", "", qwen))
	require.EqualValues(t, 75_000, at("150k", "50", qwen), "the percentage is of the window")
	require.EqualValues(t, 100_000, at("200", "", qwen), "under 100k is 100k")
	require.EqualValues(t, qwen-20_000, at("1M", "", qwen), "capped at the model's window")
	require.EqualValues(t, qwen/2, at("1M", "50", qwen), "the percentage is of the capped window")
	require.EqualValues(t, qwen-20_000, at("0.5M", "", qwen))
	require.EqualValues(t, qwen-20_000, at("2M", "", qwen))
	require.EqualValues(t, qwen-20_000, at("lots", "", qwen), "no number is ignored")
	require.EqualValues(t, 32_768-6_553, at("150k", "", 0))

	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "150k")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "50")
	require.EqualValues(t, qwen-20_000, compactAt(qwen, 0, false), "the default point reads no variable")

	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	require.True(t, autoSummarize(qwen, 0, qwen/2, false, true))
	require.False(t, autoSummarize(qwen, 0, qwen/2-1, false, true))
	require.False(t, autoSummarize(qwen, 0, qwen, true, true), "disabled stays disabled")
}

// The auto_compact_window option /autocompact sets is a window as the
// variable is: raised to 100k, lowered to 1M, capped at the model's
// window, and what the percentage is of. The variable, when set, takes
// precedence, and the default point reads neither.
func TestAutoCompactWindowOption(t *testing.T) {
	const qwen = 262_144
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	require.EqualValues(t, 150_000, compactAt(qwen, 150_000, true))
	require.EqualValues(t, 100_000, compactAt(qwen, 50_000, true), "under 100k is 100k")
	require.EqualValues(t, qwen-20_000, compactAt(qwen, 2_000_000, true), "capped at the model's window")
	require.EqualValues(t, 1_000_000, compactAt(2_000_000, 600_000_000, true), "over 1M is 1M")
	require.EqualValues(t, qwen-20_000, compactAt(qwen, 150_000, false), "the default point reads no option")

	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "50")
	require.EqualValues(t, 75_000, compactAt(qwen, 150_000, true), "the percentage is of the option's window")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "200k")
	require.EqualValues(t, 200_000, compactAt(qwen, 150_000, true), "the variable takes precedence")
}

// A session compacts at the auto_compact_window option as it is set when
// it asks, so /autocompact needs no restart; with no config it is auto.
func TestCompactsAtTheWindowOptionAsItIsNow(t *testing.T) {
	const qwen = 262_144
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	cfg := &config.Config{Options: &config.Options{}}
	a := &sessionAgent{cfg: config.NewTestStore(cfg), compactLevels: csync.NewMap[string, int](), justSummarized: csync.NewMap[string, bool]()}
	require.False(t, a.compacts("s", qwen, 200_000), "auto")
	cfg.Options.AutoCompactWindow = 150_000
	require.True(t, a.compacts("s", qwen, 200_000), "set since")
	require.False(t, a.compacts("s", qwen, 149_999))
	require.Zero(t, (&sessionAgent{}).autoCompactWindow(), "no config")
}

// /context reports the point the auto_compact_window option sets, and
// none when auto-summarize is off.
func TestCompactPointReadsTheWindowOption(t *testing.T) {
	const qwen = 262_144
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", "")
	options := &config.Options{}
	c := &coordinator{cfg: config.NewTestStore(&config.Config{Options: options})}
	require.EqualValues(t, qwen-20_000, c.compactPoint(qwen))
	options.AutoCompactWindow = 150_000
	require.EqualValues(t, 150_000, c.compactPoint(qwen))
	options.DisableAutoSummarize = true
	require.Zero(t, c.compactPoint(qwen))
}
