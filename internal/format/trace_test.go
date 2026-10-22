package format

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolTrace(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	trace := NewToolTrace(&out)

	trace.Call("c1", "bash", "go test ./...")
	trace.Call("c1", "bash", "go test ./...") // republished: printed once
	trace.Call("c2", "view", "main.go")
	trace.Call("", "ls", "") // no ID yet: nothing to key it by
	trace.Result("c2", false)
	trace.Result("c1", true)
	trace.Result("c1", true) // republished: marked once
	trace.Result("c9", true) // never traced: nothing to mark

	require.Equal(t, "tool bash go test ./...\n"+
		"tool view main.go\n"+
		"tool bash go test ./... (failed)\n", out.String(),
		"one line per call, and a second only for a failure")

	var none *ToolTrace
	none.Call("c1", "bash", "x") // a nil trace is off, not a panic
	none.Result("c1", true)
}
