package model

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/ui/util"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// goalWorkspace keeps the sessions' goals, as the agent does, makes a
// session, loads none, and has an agent that is not ready, so a prompt
// sent comes back as not sent, with what it was.
type goalWorkspace struct {
	workspace.Workspace
	goals map[string]agent.Goal
}

func (w *goalWorkspace) SessionGoal(_ context.Context, sessionID string) (agent.Goal, bool, error) {
	goal, ok := w.goals[sessionID]
	return goal, ok, nil
}

func (w *goalWorkspace) SetSessionGoal(_ context.Context, sessionID, condition string) (agent.Goal, bool, error) {
	had, ok := w.goals[sessionID]
	delete(w.goals, sessionID)
	if condition != "" {
		w.goals[sessionID] = agent.Goal{Condition: condition}
	}
	return had, ok, nil
}

func (w *goalWorkspace) CreateSession(context.Context, string) (session.Session, error) {
	return session.Session{ID: "new"}, nil
}

func (w *goalWorkspace) GetSession(context.Context, string) (session.Session, error) {
	return session.Session{}, errors.New("not loaded in this test")
}

func (w *goalWorkspace) SetCurrentSession(context.Context, string) error { return nil }

func (w *goalWorkspace) AgentReadyErr() error { return errors.New("not ready") }

func (w *goalWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{TUI: &config.TUIOptions{}}}
}

// /goal, as in Claude Code: with a condition it is set (a session is made
// for it if there is none yet), said so, and the agent sent, hidden, the
// directive to work toward it; alone it says what is set and what the
// last check found; clear clears it. With none set, it says so and how
// to set one; a condition over the limit is refused; and a goal found met
// is said so.
func TestGoalCommand(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	ws := &goalWorkspace{goals: map[string]agent.Goal{}}
	u.com.Workspace = ws
	none := util.NewInfoMsg("No goal set. Usage: /goal <condition>")

	require.Equal(t, none, u.sendMessage("/goal")())
	require.Equal(t, none, u.sendMessage("/goal clear")())

	var set goalSetMsg
	for _, msg := range msgsOf(u.sendMessage("/goal the tests pass")) {
		if m, ok := msg.(goalSetMsg); ok {
			set = m
		}
	}
	require.Equal(t, goalSetMsg{condition: "the tests pass"}, set)
	require.Equal(t, "new", u.session.ID, "a session made for it")
	require.Equal(t, map[string]agent.Goal{"new": {Condition: "the tests pass"}}, ws.goals)

	var said []util.InfoMsg
	var directive agentRunSubmittedMsg
	for _, msg := range msgsOf(u.goalSet(set)) {
		switch m := msg.(type) {
		case util.InfoMsg:
			said = append(said, m)
		case agentRunSubmittedMsg:
			directive = m
		}
	}
	require.Equal(t, []util.InfoMsg{util.NewInfoMsg("Goal set: the tests pass")}, said)
	require.Equal(t, agent.GoalDirective("the tests pass"), directive.prompt)
	require.True(t, directive.hidden)

	require.Equal(t, util.NewInfoMsg("Goal active: the tests pass. Last check: not yet evaluated"), u.sendMessage("/goal")())
	ws.goals["new"] = agent.Goal{Condition: "the tests pass", Checks: 1, LastCheck: "the tests still fail"}
	require.Equal(t, util.NewInfoMsg("Goal active: the tests pass. Last check: the tests still fail"), u.handleDialogAction(dialog.ActionGoal{})())
	require.Equal(t, util.NewInfoMsg("Goal cleared: the tests pass"), u.sendMessage("/goal Off")())
	require.Empty(t, ws.goals)
	require.Equal(t, none, u.sendMessage("/goal")())

	require.Equal(t, util.NewWarnMsg("Goal condition is limited to 2000 characters"), u.sendMessage("/goal "+strings.Repeat("x", agent.GoalConditionLimit+1))())

	require.Equal(t, util.NewInfoMsg("Goal met: the tests pass (the test run passes)"),
		u.handleAgentNotification(notify.Notification{SessionID: "new", Type: notify.TypeGoalMet, Message: "Goal met: the tests pass (the test run passes)"})())
}

// msgsOf runs cmd's tree and returns the messages it ends in.
func msgsOf(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, msgsOf(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

// A goal set is said so, through Update, and the agent sent the directive.
func TestGoalSetThroughUpdate(t *testing.T) {
	pinTTLs(t)
	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	u.com.Workspace = &goalWorkspace{goals: map[string]agent.Goal{}}
	warmCaches(u, false)
	u.session = &session.Session{ID: "s1"}

	_, cmd := u.Update(goalSetMsg{condition: "the tests pass"})
	var said []string
	var directive agentRunSubmittedMsg
	for _, msg := range msgsOf(cmd) {
		switch m := msg.(type) {
		case util.InfoMsg:
			said = append(said, m.Msg)
		case agentRunSubmittedMsg:
			directive = m
		}
	}
	require.Contains(t, said, "Goal set: the tests pass")
	require.Equal(t, agent.GoalDirective("the tests pass"), directive.prompt)
}
