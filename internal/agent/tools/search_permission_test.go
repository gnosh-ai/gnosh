package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/stretchr/testify/require"
)

// askedPermissions answers every request with grant, and keeps them.
type askedPermissions struct {
	mockPermissionService
	grant bool
	asked []permission.CreatePermissionRequest
}

func (p *askedPermissions) Request(_ context.Context, req permission.CreatePermissionRequest) (bool, error) {
	p.asked = append(p.asked, req)
	return p.grant, nil
}

// grep and glob ask before they search outside the working directory, as
// view and ls ask before reading there, and as Claude Code's Grep and
// Glob do: for a path there, given whole or relative, an absolute
// pattern there, or a pattern that climbs out with "..". Inside, they do
// not ask. A no stops the search, and a yes lets it find what is there.
func TestGrepAndGlobAskBeforeSearchingOutside(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	project := filepath.Join(parent, "proj")
	mkfiles(t, project, "a.go")
	mkfiles(t, parent, "secret/key.txt")
	secret := filepath.Join(parent, "secret")
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "s1")
	grep := func(p permission.Service) fantasy.AgentTool { return NewGrepTool(p, project, config.ToolGrep{}) }
	glob := func(p permission.Service) fantasy.AgentTool { return NewGlobTool(p, project, config.ToolGlob{}) }

	for _, c := range []struct {
		name    string
		tool    func(permission.Service) fantasy.AgentTool
		input   any
		outside string // the directory searched, when it is outside
	}{
		{"grep inside", grep, GrepParams{Pattern: "x"}, ""},
		{"grep path", grep, GrepParams{Pattern: "x", Path: secret}, secret},
		{"grep relative path", grep, GrepParams{Pattern: "x", Path: "../secret"}, secret},
		{"glob inside", glob, GlobParams{Pattern: "*.go"}, ""},
		{"glob path", glob, GlobParams{Pattern: "*.txt", Path: secret}, secret},
		{"glob absolute pattern", glob, GlobParams{Pattern: filepath.Join(secret, "*.txt")}, secret},
		{"glob pattern climbing out", glob, GlobParams{Pattern: "../secret/*.txt"}, secret},
	} {
		b, err := json.Marshal(c.input)
		require.NoError(t, err)
		for _, grant := range []bool{false, true} {
			perms := &askedPermissions{grant: grant}
			tool := c.tool(perms)
			resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call-1", Name: tool.Info().Name, Input: string(b)})
			require.NoError(t, err, c.name)
			if c.outside == "" {
				require.Empty(t, perms.asked, c.name)
				require.False(t, resp.IsError, "%s: %s", c.name, resp.Content)
				require.Contains(t, resp.Content, "a.go", c.name)
				continue
			}
			require.Len(t, perms.asked, 1, c.name)
			asked := perms.asked[0]
			require.Equal(t, [4]string{tool.Info().Name, "search", secret, "s1"}, [4]string{asked.ToolName, asked.Action, asked.Path, asked.SessionID}, c.name)
			if grant {
				require.False(t, resp.IsError, "%s: %s", c.name, resp.Content)
				require.Contains(t, resp.Content, "key.txt", c.name)
			} else {
				require.Equal(t, NewPermissionDeniedResponse(), resp, c.name)
			}
		}
	}
}
