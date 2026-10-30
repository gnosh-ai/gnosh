package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/charmbracelet/crush/internal/agent"
)

// errAgentNotReady is the answer about a goal before the agent is.
var errAgentNotReady = errors.New("the agent is not ready")

// SessionGoal is the session's goal (see agent.Goal), and false when it
// has none.
func (a *App) SessionGoal(ctx context.Context, sessionID string) (agent.Goal, bool, error) {
	if a.AgentCoordinator == nil {
		return agent.Goal{}, false, errAgentNotReady
	}
	goal, ok := a.AgentCoordinator.Goal(ctx, sessionID)
	return goal, ok, nil
}

// GoalOutcome says on w how a run's --goal ended (met, from the run's
// notifications, or else the goal g as it stands, set says whether it
// still is), and is the run's error unless the goal was met: a run exits
// non-zero while its goal is still set.
func GoalOutcome(w io.Writer, condition string, met bool, g agent.Goal, set bool, err error) error {
	switch {
	case err != nil:
		return fmt.Errorf("the goal could not be read: %w", err)
	case met && !set:
		_, _ = fmt.Fprintf(w, "goal met: %s\n", condition)
		return nil
	case set:
		return fmt.Errorf("the goal is still set: %s (`%s` in mem); last check: %s", condition, g.Name, cmp.Or(g.LastCheck, "none yet"))
	}
	return fmt.Errorf("the goal was cleared without being met: %s", condition)
}

// SetSessionGoal sets the session's goal, binding it to a goal in mem, as
// /goal does, or clears it when condition is "", and says what it did:
// the directive to send the agent, or what clearing it did.
func (a *App) SetSessionGoal(ctx context.Context, sessionID, condition string) (agent.GoalChange, error) {
	if a.AgentCoordinator == nil {
		return agent.GoalChange{}, errAgentNotReady
	}
	if condition == "" {
		return a.AgentCoordinator.ClearGoal(ctx, sessionID)
	}
	return a.AgentCoordinator.SetGoal(ctx, sessionID, condition)
}
