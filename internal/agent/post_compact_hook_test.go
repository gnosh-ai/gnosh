package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// Once the summary is saved, the PostCompact hooks get it, with the
// trigger, as in Claude Code.
func TestSummarize_PostCompactGetsTheSummary(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	r := compactHookAgent(t, hooks.EventPostCompact, false, preCompactHooks("", `cat > `+payloadPath)...)
	err, summarized, warnings := r.summarize(t, preCompactTriggerAuto)
	require.NoError(t, err)
	require.Equal(t, 1, summarized)
	require.Empty(t, warnings)

	data, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "PostCompact", payload["hook_event_name"])
	require.Equal(t, r.sessionID, payload["session_id"])
	require.Equal(t, "auto", payload["trigger"])
	require.Equal(t, "summary", payload["compact_summary"], "the fake model's summary")
}

// A PostCompact hook has no say over a summary that is done: one that
// could not evaluate is a warning to the user. A sub-agent's summary
// fires no PostCompact hooks, as it fires no PreCompact ones.
func TestSummarize_PostCompactFailedHookWarns(t *testing.T) {
	t.Parallel()

	r := compactHookAgent(t, hooks.EventPostCompact, false, preCompactHooks("", `exit 3`)...)
	err, summarized, warnings := r.summarize(t, preCompactTriggerManual)
	require.NoError(t, err)
	require.Equal(t, 1, summarized)
	require.Len(t, warnings, 1)
	require.True(t, strings.HasPrefix(warnings[0], "PostCompact hook"), warnings[0])
	require.Contains(t, warnings[0], "could not evaluate")

	dir := t.TempDir()
	ran := filepath.Join(dir, "ran")
	sub := compactHookAgent(t, hooks.EventPostCompact, true, preCompactHooks("", `touch `+ran)...)
	err, summarized, _ = sub.summarize(t, preCompactTriggerAuto)
	require.NoError(t, err)
	require.Equal(t, 1, summarized)
	require.NoFileExists(t, ran)
}

// After a summary the session is given the project's mem again, as a new
// session is, with the rest of what it was working with; and mem is told
// first, so that what recall gave the session may be given again.
func TestSummarize_GivesMemAgain(t *testing.T) {
	t.Parallel()

	r := compactHookAgent(t, hooks.EventPostCompact, false)
	block := "<mem>\nmem: 1 item open\nopen goals, newest first:\n- `release` \"release v2\" (just now)\n</mem>"
	var calls []string
	r.agent.memSummarized = func(sessionID string) { calls = append(calls, "summarized "+sessionID) }
	r.agent.memBlock = func(sessionID string) string {
		calls = append(calls, "block "+sessionID)
		return block
	}
	err, summarized, _ := r.summarize(t, preCompactTriggerAuto)
	require.NoError(t, err)
	require.Equal(t, 1, summarized)
	require.Equal(t, []string{"summarized " + r.sessionID, "block " + r.sessionID}, calls)

	msgs, err := r.env.messages.List(t.Context(), r.sessionID)
	require.NoError(t, err)
	given := msgs[len(msgs)-1]
	require.Equal(t, message.User, given.Role)
	require.Equal(t, message.TextContent{Text: block, Hidden: true}, given.Parts[0])
}
