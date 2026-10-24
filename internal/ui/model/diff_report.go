package model

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/crush/internal/diff"
	"github.com/charmbracelet/crush/internal/ui/dialog"
)

// diffLinesPerFile is how much of one file's diff /diff shows; the rest
// is counted.
const diffLinesPerFile = 400

// openDiffReport shows what the session changed, as Claude Code's /diff
// shows changes.
func (m *UI) openDiffReport() {
	m.dialog.OpenDialog(dialog.NewReport(m.com, "Diff", m.diffReport()))
}

// diffReport is what the session changed, as Markdown for /diff: each file
// the sidebar counts under Modified Files, newest change first, with its
// lines added and removed and the diff from its first version in the
// session to its latest. Where Claude Code's /diff shows git's uncommitted
// changes, this shows the session's own.
func (m *UI) diffReport() string {
	var sb strings.Builder
	sb.WriteString("## Diff\n\n")
	cwd := m.com.Workspace.WorkingDir()
	changed := 0
	for _, f := range m.sessionFiles {
		if f.Additions == 0 && f.Deletions == 0 {
			continue
		}
		changed++
		path := f.LatestVersion.Path
		if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
			path = rel
		}
		unified, _, _ := diff.GenerateDiff(f.FirstVersion.Content, f.LatestVersion.Content, path)
		lines := strings.Split(strings.TrimRight(unified, "\n"), "\n")
		more := len(lines) - diffLinesPerFile
		if more > 0 {
			lines = lines[:diffLinesPerFile]
		}
		body := strings.Join(lines, "\n")
		// A fence longer than any run of backticks in the diff, which a
		// Markdown file's own fences would otherwise close.
		fence := "```"
		for strings.Contains(body, fence) {
			fence += "`"
		}
		fmt.Fprintf(&sb, "### `%s` (+%d -%d)\n\n%sdiff\n%s\n%s\n\n", path, f.Additions, f.Deletions, fence, body, fence)
		if more > 0 {
			fmt.Fprintf(&sb, "… %d more lines of this diff\n\n", more)
		}
	}
	if changed == 0 {
		sb.WriteString("No files changed in this session.\n")
	}
	return sb.String()
}
