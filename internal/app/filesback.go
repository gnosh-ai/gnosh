package app

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/permission"
	"github.com/gnosh-ai/gnosh/internal/prompt"
	"github.com/gnosh-ai/gnosh/internal/tools/fstools"
)

// This file is what a summary gives back of the files the model was
// working on. A summary takes every copy of a file out of the model's view,
// and the work goes on with the files it read or changed last: as Claude
// Code does after compacting, they are read again as they now are and
// given back after the summary, so that they count as read and an Edit of
// one needs no Read first. They keep to a share of the model's window, so
// that what the summary made room for is not filled again at once; the
// ones that do not fit are named, to be read before they are changed.

const (
	// backFiles is how many files are given back at most.
	backFiles = 5
	// backShare is the share of the model's window all of them may take
	// together, and backTokens the most they may take whatever the window;
	// backFileTokens is the most one of them may take.
	backShare      = 10
	backTokens     = 50_000
	backFileTokens = 5_000
)

// filesBack reads again the files recent lists (the session's last, the
// most recent first) and gives back each that fits; window is the model's.
// It runs after the session's records of what it read were forgotten, so
// that only the files given back count as read; it returns them as well.
// A file that is gone, is no longer a file, or that a deny rule now keeps
// from Read is passed over.
func (l *live) filesBack(ctx context.Context, recent []fstools.FileState, window int) (parts []agent.Part, given []string) {
	read := l.readTool()
	if read == nil || len(recent) == 0 || window <= 0 {
		return nil, nil
	}
	name := read.Spec().Name
	budget := min(backTokens, window/backShare)
	env := l.env()
	var shown, named []string
	for _, f := range recent {
		if ctx.Err() != nil {
			break
		}
		info, err := os.Stat(f.Path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		input, _ := json.Marshal(map[string]string{"file_path": f.Path})
		call := core.ToolCall{ID: newCallID(), Name: name, Input: input}
		act := core.Action{Kind: core.ActionRead, Paths: []string{f.Path}}
		if d := l.engine.DecideAt(env.Cwd, name, call, act); d.Behavior == permission.Deny {
			continue
		}
		each := min(backFileTokens, budget)
		if roughTokens(int(min(info.Size(), 1<<30))) > each {
			named = append(named, f.Path)
			continue
		}
		res, err := read.Run(ctx, call, env)
		text := core.TextOf(res.Content)
		if err != nil || res.IsError || hasPicture(res.Content) || roughTokens(len(text)) > each {
			// Read counted it as read: it is not given back after all.
			fstools.Forget(env, f.Path)
			named = append(named, f.Path)
			continue
		}
		budget -= roughTokens(len(text))
		given = append(given, f.Path)
		shown = append(shown, l.showPath(f.Path))
		parts = append(parts, agent.Part{Source: "file-back:" + f.Path, Text: l.texts.FileShownBack(f.Path) + "\n" + prompt.Untrusted(text)})
		// The instruction files of its folders come with it, as after a
		// Read.
		parts = append(parts, l.lateInstructions(ctx, agent.ToolDone{Call: call, Tool: read, Result: res})...)
	}
	if len(named) > 0 {
		parts = append(parts, agent.Part{Source: "files-back", Text: l.texts.FilesNotShownBack(named)})
	}
	if len(shown) > 0 {
		l.notice(agent.LevelInfo, "summary", "Given back after the summary, as they now are: "+strings.Join(shown, ", ")+".")
	}
	return parts, given
}

// roughTokens is about how many tokens n bytes of text take, as the agent
// loop counts them before a model has: three bytes to a token.
func roughTokens(n int) int { return (n + 2) / 3 }

// hasPicture says that blocks hold a picture.
func hasPicture(blocks []core.Block) bool {
	for _, b := range blocks {
		if b.Type == core.BlockImage {
			return true
		}
	}
	return false
}
