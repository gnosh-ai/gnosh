package app

import (
	"errors"

	"github.com/charmbracelet/crush/internal/agent"
)

// errAgentNotReady is the answer about a goal before the agent is.
var errAgentNotReady = errors.New("the agent is not ready")

// SessionGoal is the session's goal (see agent.Goal), and false when it
// has none.
func (a *App) SessionGoal(sessionID string) (agent.Goal, bool, error) {
	if a.AgentCoordinator == nil {
		return agent.Goal{}, false, errAgentNotReady
	}
	goal, ok := a.AgentCoordinator.Goal(sessionID)
	return goal, ok, nil
}

// SetSessionGoal sets the session's goal, as Claude Code's /goal does, or
// clears it when condition is "", and returns the goal it had.
func (a *App) SetSessionGoal(sessionID, condition string) (agent.Goal, bool, error) {
	if a.AgentCoordinator == nil {
		return agent.Goal{}, false, errAgentNotReady
	}
	had, ok := a.AgentCoordinator.ClearGoal(sessionID)
	if condition != "" {
		a.AgentCoordinator.SetGoal(sessionID, condition)
	}
	return had, ok, nil
}
