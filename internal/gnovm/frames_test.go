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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// framesStory is a memStory whose sessions have stacks of frames: the
// natives answer each session's stack as its own pushes and pops left it,
// as gnosh derives it from the session's rows.
type framesStory struct {
	*memStory
	stacks map[string][]string
}

func newFramesStory(t *testing.T) *framesStory {
	return &framesStory{memStory: newMemStory(t), stacks: map[string][]string{}}
}

// run runs code as who, bound to goal bound by /goal ("" for none), with
// who's stack, and keeps the stack as the call left it. The transcript
// shows the pushes and pops the call reported, and the hint the host adds
// after a pop no push followed.
func (s *framesStory) run(who, bound, code string) {
	stack := s.stacks[who]
	call, err := s.r.commitNext(s.r.call(code, memContext{caller: who, bound: bound, stack: stack, holding: s.holding}))
	out := call.Output
	if err == nil {
		if hint := nextHint(s.r, who, bound, stack, call.Frames, call.NilPush); hint != "" {
			out += hint + "\n"
		}
		s.stacks[who] = stackAfter(stack, call.Frames)
	}
	s.out.WriteString("> " + who + ": " + code + "\n" + out)
	for _, f := range call.Frames {
		line := "  [" + f.Kind + " `" + f.Id + "`"
		if f.Kind == "pop" {
			line += " → " + f.Result
		}
		s.out.WriteString(line + "]\n")
	}
	if err != nil {
		s.out.WriteString("error: " + err.Error() + "\n")
	} else if len(call.Ops) > 0 {
		s.calls = append(s.calls, call)
	}
	s.check(s.r, code)
	s.now = s.now.Add(time.Minute)
}

