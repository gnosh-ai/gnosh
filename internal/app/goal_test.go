package app

import (
	"testing"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/agenttest"
	"github.com/stretchr/testify/require"
)

// goalCoordinator keeps the sessions' goals, as the coordinator does.
type goalCoordinator struct {
	agenttest.NopCoordinator
	goals map[string]agent.Goal
}

func (c *goalCoordinator) SetGoal(sessionID, condition string) {
	c.goals[sessionID] = agent.Goal{Condition: condition}
}

func (c *goalCoordinator) Goal(sessionID string) (agent.Goal, bool) {
	goal, ok := c.goals[sessionID]
	return goal, ok
}

func (c *goalCoordinator) ClearGoal(sessionID string) (agent.Goal, bool) {
	goal, ok := c.goals[sessionID]
	delete(c.goals, sessionID)
	return goal, ok
}

// /goal through the app: a goal is set, read, set anew (answering the one
// it replaced) and cleared (answering it); before the agent is ready
// there is none to read or set.
func TestSessionGoal(t *testing.T) {
	t.Parallel()

	a := &App{AgentCoordinator: &goalCoordinator{goals: map[string]agent.Goal{}}}
	_, ok, err := a.SessionGoal("s1")
	require.NoError(t, err)
	require.False(t, ok)

	_, had, err := a.SetSessionGoal("s1", "the tests pass")
	require.NoError(t, err)
	require.False(t, had)
	goal, ok, err := a.SessionGoal("s1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, agent.Goal{Condition: "the tests pass"}, goal)

	replaced, had, err := a.SetSessionGoal("s1", "the docs build")
	require.NoError(t, err)
	require.True(t, had)
	require.Equal(t, "the tests pass", replaced.Condition)
	cleared, had, err := a.SetSessionGoal("s1", "")
	require.NoError(t, err)
	require.True(t, had)
	require.Equal(t, "the docs build", cleared.Condition)
	_, ok, err = a.SessionGoal("s1")
	require.NoError(t, err)
	require.False(t, ok)

	_, _, err = (&App{}).SessionGoal("s1")
	require.EqualError(t, err, "the agent is not ready")
	_, _, err = (&App{}).SetSessionGoal("s1", "the tests pass")
	require.EqualError(t, err, "the agent is not ready")
}
