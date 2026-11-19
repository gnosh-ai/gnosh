package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// This file is Claude Code's fileSuggestion: a command of the person's own
// offers the paths an @ mention completes to, in place of gnosh's own
// search (for a big repository with an index of its own). It is given
// {"query": what was typed after the @} on its standard input, with the
// variables a hook has (CLAUDE_PROJECT_DIR among them), and prints a path
// a line, of which fifteen are offered. It is given five seconds, and what
// it started ends with it. A project's command waits for trust, as a
// hook's does, and safe mode and disableAllHooks turn it off.

// fileSuggestWait is how long the command is given, fileSuggestMax how
// many of its paths are offered, and fileSuggestMaxBytes how much of what
// it prints is read.
var fileSuggestWait = 5 * time.Second

const (
	fileSuggestMax      = 15
	fileSuggestMaxBytes = 64 << 10
)

// SuggestFiles runs the fileSuggestion command for what was typed after an
// @ and returns the paths it printed, at most fifteen. ok is false when
// there is no such command: gnosh's own search is used then.
func (c *controls) SuggestFiles(ctx context.Context, query string) (paths []string, ok bool, err error) {
	l := c.cur()
	fs := l.cfg.FileSuggestion
	if fs == nil || l.gitRunsPrograms() {
		// (In a folder whose git settings name programs, gnosh's own
		// search, which keeps git from running them, is used instead: the
		// command may call git; see startStatusLine.)
		return nil, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, fileSuggestWait)
	defer cancel()
	input, _ := json.Marshal(map[string]string{"query": query})
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", fs.Command)
	cmd.Dir = l.cwd()
	cmd.Env = l.fileSuggestEnv()
	cmd.Stdin = bytes.NewReader(input)
	var out capBuffer
	out.max = fileSuggestMaxBytes
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	groupOf(cmd)
	runErr := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, true, fmt.Errorf("the fileSuggestion command (%s) did not answer within %s", fs.Command, fileSuggestWait)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if p := strings.TrimSpace(line); p != "" && !slices.Contains(paths, p) {
			paths = append(paths, p)
			if len(paths) == fileSuggestMax {
				break
			}
		}
	}
	if runErr != nil && len(paths) == 0 {
		return nil, true, fmt.Errorf("the fileSuggestion command (%s) failed: %v", fs.Command, runErr)
	}
	return paths, true, nil
}

// fileSuggestEnv is the environment of the fileSuggestion command: gnosh's
// own, the env block's, and the variables a hook is given that do not
// belong to an event.
func (l *live) fileSuggestEnv() []string {
	env := l.ownCommandEnv()
	env = slices.DeleteFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "CLAUDECODE=") })
	root := l.cfg.Paths.ProjectRoot
	return append(env, "GNOSH=1", "AGENT=gnosh", "AI_AGENT=gnosh", "GNOSH_SESSION_ID="+l.ts.ID(),
		"GNOSH_CWD="+l.cwd(), "GNOSH_PROJECT_DIR="+root, "CLAUDE_PROJECT_DIR="+root)
}
