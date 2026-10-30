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
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// /mem, as the user types it: what it shows, what it undoes and redoes, and
// what it erases, goals, steps, facts and rules, from every file of mem.
func TestMemCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newTestHost(t, dir)
	ctx := context.Background()
	titles := map[string]string{"s1": "Release work"}
	agent := func(code string) *RunResult {
		t.Helper()
		res, err := h.Run(ctx, RunRequest{Project: "/proj", Session: "s1", Name: MemVM, Code: code})
		require.NoError(t, err, code)
		return res
	}
	mem := func(args string) string {
		t.Helper()
		out, err := h.MemCommand(ctx, "/proj", "s2", args, titles)
		require.NoError(t, err, args)
		return out
	}

	require.Contains(t, mem(""), "mem is empty")
	require.Equal(t, "mem has no changes yet", mem("log"))
	agent(`mem.Goal("ship the release").Steps("build", "test")`) // #1
	agent(`mem.Fact("cmd/test", "go test ./...")`)               // #2

	require.Contains(t, mem(""), "`ship-release` \"ship the release\" (0/2 done · session \"Release work\" · ")
	inS1, err := h.MemCommand(ctx, "/proj", "s1", "", titles)
	require.NoError(t, err)
	require.Contains(t, inS1, "(0/2 done · this session · ")
	require.Contains(t, mem("find zzz-nothing"), "/mem lists 1 goal, 0 rules and 1 fact")
	require.Contains(t, mem("show ship-release cmd/test"), "go test ./...")
	require.Contains(t, mem("find release"), "`ship-release`")
	log := mem("log")
	require.Contains(t, log, `#1 `)
	require.Contains(t, log, `the agent in "Release work": mem.Goal("ship the release").Steps("build", "test")`)
	require.Contains(t, log, "    add `ship-release/2`: \"test\"")
	require.Contains(t, log, "    fact `cmd/test`: \"go test ./...\"")
	require.NotContains(t, mem("log 1"), "#1 ")

	// Undo shows the last change first; undoing an undo redoes.
	require.Contains(t, mem("undo"), "to undo it: /mem undo 2")
	out := mem("undo 2")
	require.Contains(t, out, "undid #2 as #3:")
	require.Contains(t, out, "you: /mem undo 2\n    remove fact `cmd/test`")
	require.Contains(t, agent(`mem.Show("cmd/test")`).Output, "no item `cmd/test`")
	require.Contains(t, mem("undo 3"), "undid #3 as #4:")
	require.Contains(t, agent(`mem.Show("cmd/test")`).Output, "go test ./...")

	// What a later change built on is not undone.
	agent(`mem.Done("ship-release/1", "built")`) // #5
	require.Contains(t, mem("undo 1"), "change #1 cannot be undone: #5, after it, changed the same thing (set `ship-release/1` status: \"\" → \"done\")")
	require.Contains(t, mem("undo 9"), "mem has no change #9")
	require.Contains(t, mem("undo x"), "/mem undo takes a change's number")
	require.Contains(t, mem("undo 5"), "undid #5 as #6:")
	require.Contains(t, mem(""), "[ ] `ship-release/1` \"build\"")

	// The user forgets an open goal: it is dropped first.
	out = mem("forget ship-release")
	require.Contains(t, out, "dropped and forgot `ship-release` and its 2 steps")
	require.Contains(t, out, "(mem's journal was written again without what was erased")
	requireNowhere(t, dir, "ship the release")
	require.Contains(t, mem("undo 1"), "change #1 cannot be undone")

	// A fact or a rule is forgotten with its history; only the user forgets
	// a rule.
	require.Contains(t, mem("forget cmd/test"), "forgot fact `cmd/test`")
	requireNowhere(t, dir, "go test ./...")
	require.Contains(t, mem("log"), "#2 ")
	require.Contains(t, mem("undo 2"), "nothing it changed is left in mem's history")
	agent(`mem.Rule("style/emoji", "never use emoji")`)
	require.Contains(t, agent(`mem.Forget("style/emoji")`).Err.Error(), "rules are the user's: they forget one with /mem forget style/emoji")
	require.Contains(t, mem("forget style/emoji"), "forgot rule `style/emoji`")
	requireNowhere(t, dir, "never use emoji")
	require.Contains(t, mem("forget nothing-here"), "no item `nothing-here`")

	// The user redacts text however widely mem holds it.
	for _, title := range []string{"call 555-0199", "text 555-0199", "mail 555-0199", "visit 555-0199"} {
		agent(`mem.Goal("` + title + `")`)
	}
	require.Contains(t, agent(`mem.Redact("555-0199")`).Err.Error(), "that text is in 4 places")
	require.Contains(t, mem("redact 555-0199"), "redacted it in 4 places")
	requireNowhere(t, dir, "555-0199")
	require.Contains(t, mem("log 1"), "you: (redacted)")

	require.Contains(t, mem("repair"), "built again from its journal")
	require.Contains(t, mem(""), "(redacted)")
	require.Contains(t, mem("lgo"), "/mem has no \"lgo\"; it takes:\n/mem ")
	require.Contains(t, mem("find"), "/mem find takes words")
}

