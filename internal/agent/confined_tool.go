package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/filepathext"
)

// confinedTool runs a file tool only on what lies inside dir: view's
// file_path, grep's and glob's path, and glob's pattern, whose leading
// directories glob walks from that path. It keeps the fetch agent's file
// tools to the directory holding the pages it fetched. That agent reads
// untrusted pages and fetches any URL without asking, in a session whose
// permission requests are all approved, so a page that told it to read
// ~/.ssh/id_ed25519 or the project's .env and put it in a URL would
// otherwise have it done.
type confinedTool struct {
	fantasy.AgentTool
	dir string
}

func confine(dir string, tool fantasy.AgentTool) fantasy.AgentTool {
	return confinedTool{AgentTool: tool, dir: dir}
}

func (t confinedTool) Unwrap() fantasy.AgentTool { return t.AgentTool }

func (t confinedTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	var input struct {
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		Pattern  string `json:"pattern"`
	}
	if err := json.Unmarshal([]byte(call.Input), &input); err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("invalid parameters: %s", err)), nil
	}
	for _, p := range []string{input.FilePath, input.Path} {
		if !insideDir(t.dir, p) {
			return t.refuse(p), nil
		}
	}
	// A grep pattern is a regular expression, not a path.
	if t.Info().Name == tools.GlobToolName && slices.Contains(strings.Split(filepath.ToSlash(input.Pattern), "/"), "..") {
		return t.refuse(input.Pattern), nil
	}
	return t.AgentTool.Run(ctx, call)
}

func (t confinedTool) refuse(p string) fantasy.ToolResponse {
	return fantasy.NewTextErrorResponse(fmt.Sprintf("%s is outside %s, which holds the pages fetched for this task; only they can be read here", p, t.dir))
}

// insideDir reports whether p, taken relative to dir as the file tools
// take it, is dir or lies under it.
func insideDir(dir, p string) bool {
	rel, err := filepath.Rel(dir, filepathext.SmartJoin(dir, p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
