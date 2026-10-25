package model

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/ui/util"
)

// goalSetMsg is a goal set for the session, which the agent is to start
// working toward.
type goalSetMsg struct {
	condition string
}

// goalClearWords clear a goal when given to /goal, as in Claude Code.
var goalClearWords = []string{"clear", "off", "reset", "none", "cancel"}

// goal sets, clears or shows the session's goal, as Claude Code's /goal
// does. A condition is set as the goal (a session is made for it if there
// is none yet), and a hidden prompt has the agent start working toward it;
// a clearing word clears it; nothing says what is set and what the last
// check found.
func (m *UI) goal(arg string) tea.Cmd {
	arg = strings.TrimSpace(arg)
	if arg == "" || slices.Contains(goalClearWords, strings.ToLower(arg)) {
		if !m.hasSession() {
			return util.ReportInfo("No goal set. Usage: /goal <condition>")
		}
		sessionID, clear := m.session.ID, arg != ""
		return func() tea.Msg {
			var (
				goal agent.Goal
				ok   bool
				err  error
			)
			if clear {
				goal, ok, err = m.com.Workspace.SetSessionGoal(context.Background(), sessionID, "")
			} else {
				goal, ok, err = m.com.Workspace.SessionGoal(context.Background(), sessionID)
			}
			switch {
			case err != nil:
				return util.ReportError(fmt.Errorf("failed to read the goal: %w", err))()
			case !ok:
				return util.NewInfoMsg("No goal set. Usage: /goal <condition>")
			case clear:
				return util.NewInfoMsg("Goal cleared: " + goal.Condition)
			}
			return util.NewInfoMsg(fmt.Sprintf("Goal active: %s. Last check: %s", goal.Condition, cmp.Or(goal.LastCheck, "not yet evaluated")))
		}
	}
	if utf8.RuneCountInString(arg) > agent.GoalConditionLimit {
		return util.ReportWarn(fmt.Sprintf("Goal condition is limited to %d characters", agent.GoalConditionLimit))
	}
	loadCmd, err := m.ensureSession()
	if err != nil {
		return util.ReportError(err)
	}
	sessionID := m.session.ID
	return tea.Batch(loadCmd, func() tea.Msg {
		if _, _, err := m.com.Workspace.SetSessionGoal(context.Background(), sessionID, arg); err != nil {
			return util.ReportError(fmt.Errorf("failed to set the goal: %w", err))()
		}
		return goalSetMsg{condition: arg}
	})
}

// goalSet says the goal is set and starts the agent working toward it,
// with the directive Claude Code sends, hidden; with the agent busy, the
// directive waits in the queue.
func (m *UI) goalSet(msg goalSetMsg) tea.Cmd {
	return tea.Batch(util.ReportInfo("Goal set: "+msg.condition), m.sendMessageInternal(agent.GoalDirective(msg.condition), true))
}
