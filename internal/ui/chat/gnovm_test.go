package chat

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// A gnovm_run call is headed by the Gno it runs, highlighted, not by its
// JSON: Gnovm Run mem.Show().
func TestGnoVMRunToolIsHeadedByItsCode(t *testing.T) {
	t.Parallel()

	sty := styles.GnoshCosmos()
	render := func(input string) string {
		return (&GnoVMRunToolRenderContext{}).RenderTool(&sty, 120, &ToolRenderOpts{
			ToolCall: message.ToolCall{ID: "g1", Name: "gnovm_run", Input: input, Finished: true},
			Result:   &message.ToolResult{ToolCallID: "g1", Content: "1 goal open"},
			Status:   ToolStatusSuccess,
		})
	}
	firstLine := func(s string) string { return strings.SplitN(ansi.Strip(s), "\n", 2)[0] }

	out := render(`{"code":"mem.Show()","name":"mem"}`)
	require.Contains(t, firstLine(out), "Gnovm Run")
	require.Contains(t, firstLine(out), "mem.Show()")
	require.NotContains(t, ansi.Strip(out), `"code"`)
	require.Contains(t, ansi.Strip(out), "1 goal open")
	require.Contains(t, out, "\x1b[38;2;", "the code is highlighted")

	// Another VM leads the line; gas and reset follow the code.
	line := firstLine(render(`{"code":"x := 1 + 1","name":"calc","gas":2e9,"reset":true}`))
	require.Contains(t, line, "calc x := 1 + 1")
	require.Contains(t, line, "gas=2e9")
	require.Contains(t, line, "reset=true")

	// The session's scratch VM, which no name means, is left out of it.
	require.Contains(t, firstLine(render(`{"code":"println(2+2)"}`)), "Gnovm Run println(2+2)")

	// Input that does not parse is shown as any tool's is.
	require.Contains(t, ansi.Strip(render(`{"code":`)), "Invalid parameters")
}

// While a gnovm_run call streams, its code so far heads it, as it is
// written; before the code begins, the spinner alone.
func TestGnoVMRunToolShowsItsCodeWhileItStreams(t *testing.T) {
	t.Parallel()

	sty := styles.GnoshCosmos()
	render := func(input string) string {
		return ansi.Strip((&GnoVMRunToolRenderContext{}).RenderTool(&sty, 120, &ToolRenderOpts{
			ToolCall: message.ToolCall{ID: "g1", Name: "gnovm_run", Input: input},
			Status:   ToolStatusRunning,
		}))
	}

	out := render(`{"code":"x := 17 * 23\nprintln(`)
	require.Contains(t, out, "Gnovm Run x := 17 * 23")
	require.Contains(t, out, "println(")

	require.Equal(t, ansi.Strip(pendingTool(&sty, "Gnovm Run", nil, false)), render(`{"co`))
	require.Equal(t, ansi.Strip(pendingTool(&sty, "Gnovm Run", nil, false)), render(``))
}
