package message

import (
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/google"
	"github.com/stretchr/testify/require"
)

// The agent's OnReasoningEnd stores the provider's signature and then calls
// FinishThinking. The signature has to survive that call, or the next
// request rebuilt from the stored message goes without it.
func TestFinishThinking_KeepsGoogleThoughtSignature(t *testing.T) {
	t.Parallel()

	msg := Message{Role: Assistant}
	msg.AppendReasoningContent("let me look at the file")
	msg.AppendThoughtSignature("c2lnbmF0dXJl", "call_1")
	msg.FinishThinking()
	msg.AddToolCall(ToolCall{ID: "call_1", Name: "view", Input: `{"file_path":"main.go"}`, Finished: true})

	reasoning := msg.ReasoningContent()
	require.NotZero(t, reasoning.FinishedAt)
	require.Equal(t, "c2lnbmF0dXJl", reasoning.ThoughtSignature)
	require.Equal(t, "call_1", reasoning.ToolID)

	ai := msg.ToAIMessage()
	require.Len(t, ai, 1)
	var found bool
	for _, part := range ai[0].Content {
		rp, ok := fantasy.AsMessagePart[fantasy.ReasoningPart](part)
		if !ok {
			continue
		}
		meta, ok := rp.ProviderOptions[google.Name].(*google.ReasoningMetadata)
		require.True(t, ok, "reasoning part carries no google metadata")
		require.Equal(t, "c2lnbmF0dXJl", meta.Signature)
		require.Equal(t, "call_1", meta.ToolID)
		found = true
	}
	require.True(t, found, "no reasoning part in the rebuilt message")
}
