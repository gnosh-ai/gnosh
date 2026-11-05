package model

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/stretchr/testify/require"
)

func TestMCPList_ChannelBadge(t *testing.T) {
	t.Parallel()
	styles := common.DefaultCommon(nil).Styles

	channelSrv := []mcp.ClientInfo{
		{Name: "webhook", State: mcp.StateConnected, Channel: true},
	}
	if out := mcpList(styles, channelSrv, 80, 10); !strings.Contains(out, "channel") {
		t.Errorf("expected a channel badge for an active channel server, got:\n%s", out)
	}

	plainSrv := []mcp.ClientInfo{
		{Name: "webhook", State: mcp.StateConnected, Channel: false},
	}
	if out := mcpList(styles, plainSrv, 80, 10); strings.Contains(out, "channel") {
		t.Errorf("non-channel server must not show a channel badge, got:\n%s", out)
	}
}

// mcpWorkspace has the MCPs github and filesystem configured.
type mcpWorkspace struct{ *exportWorkspace }

func (mcpWorkspace) Config() *config.Config {
	return &config.Config{MCP: config.MCPs{"github": {}, "filesystem": {}}, Options: &config.Options{TUI: &config.TUIOptions{}}}
}

// An MCP with more capabilities than the sidebar has room for keeps its
// name, on one row.
func TestSidebar_AnMCPKeepsItsNameBesideCountsTooWideForIt(t *testing.T) {
	t.Parallel()
	u, _ := sidebarUI(t, 60)
	u.com.Workspace = mcpWorkspace{u.com.Workspace.(*exportWorkspace)}
	counts := mcp.Counts{Tools: 41, Prompts: 2, Resources: 12}
	u.mcpStates = map[string]mcp.ClientInfo{
		"github":     {Name: "github", State: mcp.StateConnected, Counts: counts},
		"filesystem": {Name: "filesystem", State: mcp.StateConnected, Counts: counts},
	}

	scr := drawSidebarOf(u)
	fs := rowShowing(u, scr, "● filesystem 41 tools")
	require.GreaterOrEqual(t, fs, 0, "the name, then as many counts as fit")
	require.Equal(t, fs+1, rowShowing(u, scr, "● github 41 tools"), "the next MCP on the next row")
}
