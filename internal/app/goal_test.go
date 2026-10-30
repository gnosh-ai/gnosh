package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/agenttest"
	"github.com/stretchr/testify/require"
)

// goalCoordinator keeps the sessions' goals, as the coordinator does in
// their rows, naming each after its condition, as mem would.
type goalCoordinator struct {
	agenttest.NopCoordinator
	goals map[string]agent.Goal
}

func (c *goalCoordinator) SetGoal(_ context.Context, sessionID, condition string) (agent.GoalChange, error) {
	goal := agent.Goal{Condition: condition, Name: strings.ReplaceAll(condition, " ", "-")}
	c.goals[sessionID] = goal
	return agent.GoalChange{Goal: goal, Had: true, Directive: "work toward " + goal.Name}, nil
}

func (c *goalCoordinator) Goal(_ context.Context, sessionID string) (agent.Goal, bool) {
	goal, ok := c.goals[sessionID]
	return goal, ok
}

func (c *goalCoordinator) ClearGoal(_ context.Context, sessionID string) (agent.GoalChange, error) {
	goal, ok := c.goals[sessionID]
	delete(c.goals, sessionID)
	if !ok {
		return agent.GoalChange{}, nil
	}
	return agent.GoalChange{Goal: goal, Had: true, Notice: "/goal is cleared"}, nil
}

// /goal through the app: a goal is set, with the directive to send, read,
// set anew and cleared, saying what clearing did; before the agent is
// ready there is none to read or set.
func TestSessionGoal(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	a := &App{AgentCoordinator: &goalCoordinator{goals: map[string]agent.Goal{}}}
	_, ok, err := a.SessionGoal(ctx, "s1")
	require.NoError(t, err)
	require.False(t, ok)

	change, err := a.SetSessionGoal(ctx, "s1", "the tests pass")
	require.NoError(t, err)
	require.Equal(t, "work toward the-tests-pass", change.Directive)
	goal, ok, err := a.SessionGoal(ctx, "s1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, agent.Goal{Condition: "the tests pass", Name: "the-tests-pass"}, goal)

	_, err = a.SetSessionGoal(ctx, "s1", "the docs build")
	require.NoError(t, err)
	cleared, err := a.SetSessionGoal(ctx, "s1", "")
	require.NoError(t, err)
	require.True(t, cleared.Had)
	require.Equal(t, "the docs build", cleared.Goal.Condition)
	require.Equal(t, "/goal is cleared", cleared.Notice)
	_, ok, err = a.SessionGoal(ctx, "s1")
	require.NoError(t, err)
	require.False(t, ok)

	_, _, err = (&App{}).SessionGoal(ctx, "s1")
	require.EqualError(t, err, "the agent is not ready")
	_, err = (&App{}).SetSessionGoal(ctx, "s1", "the tests pass")
	require.EqualError(t, err, "the agent is not ready")
}

// A run's --goal ends: met, said so on stderr; still set, or cleared
// without being met, the run fails, saying how it stands.
func TestGoalOutcome(t *testing.T) {
	t.Parallel()
	var w bytes.Buffer
	require.NoError(t, GoalOutcome(&w, "the tests pass", true, agent.Goal{}, false, nil))
	require.Equal(t, "goal met: the tests pass\n", w.String())
	require.EqualError(t, GoalOutcome(&w, "the tests pass", false, agent.Goal{Name: "tests-pass", LastCheck: "the tests still fail"}, true, nil),
		"the goal is still set: the tests pass (`tests-pass` in mem); last check: the tests still fail")
	require.EqualError(t, GoalOutcome(&w, "the tests pass", false, agent.Goal{}, false, nil), "the goal was cleared without being met: the tests pass")
	require.ErrorContains(t, GoalOutcome(&w, "the tests pass", false, agent.Goal{}, false, errors.New("no db")), "the goal could not be read: no db")
}
