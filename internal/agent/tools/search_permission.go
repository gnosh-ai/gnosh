package tools

import (
	"context"
	"fmt"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/permission"
)

// askToSearch asks before a search of dir outside the working directory,
// as view and ls ask before reading there, and as Claude Code asks before
// its Grep and Glob read there. stop is set when the search must not go
// on, with resp the answer to give when the user said no.
func askToSearch(ctx context.Context, permissions permission.Service, call fantasy.ToolCall, toolName, dir, workingDir string, params any) (resp fantasy.ToolResponse, stop bool, err error) {
	if fsext.HasPrefix(dir, workingDir) {
		return fantasy.ToolResponse{}, false, nil
	}
	sessionID := GetSessionFromContext(ctx)
	if sessionID == "" {
		return fantasy.ToolResponse{}, true, fmt.Errorf("session ID is required for searching outside the working directory")
	}
	granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
		SessionID:   sessionID,
		Path:        dir,
		ToolCallID:  call.ID,
		ToolName:    toolName,
		Action:      "search",
		Description: fmt.Sprintf("Search outside working directory: %s", dir),
		Params:      params,
	})
	if err != nil {
		return fantasy.ToolResponse{}, true, err
	}
	if !granted {
		return NewPermissionDeniedResponse(), true, nil
	}
	return fantasy.ToolResponse{}, false, nil
}
