package chat

import (
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// A hidden notice, a background job's end, is one subdued line in the
// chat, under its message's ID; other hidden text is left out, as before.
func TestExtractMessageItemsShowsANotice(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	text := `Background command "render" (job 001) completed (exit code 0). Call job_output with shell_id 001 for its output.`
	items := ExtractMessageItems(&sty, &message.Message{ID: "n1", Role: message.User, Parts: []message.ContentPart{
		message.TextContent{Text: text, Hidden: true, Notice: true},
	}}, nil, "")
	require.Len(t, items, 1)
	require.Equal(t, "n1", items[0].ID())
	shown := ansi.Strip(items[0].Render(80))
	require.Contains(t, shown, `⎿ Background command "render" (job 001) completed`)
	require.Contains(t, shown, "for its output.")
	require.Equal(t, shown, ansi.Strip(items[0].Render(80)), "the same each time")

	require.Empty(t, ExtractMessageItems(&sty, &message.Message{ID: "h1", Role: message.User, Parts: []message.ContentPart{
		message.TextContent{Text: "Continue.", Hidden: true},
	}}, nil, ""))
}
