package agent

import (
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

// A summary that leaves the conversation still over the point that set it
// off is not followed by another, and another, on every step: the session
// first drops the point CLAUDE_AUTOCOMPACT_PCT_OVERRIDE gave for the
// default, and if it is over that too, stops compacting. On the AI box a
// stand-in reporting 60k tokens after each summary had Gnosh summarize
// 1,562 times in two minutes with the percentage at 50.
func TestCompactionDoesNotLoop(t *testing.T) {
	for name, tc := range map[string]struct {
		percent string
		usage   int64
		level   int
	}{
		"over the default point":   {"", 190_000, compactNever},
		"over the percentage only": {"50", 120_000, compactAtDefault},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
			t.Setenv("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE", tc.percent)
			llm := &requestRecordingModel{
				replies: []string{"call probe", "the summary", "call probe", "done"},
				usage:   fantasy.Usage{InputTokens: tc.usage, OutputTokens: 10},
			}
			a, sessionID, runs := summaryRequestAgent(t, llm)

			_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "go on"})
			require.NoError(t, err)

			summaries := 0
			for _, req := range llm.requests() {
				if req.ToolChoice != nil && *req.ToolChoice == fantasy.ToolChoiceNone {
					summaries++
				}
			}
			require.Equal(t, 1, summaries, "one summary, not one per step")
			require.Len(t, llm.requests(), 4, "a step, the summary, and the continued turn's two steps")
			require.EqualValues(t, 2, runs.Load())
			level, _ := a.compactLevels.Get(sessionID)
			require.Equal(t, tc.level, level)
		})
	}

	// A summary asked for by hand starts auto-compaction afresh.
	llm := &requestRecordingModel{replies: []string{"the summary"}}
	a, sessionID, _ := summaryRequestAgent(t, llm)
	a.compactLevels.Set(sessionID, compactNever)
	require.NoError(t, a.Summarize(t.Context(), sessionID, "", fantasy.ProviderOptions{}, nil, preCompactTriggerManual))
	_, set := a.compactLevels.Get(sessionID)
	require.False(t, set)
}