// Every change that can be undone, undone and then redone, leaves mem as it
// was; and an undo that would open a step under a closed goal keeps
// nothing.
func TestMemUndoRedoes(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	ctx := context.Background()
	run := func(session, code string) {
		t.Helper()
		res, err := h.Run(ctx, RunRequest{Project: "/proj", Session: session, Name: MemVM, Code: code})
		require.NoError(t, err, code)
		require.NoError(t, res.Err, code)
	}
	mem := func(args string) string {
		t.Helper()
		out, err := h.MemCommand(ctx, "/proj", "s9", args, nil)
		require.NoError(t, err, args)
		return out
	}
	// dump shows mem whatever the order its goals changed in: Show's first
	// line, then each name in full.
	dump := func() string {
		t.Helper()
		res, err := h.Run(ctx, RunRequest{Project: "/proj", Session: "s9", Name: MemVM, Code: `
			println(strings.SplitN(mem.Show(), "\n", 2)[0])
			for _, n := range strings.Fields("release-v2 release-v2/1 release-v2/2 release-v2/2/1 release-v2/3 renew-cert make-tea cmd/test cmd/build style") {
				println(mem.Show(n))
			}`})
		require.NoError(t, err)
		require.NoError(t, res.Err)
		return res.Output
	}
	story := []string{
		`mem.Goal("release v2").Steps("bump the version", "tag v2.0.0", "CI is green")`,
		`mem.Done("release-v2/1", "now 2.0.0")`,
		`mem.Note("release-v2/2", "the tag needs the release key")`,
		`mem.Ask("release-v2/2", "which key signs the tag?")`,
		`mem.Fact("cmd/test", "go test ./...")`,
		`mem.Fact("cmd/test", "go test -race ./...")`,
		`mem.Goal("renew the cert", "renew-cert").Wait("2027-02-01")`,
		`mem.Wait("renew-cert", "")`,
		`mem.Start("release-v2/3")`,
		`mem.Goal("make tea").Steps("boil water", "pour water")`,
		`mem.Drop("make-tea", "no kettle")`,
		`mem.Reopen("make-tea")`,
		`mem.Done("release-v2/2/1", "the YubiKey")`,
		`mem.Fact("cmd/build", "make build")`,
		`mem.Fact("cmd/build", "")`,
	}
	for _, code := range story {
		run("s1", code)
	}
	run("user", `mem.Rule("style", "no emoji")`)

	var undone []int
	for seq := 1; seq <= len(story)+1; seq++ {
		before := dump()
		out := mem("undo " + strconv.Itoa(seq))
		if strings.Contains(out, "cannot be undone") {
			require.Equal(t, before, dump(), "a refused undo keeps nothing")
			continue
		}
		undid := regexp.MustCompile(`^undid #\d+ as #(\d+):`).FindStringSubmatch(out)
		require.NotNil(t, undid, out)
		require.NotEqual(t, before, dump(), "undoing #%d changed nothing", seq)
		require.Contains(t, mem("undo "+undid[1]), "undid #"+undid[1])
		require.Equal(t, before, dump(), "undoing #%d, then the undo", seq)
		undone = append(undone, seq)
	}
	require.Equal(t, []int{2, 3, 6, 8, 9, 12, 13, 15, 16}, undone)

	run("s1", `mem.Done("make-tea/1", "boiled"); mem.Done("make-tea/2", "poured")`)
	run("s1", `mem.Done("make-tea", "tea made")`)
	last := strings.TrimPrefix(regexp.MustCompile(`#\d+`).FindString(mem("undo")), "#")
	seq, err := strconv.Atoi(last)
	require.NoError(t, err)
	before := dump()
	require.Regexp(t, "change #\\d+ cannot be undone: cannot open `make-tea/\\d` under closed `make-tea`", mem("undo "+strconv.Itoa(seq-1)))
	require.Equal(t, before, dump())
}

// An undo is the inverse of each Op, the last first.
func TestMemInverse(t *testing.T) {
	t.Parallel()
	ops := []memOp{
		{Kind: "add", Name: "a", New: "do a", By: "s1"},
		{Kind: "set", Name: "a", Field: "wait", New: "2026-11-01"},
		{Kind: "note", Name: "a", New: "found x"},
		{Kind: "rule", Name: "r", Old: "old", New: "new", By: "s1"},
	}
	inverse, why := memInverse(ops)
	require.Empty(t, why)
	require.Equal(t, []memOp{
		{Kind: "rule", Name: "r", Old: "new", New: "old", By: "user"},
		{Kind: "unnote", Name: "a", Old: "found x"},
		{Kind: "set", Name: "a", Field: "wait", Old: "2026-11-01"},
		{Kind: "unadd", Name: "a", Old: "do a", By: "s1"},
	}, inverse)
	again, _ := memInverse(inverse)
	require.Equal(t, []memOp{
		{Kind: "add", Name: "a", New: "do a", By: "s1"},
		{Kind: "set", Name: "a", Field: "wait", New: "2026-11-01"},
		{Kind: "note", Name: "a", New: "found x"},
		{Kind: "rule", Name: "r", Old: "old", New: "new", By: "user"},
	}, again)
	_, why = memInverse([]memOp{{Kind: "forget", Name: "a"}})
	require.Equal(t, "it forgets, which is final", why)
}
