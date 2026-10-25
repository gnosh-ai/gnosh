package proto

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Streaming text into a part keeps what the part is: hidden, a notice.
func TestAppendContentKeepsHiddenAndNotice(t *testing.T) {
	t.Parallel()

	msg := Message{Parts: []ContentPart{TextContent{Text: "Background", Hidden: true, Notice: true}}}
	msg.AppendContent(" command")
	require.Equal(t, []ContentPart{TextContent{Text: "Background command", Hidden: true, Notice: true}}, msg.Parts)
}
