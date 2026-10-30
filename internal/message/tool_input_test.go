package message

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A tool call's input grows as the model streams it, and only while the
// call is unfinished.
func TestAppendToolCallInput(t *testing.T) {
	t.Parallel()

	m := &Message{}
	m.AddToolCall(ToolCall{ID: "c1", Name: "gnovm_run"})
	m.AppendToolCallInput("c1", `{"code":"x`)
	m.AppendToolCallInput("c1", ` := 1"}`)
	m.AppendToolCallInput("other", `ignored`)
	require.Equal(t, `{"code":"x := 1"}`, m.ToolCalls()[0].Input)

	m.AddToolCall(ToolCall{ID: "c1", Name: "gnovm_run", Input: `{"code":"x := 2"}`, Finished: true})
	m.AppendToolCallInput("c1", `late`)
	require.Equal(t, `{"code":"x := 2"}`, m.ToolCalls()[0].Input, "a finished call keeps its input")
}
