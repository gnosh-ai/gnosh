package dialog

import (
	"fmt"
	"image"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// The report dialog, which /context and /hooks open, shows its report
// rendered as the chat renders Markdown, scrolls a line or a page at a
// time, never past its end, and closes on Escape.
func TestReportDialog(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	var report strings.Builder
	report.WriteString("## Context Usage\n\n| Category | Tokens | Percentage |\n|----------|--------|------------|\n| Free space | 962.2k | 96.2% |\n\n")
	for i := range 60 {
		fmt.Fprintf(&report, "line %d\n\n", i)
	}
	d := NewReport(&common.Common{Styles: &sty}, "Context Usage", report.String())
	rendered := ansi.Strip(d.rendered(80))
	require.Contains(t, rendered, "Free space")
	require.NotContains(t, rendered, "|----------|", "the table is drawn, not shown as Markdown")

	scr := uv.NewScreenBuffer(100, 20)
	draw := func() { d.Draw(scr, image.Rect(0, 0, 100, 20)) }
	draw()
	require.Zero(t, d.offset)
	require.Greater(t, d.lines, 1)
	require.Less(t, d.lines, 20)

	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyDown})
	require.Equal(t, 1, d.offset)
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyPgDown})
	require.Equal(t, d.lines, d.offset)
	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
	require.Equal(t, d.lines-1, d.offset)
	d.HandleMsg(common.CoalescedWheelMsg{DeltaY: 2})
	require.Equal(t, d.lines+1, d.offset)

	for range 50 {
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	draw()
	total := len(strings.Split(d.rendered(100-sty.Dialog.View.GetHorizontalFrameSize()-1), "\n"))
	require.Equal(t, total-d.lines, d.offset, "the last page ends at the report's end")

	d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyPgUp})
	require.Equal(t, total-d.lines-(d.lines-1), d.offset)
	for range 500 {
		d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	require.Zero(t, d.offset, "never above the first line")
	require.Equal(t, ActionClose{}, d.HandleMsg(tea.KeyPressMsg{Code: tea.KeyEscape}))
}

// A line break made with two trailing spaces, as /context writes its
// header after Claude Code's, keeps its lines apart in the dialog.
func TestReportKeepsHardBreaks(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	d := NewReport(&common.Common{Styles: &sty}, "Context Usage", "## Context Usage\n\n**Model:** Qwen  \n**Tokens:** 7.1k / 1M (1%)\n")
	var model string
	for line := range strings.SplitSeq(ansi.Strip(d.rendered(80)), "\n") {
		if strings.Contains(line, "Model:") {
			model = line
		}
	}
	require.Contains(t, model, "Model: Qwen")
	require.NotContains(t, model, "Tokens", "the two lines ran together")
}
