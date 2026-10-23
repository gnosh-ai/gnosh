package dialog

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

type paletteWorkspace struct {
	workspace.Workspace
	cfg *config.Config
}

func (w *paletteWorkspace) Config() *config.Config { return w.cfg }

// Claude Code's slash command names, typed after "/" from habit, select the
// command that does the same here; "resume" and "compact" used to match
// nothing.
func TestPaletteFindsClaudeCodeSlashNames(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	com := &common.Common{
		Workspace: &paletteWorkspace{cfg: &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}},
		Styles:    &sty,
	}
	for typed, want := range map[string]string{
		"resume":  "Sessions",
		"compact": "Summarize Session",
		"clear":   "New Session",
		"new":     "New Session",
		"reset":   "New Session",
		"model":   "Switch Model",
		"init":    "Initialize Project",
		"help":    "Toggle Help",
		"theme":   "Themes",
		"exit":    "Quit",
		"quit":    "Quit",
	} {
		c, err := NewCommands(com, "s1", true, false, false, nil, nil)
		require.NoError(t, err)
		for _, r := range typed {
			c.HandleMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		item, ok := c.list.SelectedItem().(*CommandItem)
		require.True(t, ok, "%q selects nothing", typed)
		require.Equal(t, want, item.title, "typed %q", typed)
	}
}