// TestFramesStory runs the frames' calls (FRAMES.md) through a story of
// three sessions and the user, and checks its transcript against
// testdata/gnomem/v2/frames.golden (GNOMEM_V2_WRITE=1 rewrites it): the
// trampoline, thoughts, the refusals, held frames, a bound root, and the
// States NextTodo reads. Then it rebuilds the mem from the story's Ops
// alone, as gnosh rebuilds from its journal: the pushes and pops are not
// Ops, and the rebuilt mem shows exactly as the live one.
func TestFramesStory(t *testing.T) {
	s := newFramesStory(t)
	// Named steps, the root, and what a nil push says there.
	s.run("s1", "", `mem.Goal("fix the flaky test", "fix-flaky").Steps("reproduce: reproduce it", "find why TestFlaky fails", "rerun: rerun the suite 20 times")`)
	s.run("s1", "", `mem.NewFrame("side", "a side quest made at the root")`)
	s.run("s1", "", `mem.Frame()`)
	s.run("s1", "", `mem.PushFrame(mem.Frame().NextTodo())`)
	s.run("s1", "", `mem.PushFrame(mem.Frame())`)
	s.run("s1", "", `mem.Think("port", "the shared port is 8080")`)
	s.run("s1", "", `mem.Frame("fix-flaky/retry")`)
	s.run("s1", "", `mem.Frame("Fix-Flaky")`)
	// Into the goal; a new step's name.
	s.run("s1", "", `mem.PushFrame(mem.Frame("fix-flaky"))`)
	s.run("s1", "", `mem.NewFrame("Rename-Port-Flag")`)
	s.run("s1", "", `mem.NewFrame("rename port flag")`)
	s.run("s1", "", `mem.NewFrame("rename the port flag", "rename-port-flag")`)
	s.run("s1", "", `mem.NewFrame("reproduce")`)
	s.run("s1", "", `mem.NewFrame("rename-port-flag", "rename the port flag")`)
	s.run("s1", "", `mem.PushFrame(mem.Frame("fix-flaky"))`)
	s.run("s1", "", `mem.PushFrame(mem.Frame().NextTodo())`)
	// Thoughts on the present frame.
	s.run("s1", "", `mem.Think("port", "the user says the shared port is 8080")`)
	s.run("s1", "", `mem.Think("port", "again")`)
	s.run("s1", "", `mem.Think("Port", "again")`)
	s.run("s1", "", `mem.Think("", "again")`)
	s.run("s1", "", `mem.Note("fix-flaky/reproduce", "it fails 3 of 20 runs")`)
	s.run("s1", "", `mem.Think("the fix is to pick a free port in setup")`)
	s.run("s1", "", `mem.Think("the fix is to pick a free port in setup")`)
	s.run("s1", "", `mem.Think("a", "b", "c")`)
	s.run("s1", "", `mem.Fact("the staging database is db-stg-7")`)
	s.run("s1", "", `mem.Fact("the staging database is db-stg-7")`)
	s.run("s1", "", `mem.Fact("cmd/test")`)
	s.run("s1", "", `mem.Thought("nope")`)
	s.run("s1", "", `mem.Frame()`)
	// Closing the present frame by another spelling pops it; below the top
	// is refused.
	s.run("s1", "", `mem.PopFrame("")`)
	s.run("s1", "", `mem.Done("fix-flaky/reproduce")`)
	s.run("s1", "", `mem.Done("fix-flaky", "done")`)
	s.run("s1", "", `mem.Drop("fix-flaky", "too hard")`)
	s.run("s1", "", `mem.Done("fix-flaky/reproduce", "it fails 3 of 20 runs on the shared port")`)
	s.run("s1", "", `mem.PopFrame("twice")`)
	// A thought of a popped step, reached from its goal, and dived into
	// from the next step.
	s.run("s1", "", `mem.Thought("port")`)
	s.run("s1", "", `mem.PushFrame(mem.Frame().NextTodo())`)
	s.run("s1", "", `mem.PushThought(mem.Thought("port"))`)
	s.run("s1", "", `mem.PushThought(mem.Thought("port"))`)
	s.run("s1", "", `mem.Frame("fix-flaky/reproduce")`)
	s.run("s1", "", `mem.PopFrame("8080 comes from PORT in setup")`)
	s.run("s1", "", `mem.Frame()`)
	// Several statements in one input: a refusal discards the pop too.
	s.run("s1", "", "mem.PopFrame(\"TestFlaky races on the shared port\")\nmem.PushFrame(mem.Frame(\"fix-flaky/nope\"))")
	s.run("s1", "", `mem.Frame().Id`)
	s.run("s1", "", "mem.PopFrame(\"TestFlaky races on the shared port; fixed by picking a free port in setup (internal/net/listen_test.go:41)\")\nmem.PushFrame(mem.Frame().NextTodo())")
	// A dive with a reason; a thought's step entered again.
	s.run("s1", "", `mem.Think("flake-rate", "3 of 20 runs failed before the fix")`)
	s.run("s1", "", `mem.PushThought(mem.Thought("flake-rate"), "measure it again after the fix")`)
	s.run("s1", "", `mem.PopFrame("0 of 20 after the fix")`)
	s.run("s1", "", `mem.Reopen("fix-flaky/rerun/flake-rate")`)
	s.run("s1", "", `mem.PushThought(mem.Thought("flake-rate"))`)
	s.run("s1", "", `mem.PopFrame("0 of 50 after the fix")`)
	// Drop of the present frame pops it, its open steps dropped with it.
	s.run("s1", "", "mem.NewFrame(\"twenty\", \"twenty runs\")\nmem.NewFrame(\"fifty\", \"fifty runs\")")
	s.run("s1", "", `mem.PushFrame(mem.Frame().NextTodo())`)
	s.run("s1", "", `mem.PopFrame("20 of 20 passed")`)
	s.run("s1", "", `mem.Drop("fix-flaky/rerun", "twenty runs were enough")`)
	// A frame another session holds: nobody pops, closes, drops, forgets or
	// takes it; that session pops it.
	s.run("s2", "", `mem.PushFrame(mem.Frame("fix-flaky/rename-port-flag"))`)
	s.run("s1", "", `mem.PushFrame(mem.Frame().NextTodo())`)
	s.run("s1", "", `mem.Done("fix-flaky/rename-port-flag", "renamed")`)
	s.run("s1", "", `mem.Drop("fix-flaky/rename-port-flag", "not needed")`)
	s.run("s1", "", `mem.Start("fix-flaky/rename-port-flag")`)
	s.run("s1", "", `mem.PushFrame(mem.Frame("fix-flaky/rename-port-flag"))`)
	s.run("s1", "", `mem.PopFrame("the suite passes with the free port")`)
	s.run("user", "", `mem.Forget("fix-flaky/rename-port-flag")`)
	s.run("user", "", `mem.Drop("fix-flaky", "the user gives up")`)
	s.run("s3", "", `mem.PushFrame(mem.Frame("fix-flaky"))`)
	s.run("s3", "", `mem.PushFrame(mem.Frame().NextTodo())`)
	s.run("s2", "", `mem.PopFrame("the flag is --port now")`)
	s.run("s3", "", `mem.PushFrame(mem.Frame().NextTodo())`)
	s.run("s1", "", `mem.PopFrame("the suite passes 20 of 20 with the free port")`)
	s.run("s1", "", `mem.PushFrame(mem.Frame().NextTodo())`)
	s.run("s1", "", `println(mem.Show("fix-flaky"))`)
	s.run("s1", "", `println(mem.Show("fix-flaky/rerun"))`)
	// An off-stack ancestor: a step entered by id, then its goal.
	s.run("s3", "", `mem.PushFrame(mem.Frame("side"))`)
	s.run("s3", "", `mem.PushFrame(mem.NewFrame("look", "look around"))`)
	s.run("s1", "", `mem.PushFrame(mem.Frame("side/look"))`)
	s.run("s1", "", `mem.Done("side", "nothing to see")`)
	s.run("s3", "", `mem.PopFrame("nothing here")`)
	s.run("s3", "", `mem.PopFrame("nothing to see")`)
	// A bound root: the goal /goal bound the session to is where it stands.
	s.run("user", "", `mem.Bind("deploy the site")`)
	s.run("s4", "deploy-site", `mem.Frame()`)
	s.run("s4", "deploy-site", `mem.PushFrame(mem.Frame("deploy-site"))`)
	s.run("s4", "deploy-site", `mem.PushFrame(mem.Frame().NextTodo())`)
	s.run("s4", "deploy-site", `mem.NewFrame("build", "build it")`)
	s.run("s4", "deploy-site", `mem.PushFrame(mem.Frame().NextTodo())`)
	s.run("s4", "deploy-site", `mem.Done("deploy-site", "built")`)
	s.run("s4", "deploy-site", `mem.PopFrame("built in 3s")`)
	s.run("s4", "deploy-site", `mem.PushFrame(mem.Frame().NextTodo())`)
	s.run("s4", "deploy-site", `mem.Drop("deploy-site", "no")`)
	s.run("s4", "deploy-site", `mem.Done("deploy-site", "built and deployed")`)
	// The States NextTodo reads, and the frames it passes over.
	s.run("s1", "", `mem.Goal("ship it", "ship").Steps("a", "b", "c")`)
	s.run("s1", "", "mem.Ask(\"ship/a\", \"which key?\")\nmem.Wait(\"ship/b\", \"2026-12-01\")")
	s.run("s2", "", `mem.Start("ship/c")`)
	s.run("s1", "", `for _, t := range mem.Frame("ship").Todos { println(t.Id, t.State, t.Doing) }`)
	s.run("s1", "", `mem.PushFrame(mem.Frame("ship"))`)
	s.run("s1", "", `mem.PushFrame(mem.Frame().NextTodo())`)
	s.run("s1", "", `mem.PushFrame(mem.Frame().NextTodo())`)
	s.run("s1", "", `mem.PushFrame(mem.Frame("ship/b"))`)
	s.run("s1", "", `mem.PushFrame(mem.Frame("ship/a/which-key"))`)
	s.run("s1", "", `mem.Recall("shared port")`)
	// A frame of the stack that mem holds closed (an undo, or a rewind
	// that kept the push and lost the pop) is popped without a change.
	s.stacks["s2"] = []string{"fix-flaky/rename-port-flag"}
	s.run("s2", "", `mem.PopFrame("again")`)
	s.stacks["s2"] = []string{"fix-flaky/rename-port-flag"}
	s.run("s2", "", `mem.Done("fix-flaky/rename-port-flag")`)
	// A secret in the name of a frame on the stack is not redacted under
	// the session's feet; the bound root is never pushed; a goal's name
	// keeps a goal's rules.
	s.run("s3", "", `mem.Goal("email John Hunter about the invoice").Push()`)
	s.run("s3", "", `mem.Redact("John Hunter")`)
	s.run("s3", "", `mem.NewFrame("a-b-c-d-e", "five words")`)
	s.run("s3", "", `mem.PopFrame("sent")`)
	s.run("s3", "", `mem.NewFrame("a-b-c-d-e", "five words")`)
	s.run("s1", "", `println(mem.Show())`)

	golden := filepath.Join("testdata", "gnomem", "v2", "frames.golden")
	if os.Getenv("GNOMEM_V2_WRITE") != "" {
		require.NoError(t, os.WriteFile(golden, []byte(s.out.String()), 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	require.Equal(t, string(want), s.out.String())

	rebuilt, err := openMemRealm(newBase(), t.TempDir())
	require.NoError(t, err)
	defer rebuilt.close()
	rebuilt.clock = s.r.clock
	for _, c := range s.calls {
		require.NoError(t, rebuilt.applyNow(c.Ops, c.At))
	}
	s.check(rebuilt, "the rebuild")
	for _, code := range []string{`println(mem.Show())`, `println(mem.JSON())`, `println(mem.Show("fix-flaky"))`, `println(mem.Show("fix-flaky/reproduce"))`} {
		live, err := s.r.run(code, "s1")
		require.NoError(t, err, live.Output)
		again, err := rebuilt.run(code, "s1")
		require.NoError(t, err, again.Output)
		require.Equal(t, live.Output, again.Output, code)
	}
}
