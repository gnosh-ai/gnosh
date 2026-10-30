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
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A goal's title is the condition's first line, cut at a word to fit.
func TestGoalTitle(t *testing.T) {
	t.Parallel()
	require.Equal(t, "go test ./... passes", goalTitle("  go test ./...   passes\nand nothing else breaks"))
	require.Empty(t, goalTitle(" \n \t"))
	long := strings.Repeat("word ", 60)
	require.Equal(t, strings.TrimSpace(long[:200]), goalTitle(long))
	require.LessOrEqual(t, len(goalTitle(strings.Repeat("é", 150))), 200)
}

// /goal binds a session to a goal in mem: the open one titled as its
// condition, or one it adds; and as each turn ends, the goal says what
// comes next (MEMPLAN §7's table).
func TestGoalBinding(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestHost(t, dir)
	ctx := context.Background()
	agent := func(code, goal string) *RunResult {
		t.Helper()
		res, err := h.Run(ctx, RunRequest{Project: "/proj", Session: "s1", Name: MemVM, Code: code, Goal: goal})
		require.NoError(t, err, code)
		return res
	}
	turn := func(b GoalBinding) (GoalTurn, GoalBinding) {
		t.Helper()
		tn, b, err := h.GoalTurnEnd(ctx, "/proj", "s1", b, "STOP")
		require.NoError(t, err)
		return tn, b
	}

	b, directive, err := h.BindGoal(ctx, "/proj", "s1", "ship the release\nwith the notes written", nil)
	require.NoError(t, err)
	require.Equal(t, "ship-release", b.Name)
	require.Equal(t, "ship the release", b.Title)
	require.True(t, b.Added)
	require.NotEmpty(t, b.Instance)
	require.Contains(t, directive, "Goal `ship-release` \"ship the release\" is set: ship the release\nwith the notes written")
	require.Contains(t, directive, "mem.Done(\"ship-release\", \"stopped: <why>\")")

	// The same condition binds the same goal, still one /goal added.
	again, _, err := h.BindGoal(ctx, "/proj", "s1", "Ship the release.", &b)
	require.NoError(t, err)
	require.Equal(t, b.Name, again.Name)
	require.True(t, again.Added)

	// The session's own agent may not drop or forget its /goal.
	res := agent(`mem.Drop("ship-release", "too hard")`, b.Name)
	require.ErrorContains(t, res.Err, "`ship-release` is the user's /goal for this session, so this session does not drop it")

	tn, b := turn(b)
	require.Equal(t, "continue", tn.Do)
	require.Contains(t, tn.Prompt, "none of its steps is open")
	agent(`mem.Steps("ship-release", "write the notes", "tag it")`, b.Name)
	tn, b = turn(b)
	require.Equal(t, "continue", tn.Do)
	require.Contains(t, tn.Prompt, "Its next step is `ship-release/1` \"write the notes\"")

	// Waiting on the user ends the turn; waiting on words asks once first.
	agent(`mem.Done("ship-release/1", "written"); mem.Ask("ship-release/2", "which key signs the tag?")`, b.Name)
	tn, b = turn(b)
	require.Equal(t, "end", tn.Do)
	require.Contains(t, tn.Notice, "`ship-release/2/1` \"which key signs the tag?\" is for the user")
	agent(`mem.Done("ship-release/2/1", "the YubiKey"); mem.Wait("ship-release/2", "CI is green")`, b.Name)
	tn, b = turn(b)
	require.Equal(t, "checkpoint", tn.Do)
	require.Contains(t, tn.Prompt, "`ship-release/2` \"tag it\" waits for \"CI is green\"")
	require.True(t, strings.HasSuffix(tn.Prompt, "on its own:\nSTOP"))
	tn, b = turn(b)
	require.Equal(t, "end", tn.Do, "asked once already")

	// Closed as done, the judge is asked; not met, the goal is reopened.
	agent(`mem.Wait("ship-release/2", ""); mem.Done("ship-release/2", "tagged v2.0.0"); mem.Done("ship-release", "git tag → v2.0.0")`, b.Name)
	tn, b = turn(b)
	require.Equal(t, "judge", tn.Do)
	require.Contains(t, tn.Show, "tagged v2.0.0")
	require.NoError(t, h.GoalReopen(ctx, "/proj", b, "the notes are not published"))
	tn, b = turn(b)
	require.Equal(t, "continue", tn.Do)
	show := agent(`mem.Show("ship-release")`, b.Name).Output
	require.Contains(t, show, "the check found it not met: the notes are not published")

	// A new condition replaces the goal the earlier one added.
	next, _, err := h.BindGoal(ctx, "/proj", "s1", "fix the flaky test", &b)
	require.NoError(t, err)
	require.Equal(t, "fix-flaky-test", next.Name)
	require.Contains(t, agent(`mem.Show("ship-release")`, next.Name).Output, "replaced by /goal \"fix the flaky test\"")
	tn, _ = turn(b)
	require.Equal(t, "clear", tn.Do)
	require.Contains(t, tn.Notice, "was dropped (replaced by /goal")

	// /goal clear drops what it added; a goal it found stays.
	out, err := h.GoalClear(ctx, "/proj", "s1", next)
	require.NoError(t, err)
	require.Equal(t, "/goal is cleared, and the goal `fix-flaky-test` it added is dropped", out)
	agent(`mem.Goal("write the docs")`, "")
	found, _, err := h.BindGoal(ctx, "/proj", "s1", "write the docs", nil)
	require.NoError(t, err)
	require.False(t, found.Added)
	out, err = h.GoalClear(ctx, "/proj", "s1", found)
	require.NoError(t, err)
	require.Equal(t, "/goal is cleared; the goal `write-docs` stays in mem", out)

	// A mem made anew is bound again.
	require.NoError(t, os.RemoveAll(memDir(dir, "/proj")))
	tn, _ = turn(found)
	require.Equal(t, "rebind", tn.Do)

	_, _, err = h.BindGoal(ctx, "/proj", "s1", " \n", nil)
	require.ErrorContains(t, err, "/goal takes what should hold")
}

