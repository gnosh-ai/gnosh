package model

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/stretchr/testify/require"
)

// sessionFile is a file the session changed from before to after, counted
// as the sidebar counts it.
func sessionFile(path, before, after string, additions, deletions int) SessionFile {
	return SessionFile{
		FirstVersion:  history.File{Path: path, Content: before},
		LatestVersion: history.File{Path: path, Content: after},
		Additions:     additions,
		Deletions:     deletions,
	}
}

// /diff, typed or chosen in the palette, shows each file the session changed, in the sidebar's order, by
// its path in the project (a file outside it by its own), with its lines
// added and removed and the diff from its first version to its latest; a
// file the session left as it was is not shown.
func TestDiffReport(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	u.com.Workspace = &exportWorkspace{dir: "/home/jae/project"}
	u.session = &session.Session{ID: "s1"}
	u.sessionFiles = []SessionFile{
		sessionFile("/home/jae/project/internal/a.go", "one\ntwo\n", "one\n2\n", 1, 1),
		sessionFile("/home/jae/project/same.go", "x\n", "x\n", 0, 0),
		sessionFile("/tmp/notes_v2.md", "", "new\n", 1, 0),
	}

	report := u.diffReport()
	require.True(t, strings.HasPrefix(report, "## Diff\n\n### `internal/a.go` (+1 -1)\n\n```diff\n--- a/internal/a.go\n+++ b/internal/a.go\n"), report)
	require.Contains(t, report, "\n-two\n+2\n```\n\n")
	require.Contains(t, report, "### `/tmp/notes_v2.md` (+1 -0)\n\n```diff\n")
	require.Contains(t, report, "\n+new\n```\n")
	require.NotContains(t, report, "same.go")
	require.Less(t, strings.Index(report, "internal/a.go"), strings.Index(report, "notes_v2.md"))

	u.openDiffReport()
	require.True(t, u.dialog.ContainsDialog(dialog.ReportID))
	u.dialog.CloseDialog(dialog.ReportID)
	require.Nil(t, u.sendMessage("/diff"))
	require.True(t, u.dialog.ContainsDialog(dialog.ReportID))
	u.dialog.CloseDialog(dialog.ReportID)
	c, err := dialog.NewCommands(u.com, "s1", true, false, false, nil, nil)
	require.NoError(t, err)
	u.dialog.OpenDialog(c)
	for _, r := range "diff" {
		u.handleDialogMsg(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	u.handleDialogMsg(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.False(t, u.dialog.ContainsDialog(dialog.CommandsID))
	require.True(t, u.dialog.ContainsDialog(dialog.ReportID))

	u.sessionFiles = u.sessionFiles[1:2]
	require.Equal(t, "## Diff\n\nNo files changed in this session.\n", u.diffReport())
}

// A long diff shows its first 400 lines and counts the rest; a file whose
// own text has a fence gets a longer one, so the diff stays one block.
func TestDiffReportCutsAndFences(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.com.Workspace = &exportWorkspace{dir: "/p"}
	var after strings.Builder
	for i := range 1000 {
		fmt.Fprintf(&after, "line %d\n", i)
	}
	u.sessionFiles = []SessionFile{sessionFile("/p/big.txt", "", after.String(), 1000, 0)}
	report := u.diffReport()
	// 400 of the diff's 1,003 lines: its three header lines and 397 added.
	require.Contains(t, report, "+line 396\n```\n\n… 603 more lines of this diff\n")
	require.NotContains(t, report, "+line 397\n")

	u.sessionFiles = []SessionFile{sessionFile("/p/README.md", "a\n", "a\n```go\nx\n```\n", 3, 0)}
	report = u.diffReport()
	require.Contains(t, report, "````diff\n")
	require.True(t, strings.HasSuffix(report, "+```\n````\n\n"), report)
}
