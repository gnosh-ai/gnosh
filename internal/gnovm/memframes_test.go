// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestHostFrames is the host's side of frames (FRAMES.md): the stack a call
// runs with is the request's, the pushes and pops come back in the result
// and go on the journal entry, /mem log shows them, the receipt ends with
// the next hint after a pop no push followed, a pop's entry is undone like
// any other, and Recall finds a thought through the index.
func TestHostFrames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestHost(t, dir)
	ctx := context.Background()
	run := func(session string, stack []string, code string) *RunResult {
		t.Helper()
		res, err := h.Run(ctx, RunRequest{Project: "/proj", Session: session, Name: MemVM, Code: code, Frames: stack})
		require.NoError(t, err, code)
		return res
	}
	ok := func(session string, stack []string, code string) *RunResult {
		t.Helper()
		res := run(session, stack, code)
		require.NoError(t, res.Err, res.Output)
		return res
	}

	ok("s1", nil, `mem.Goal("fix the flaky test", "fix-flaky").Steps("find-cause: find why it fails", "rerun: rerun the suite")`)
	res := ok("s1", nil, `mem.PushFrame(mem.Frame("fix-flaky"))`)
	require.Equal(t, []MemFrame{{Kind: "push", Id: "fix-flaky", Purpose: "fix the flaky test", Session: "s1"}}, res.Frames)
	require.Len(t, res.MemGoals, 1, "a push changes the goal, as the task list sees it")

	// The stack is the agent's: with none given, the session is at the root.
	res = run("s1", nil, `mem.PopFrame("x")`)
	require.ErrorContains(t, res.Err, "nothing to pop: you are at the root")
	res = run("s1", nil, `mem.PushFrame(mem.Frame().NextTodo())`)
	require.Contains(t, res.Output, "nothing to push at the root")

	stack := []string{"fix-flaky"}
	res = ok("s1", stack, `mem.PushFrame(mem.Frame().NextTodo())`)
	require.Equal(t, []MemFrame{{Kind: "push", Id: "fix-flaky/find-cause", Purpose: "find why it fails", Session: "s1"}}, res.Frames)
	stack = append(stack, "fix-flaky/find-cause")
	ok("s1", stack, `mem.Think("port", "the shared port is 8080")`)

	// Pop then push in one input: both events, in order; no hint, since a
	// push followed.
	res = ok("s1", stack, "mem.PopFrame(\"it races on the shared port\")\nmem.PushFrame(mem.Frame().NextTodo())")
	require.Equal(t, []MemFrame{
		{Kind: "pop", Id: "fix-flaky/find-cause", Purpose: "find why it fails", Result: "it races on the shared port", Thoughts: 1, Session: "s1"},
		{Kind: "push", Id: "fix-flaky/rerun", Purpose: "rerun the suite", Session: "s1"},
	}, res.Frames)
	require.Contains(t, res.Output, "done `fix-flaky/find-cause` \"find why it fails\" → it races on the shared port\n")
	require.Contains(t, res.Output, "in `fix-flaky/rerun` \"rerun the suite\": when it is done, pop it")
	require.NotContains(t, res.Output, "next:")
	stack = []string{"fix-flaky", "fix-flaky/rerun"}

	// A lone pop ends with the hint; the thought of the popped step is
	// reached by name from the next, and by meaning through the index.
	res = ok("s1", stack, `mem.Thought("port")`)
	require.Equal(t, "thought `fix-flaky/find-cause#port`: the shared port is 8080\n", res.Output)
	res = ok("s1", stack, `mem.Recall("shared port")`)
	require.Contains(t, res.Output, "thought `fix-flaky/find-cause#port`: the shared port is 8080\n")
	res = ok("s1", stack, `mem.PopFrame("20 of 20 passed")`)
	require.True(t, strings.HasSuffix(res.Output, "next: pop `fix-flaky` with the evidence that it holds\n"), res.Output)
	seq := res.Seq
	stack = []string{"fix-flaky"}

	// The journal keeps the pushes and pops beside the Ops, and /mem log
	// shows them.
	log, err := h.MemCommand(ctx, "/proj", "s1", "log", nil)
	require.NoError(t, err)
	require.Contains(t, log, "    push `fix-flaky`\n")
	require.Contains(t, log, "    pop `fix-flaky/find-cause` → \"it races on the shared port\"\n    push `fix-flaky/rerun`\n")
	require.Contains(t, log, "    pop `fix-flaky/rerun` → \"20 of 20 passed\"")

	// Undo of the entry that popped a step undoes its Done: the step is open
	// and the session's again; the rows the pop removed stay removed (the
	// agent's side).
	out, err := h.MemCommand(ctx, "/proj", "s1", "undo "+strconv.FormatInt(seq, 10), nil)
	require.NoError(t, err)
	require.Contains(t, out, "undid #")
	res = ok("s1", stack, `println(mem.Show("fix-flaky/rerun"))`)
	require.Contains(t, res.Output, "[ ] `fix-flaky/rerun` \"rerun the suite\"")
	require.Contains(t, res.Output, "held by this session")

	// Another session: the step is held.
	res = run("s2", nil, `mem.PushFrame(mem.Frame("fix-flaky/rerun"))`)
	require.ErrorContains(t, res.Err, "`fix-flaky/rerun` is held by session s1: only it can pop, close or drop it")
	require.Empty(t, res.Frames, "a refused input records no event")

	// A redacted secret leaves the journal's frames record too: the pop's
	// result in /mem log.
	ok("s1", stack, `mem.Redact("shared port")`)
	log, err = h.MemCommand(ctx, "/proj", "s1", "log", nil)
	require.NoError(t, err)
	require.NotContains(t, log, "shared port")
	require.Contains(t, log, "pop `fix-flaky/find-cause` → \"it races on the (redacted)\"")

	// A name gnosh made for a text (its small model's) reaches the realm by
	// text; a name the frame has already gets -2; a text with no made name
	// is named from its words.
	res, err = h.Run(ctx, RunRequest{Project: "/proj", Session: "s1", Name: MemVM, Code: "mem.Think(\"the suite shares port 8080\")\nmem.Think(\"the suite shares port 8080\")\nmem.Think(\"nobody named this one\")", Frames: stack, Names: map[string]string{"the suite shares port 8080": "shared-port"}})
	require.NoError(t, err)
	require.NoError(t, res.Err, res.Output)
	require.Contains(t, res.Output, "thought `shared-port` kept on `fix-flaky`\nthought `shared-port-2` kept on `fix-flaky`\nthought `nobody-named-one` kept on `fix-flaky`\n")
}
