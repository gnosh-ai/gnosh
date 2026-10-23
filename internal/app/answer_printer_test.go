package app

import (
	"bytes"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func textMsg(id, text string) message.Message {
	return message.Message{ID: id, Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: text}}}
}

// Each message's text is written once as it grows, with a blank line
// before the next message's: they used to run together, "Let me
// look.There are two files.".
func TestAnswerPrinterSeparatesMessages(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	p := &answerPrinter{w: &out, readBytes: map[string]int{}}
	for _, msg := range []message.Message{
		textMsg("m0", "  "), // whitespace before anything is printed is dropped
		textMsg("m1", "  Let me"),
		textMsg("m1", "  Let me look."),
		textMsg("m2", ""), // a message that has no text yet adds nothing
		textMsg("m2", "There are"),
		textMsg("m2", "There are two files."),
		textMsg("m2", "There are two files."),
	} {
		require.NoError(t, p.print(msg))
	}
	require.Equal(t, "Let me look.\n\nThere are two files.", out.String())

	require.Error(t, p.print(textMsg("m2", "There")), "a message cannot shrink")
}