// Another session's start ends the turn; a goal that waits itself is asked
// about first.
func TestGoalTurnTable(t *testing.T) {
	t.Parallel()
	b := GoalBinding{Name: "g", Title: "the goal"}
	held := func(why, wait, doing string) memGoalJSON {
		var g memGoalJSON
		g.Name, g.Title = "g", "the goal"
		g.Held = append(g.Held, struct {
			Name  string `json:"name"`
			Why   string `json:"why"`
			Title string `json:"title"`
			Wait  string `json:"wait"`
			Doing string `json:"doing"`
		}{"g/1", why, "a step", wait, doing})
		return g
	}
	tn, _ := goalTurn(b, held("session", "", "8b1f3c2a-0000"), "2026-10-30", "STOP")
	require.Equal(t, "end", tn.Do)
	require.Contains(t, tn.Notice, "`g/1` \"a step\" was started by session 8b1f3c2a")
	tn, _ = goalTurn(b, held("day", "2026-11-03", ""), "2026-10-30", "STOP")
	require.Equal(t, "end", tn.Do)
	require.Contains(t, tn.Notice, "`g/1` \"a step\" waits until 2026-11-03")
	g := held("day", "2026-11-03", "")
	g.Wait = "2026-11-03"
	tn, b2 := goalTurn(b, g, "2026-10-30", "STOP")
	require.Equal(t, "checkpoint", tn.Do, "the goal itself waits")
	tn, _ = goalTurn(b2, g, "2026-10-30", "STOP")
	require.Equal(t, "end", tn.Do)
	for status, want := range map[string]string{"dropped": "was dropped", "forgotten": "was forgotten", "missing": "no longer in mem"} {
		g := memGoalJSON{}
		g.Status = status
		tn, _ := goalTurn(b, g, "2026-10-30", "STOP")
		require.Equal(t, "clear", tn.Do)
		require.Contains(t, tn.Notice, want)
	}
	require.Contains(t, GoalCheckpoint(b, 10, "STOP"), "the goal `g` \"the goal\" 10 times")
}
