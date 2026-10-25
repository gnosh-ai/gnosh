package agent

import (
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// A turn's reply may run to CLAUDE_CODE_MAX_OUTPUT_TOKENS, as in Claude
// Code, when it is a positive number; else to the selected model's
// max_tokens, else to its catalog default.
func TestRunMaxTokensFollowsClaudeCodesVariable(t *testing.T) {
	model := Model{CatwalkCfg: catwalk.Model{DefaultMaxTokens: 4096}}
	selected := Model{CatwalkCfg: catwalk.Model{DefaultMaxTokens: 4096}, ModelCfg: config.SelectedModel{MaxTokens: 16000}}

	t.Setenv("CLAUDE_CODE_MAX_OUTPUT_TOKENS", "")
	require.Equal(t, int64(4096), runMaxTokens(model), "the catalog default")
	require.Equal(t, int64(16000), runMaxTokens(selected), "the selected model's max_tokens")

	t.Setenv("CLAUDE_CODE_MAX_OUTPUT_TOKENS", "32000")
	require.Equal(t, int64(32000), runMaxTokens(model))
	require.Equal(t, int64(32000), runMaxTokens(selected), "the variable wins, as in Claude Code")

	for _, value := range []string{"0", "-5", "lots"} {
		t.Setenv("CLAUDE_CODE_MAX_OUTPUT_TOKENS", value)
		require.Equal(t, int64(16000), runMaxTokens(selected), "%q is ignored", value)
	}
}
