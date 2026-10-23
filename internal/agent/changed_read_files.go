package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/filetracker"
)

// changedReadFiles wraps a tool, bash, so that a call that changes files
// the session has read says which. The agent's next edit of such a file
// is refused until it views the file again; without being told, it found
// out by having the edit refused, a model round trip each time (runs 13
// and 14, after their own git stash and Python edits). Claude Code tells
// its model of a read file that changed, too. A file the call rewrote as
// it was, as git stash and pop do, is not named.
type changedReadFiles struct {
	fantasy.AgentTool
	files      filetracker.Service
	workingDir string
}

func (t *changedReadFiles) Unwrap() fantasy.AgentTool { return t.AgentTool }

// fileStamp is what a file's modification time and size were, to tell
// cheaply whether a call touched it.
type fileStamp struct {
	exists  bool
	modTime time.Time
	size    int64
}

func (s fileStamp) same(o fileStamp) bool {
	return s.exists == o.exists && s.size == o.size && s.modTime.Equal(o.modTime)
}

func stampOf(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{exists: true, modTime: info.ModTime(), size: info.Size()}
}

func (t *changedReadFiles) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	sessionID := tools.GetSessionFromContext(ctx)
	before := map[string]fileStamp{}
	if sessionID != "" {
		if read, err := t.files.ListReadFiles(ctx, sessionID); err == nil {
			for _, path := range read {
				before[path] = stampOf(path)
			}
		}
	}
	resp, err := t.AgentTool.Run(ctx, call)
	if err != nil || len(before) == 0 {
		return resp, err
	}
	var changed []string
	for path, stamp := range before {
		if stampOf(path).same(stamp) || t.files.UnchangedSinceRead(ctx, sessionID, path) {
			continue
		}
		if rel, relErr := filepath.Rel(t.workingDir, path); relErr == nil && !strings.HasPrefix(rel, "..") {
			path = rel
		}
		changed = append(changed, path)
	}
	if len(changed) == 0 {
		return resp, nil
	}
	slices.Sort(changed)
	resp.Content += "\n\nThis command changed files you read before: " + strings.Join(changed, ", ") +
		". View them again before you edit them."
	return resp, nil
}
