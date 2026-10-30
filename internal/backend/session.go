package backend

import (
	"context"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/app"

	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/charmbracelet/crush/internal/session"
)

// CreateSession creates a new session in the given workspace.
func (b *Backend) CreateSession(ctx context.Context, workspaceID, title string) (session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return session.Session{}, err
	}

	return ws.Sessions.Create(ctx, title)
}

// BranchSession copies a session's conversation into a new session, as
// Claude Code's /branch does, titled title or after the session.
func (b *Backend) BranchSession(ctx context.Context, workspaceID, sessionID, title string) (session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return session.Session{}, err
	}
	if ws.App == nil {
		return session.Session{}, ErrAgentNotInitialized
	}
	return ws.App.BranchSession(ctx, sessionID, title)
}

// SessionGoal is a session's goal, and false when it has none.
func (b *Backend) SessionGoal(ctx context.Context, workspaceID, sessionID string) (agent.Goal, bool, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return agent.Goal{}, false, err
	}
	if ws.App == nil {
		return agent.Goal{}, false, ErrAgentNotInitialized
	}
	return ws.App.SessionGoal(ctx, sessionID)
}

// SetSessionGoal sets a session's goal, as /goal does, or clears it when
// condition is "", and says what it did; see app.App.SetSessionGoal.
func (b *Backend) SetSessionGoal(ctx context.Context, workspaceID, sessionID, condition string) (agent.GoalChange, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return agent.GoalChange{}, err
	}
	if ws.App == nil {
		return agent.GoalChange{}, ErrAgentNotInitialized
	}
	return ws.App.SetSessionGoal(ctx, sessionID, condition)
}

// Rewind restores a session's conversation, its code or both to the point
// before one of its prompts, as Claude Code's /rewind does.
func (b *Backend) Rewind(ctx context.Context, workspaceID, sessionID, messageID string, opts app.RewindOptions) (app.RewindResult, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return app.RewindResult{}, err
	}
	if ws.App == nil {
		return app.RewindResult{}, ErrAgentNotInitialized
	}
	return ws.App.Rewind(ctx, sessionID, messageID, opts)
}

// GetSession retrieves a session by workspace and session ID.
func (b *Backend) GetSession(ctx context.Context, workspaceID, sessionID string) (session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return session.Session{}, err
	}

	return ws.Sessions.Get(ctx, sessionID)
}

// ListSessions returns all sessions in the given workspace.
func (b *Backend) ListSessions(ctx context.Context, workspaceID string) ([]session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Sessions.List(ctx)
}

// GetAgentSession returns session metadata with the agent's busy
// status.
func (b *Backend) GetAgentSession(ctx context.Context, workspaceID, sessionID string) (proto.AgentSession, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return proto.AgentSession{}, err
	}

	se, err := ws.Sessions.Get(ctx, sessionID)
	if err != nil {
		return proto.AgentSession{}, err
	}

	var isSessionBusy bool
	if ws.AgentCoordinator != nil {
		isSessionBusy = ws.AgentCoordinator.IsSessionBusy(sessionID)
	}

	return proto.AgentSession{
		Session: proto.Session{
			ID:    se.ID,
			Title: se.Title,
		},
		IsBusy: isSessionBusy,
	}, nil
}

// ListSessionMessages returns all messages for a session.
func (b *Backend) ListSessionMessages(ctx context.Context, workspaceID, sessionID string) ([]message.Message, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	// Every remote client reads through here, so this is where interrupted
	// calls get settled for them.
	if ws.AgentCoordinator != nil {
		return ws.AgentCoordinator.ListSettledMessages(ctx, sessionID)
	}
	// Drain debounced updates so clients observe the latest in-memory state.
	if err := ws.Messages.FlushAll(ctx); err != nil {
		return nil, err
	}
	return ws.Messages.List(ctx, sessionID)
}

// ListSessionHistory returns the history items for a session.
func (b *Backend) ListSessionHistory(ctx context.Context, workspaceID, sessionID string) (any, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.History.ListBySession(ctx, sessionID)
}

// SaveSession updates a session in the given workspace.
func (b *Backend) SaveSession(ctx context.Context, workspaceID string, sess session.Session) (session.Session, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return session.Session{}, err
	}

	return ws.Sessions.Save(ctx, sess)
}

// MCPServersDisabled returns the MCP servers disabled for the given
// workspace's repository.
func (b *Backend) MCPServersDisabled(ctx context.Context, workspaceID string) ([]string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Sessions.MCPDisabledServers(ctx)
}

// MCPServersEnabled returns the MCP servers with a repository-scoped
// enabled override (config-disabled servers the user enabled here).
func (b *Backend) MCPServersEnabled(ctx context.Context, workspaceID string) ([]string, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Sessions.MCPServersEnabled(ctx)
}

// SetMCPServerDisabled toggles a repository-scoped MCP override for the
// given workspace and applies it to the running client.
func (b *Backend) SetMCPServerDisabled(ctx context.Context, workspaceID, name string, disabled bool) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	if err := ws.Sessions.SetMCPServerDisabled(ctx, name, disabled); err != nil {
		return err
	}
	return mcptools.SetLocalDisabled(ctx, ws.Cfg, name, disabled)
}

// SetMCPServerConfigDisabled toggles an MCP server's disabled flag in the
// global config and applies the change to the running client.
func (b *Backend) SetMCPServerConfigDisabled(ctx context.Context, workspaceID, name string, disabled bool) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	override, err := session.HasMCPOverride(ctx, ws.Sessions, name)
	if err != nil {
		return err
	}
	return mcptools.SetConfigDisabled(ctx, ws.Cfg, config.ScopeGlobal, name, disabled, override)
}

// StartMCPServer starts the named MCP server for the given workspace even
// when its config entry is disabled. Runtime-only.
func (b *Backend) StartMCPServer(ctx context.Context, workspaceID, name string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	return mcptools.InitializeSingleForced(ctx, name, ws.Cfg)
}

// DeleteSession deletes a session from the given workspace.
func (b *Backend) DeleteSession(ctx context.Context, workspaceID, sessionID string) error {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return err
	}

	return ws.Sessions.Delete(ctx, sessionID)
}

// ListUserMessages returns user-role messages for a session.
func (b *Backend) ListUserMessages(ctx context.Context, workspaceID, sessionID string) ([]message.Message, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Messages.ListUserMessages(ctx, sessionID)
}

// ListAllUserMessages returns all user-role messages across sessions.
func (b *Backend) ListAllUserMessages(ctx context.Context, workspaceID string) ([]message.Message, error) {
	ws, err := b.GetWorkspace(workspaceID)
	if err != nil {
		return nil, err
	}

	return ws.Messages.ListAllUserMessages(ctx)
}
