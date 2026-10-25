package model

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/version"
)

// branchedMsg is a branch made of session from, to go on in; named when
// it was given its name.
type branchedMsg struct {
	branch, from session.Session
	named        bool
}

// said is what Claude Code says of a branch it has made, with the
// branch's name when it was given one and the original's title when it
// has one.
func (msg branchedMsg) said() string {
	name, title := "", ""
	if msg.named {
		name = fmt.Sprintf(` "%s"`, msg.branch.Title)
	}
	if msg.from.Title != "" {
		title = fmt.Sprintf(` ("%s")`, msg.from.Title)
	}
	return fmt.Sprintf("Branched conversation%s. You are now in the new branch (session %s). Use /resume %s%s to return to the original, or run `%s -r %s` in a new terminal.",
		name, msg.branch.ID, msg.from.ID, title, version.Command, msg.from.ID)
}

// branchSession copies the session's conversation into a new session and
// goes on there, as Claude Code's /branch does: named name (as /rename
// keeps a name), or after the session. The session is left as it was.
func (m *UI) branchSession(name string) tea.Cmd {
	if !m.hasSession() {
		return util.ReportWarn("No conversation to branch")
	}
	if m.isAgentBusy() {
		return util.ReportWarn("Agent is busy, please wait before branching the conversation...")
	}
	return m.branch(*m.session, session.CleanName(name))
}

// branch copies the conversation of session from into a new session to go
// on in, titled title, or after from with no title.
func (m *UI) branch(from session.Session, title string) tea.Cmd {
	return func() tea.Msg {
		branch, err := m.com.Workspace.BranchSession(context.Background(), from.ID, title)
		if err != nil {
			return util.ReportError(fmt.Errorf("failed to branch conversation: %w", err))()
		}
		return branchedMsg{branch: branch, from: from, named: title != ""}
	}
}

// forkSession goes on in a copy of session id, for --fork-session with
// the session named on the command line.
func (m *UI) forkSession(id string) tea.Cmd {
	return func() tea.Msg {
		from, err := m.com.Workspace.GetSession(context.Background(), id)
		if err != nil {
			return util.ReportError(err)()
		}
		return m.branch(from, "")()
	}
}
