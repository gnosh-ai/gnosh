// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// memStory runs inputs against a mem, as sessions and the user, on a clock
// that moves a minute per input, and keeps a transcript of what each input
// printed: the receipts and refusals a model reads.
type memStory struct {
	t     *testing.T
	r     *memRealm
	now   time.Time
	out   strings.Builder
	calls []memCall
	// holding answers Redact's question, as klash's journal would.
	holding func(string) ([]string, []string, bool)
}

func newMemStory(t *testing.T) *memStory {
	s := &memStory{t: t, now: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)}
	r, err := openMemRealm(newBase(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { r.close() })
	r.clock = func() time.Time { return s.now }
	s.r = r
	return s
}

func (s *memStory) run(who, code string, imports ...string) {
	call, err := s.r.call(code, memContext{caller: who, holding: s.holding}, imports...)
	s.out.WriteString("> " + who + ": " + code + "\n" + call.Output)
	if err != nil {
		s.out.WriteString("error: " + err.Error() + "\n")
	} else if len(call.Ops) > 0 {
		s.calls = append(s.calls, call)
	}
	s.check(s.r, code)
	s.now = s.now.Add(time.Minute)
}

// check fails the test if mem r disagrees with what apply keeps.
func (s *memStory) check(r *memRealm, after string) {
	s.t.Helper()
	check, err := r.run(`print(mem.Check())`, "klash")
	require.NoError(s.t, err)
	require.Empty(s.t, check.Output, "after %s", after)
}

// TestMemStory runs a story through every call of gnomem v2, and checks
// its transcript against testdata/gnomem/v2/story.golden (GNOMEM_V2_WRITE=1
// rewrites it). Then it rebuilds the mem from the story's Ops alone, as
// klash rebuilds from its journal, and checks that the rebuilt mem shows
// and answers JSON exactly as the live one.
func TestMemStory(t *testing.T) {
	s := newMemStory(t)
	s.run("s1", `println(mem.Show())`)
	s.run("s1", `mem.Goal("release v2").Steps("bump the version", "tag v2.0.0", "CI is green")`)
	s.run("s1", `mem.Done("release-v2/1", "now 2.0.0")`)
	s.run("s1", `mem.Ask("release-v2/2", "which key signs the tag?")`)
	s.run("s1", `mem.Done("release-v2/2", "tagged")`)
	s.run("s1", `mem.Done("release-v2/2/1", "the YubiKey")`)
	s.run("s1", `mem.Note("release-v2/2", "the tag needs the release key")`)
	s.run("s1", `mem.Done("Release-v2/2", "tagged")`)
	s.run("s1", `mem.Done("2", "tagged")`)
	s.run("s1", `mem.Goal("Release v2")`)
	s.run("s1", `mem.Goal("renew the gno.shop TLS cert", "renew-cert").Wait("Dec 1")`)
	s.run("s1", `mem.Goal("renew the gno.shop TLS cert", "renew-cert").Wait("2026-12-01")`)
	s.run("s1", `mem.Goal("bump the gno pin").Wait("PR #6399 merged")`)
	s.run("s2", `mem.Start("release-v2/2")`)
	s.run("s1", `println(mem.Next("release-v2"))`)
	s.run("s1", `println(mem.Show())`)
	s.run("s1", `mem.Fact("cmd/test", "go test ./...")`)
	s.run("s1", `mem.Fact("cmd/build", "make build")`)
	s.run("s1", `mem.Fact("cmd/test", "go test -race ./...")`)
	s.run("s1", `mem.Fact("cmd/tests", "go test ./internal/...")`)
	s.run("s1", `mem.Fact("Cmd/Test", "x")`)
	s.run("user", `mem.Rule("push-after-landing", "push klash main to GitHub only after land-branch.sh prints landed:")`)
	s.run("s1", `mem.Drop("bump-gno-pin", "the PR was closed")`)
	s.run("s1", `mem.Reopen("bump-gno-pin")`)
	s.run("s1", `mem.Done("release-v2/2", "tagged v2.0.0")`)
	s.run("s1", `mem.Done("release-v2/3", "go test ./... → ok")`)
	s.run("s1", `mem.Done("release-v2", "shipped")`)
	s.run("s1", `println(mem.Show())`)
	s.run("s1", `println(mem.Show("release-v2"))`)
	s.run("s1", `println(mem.Show("cmd"))`)
	s.run("s1", `mem.Forget("release-v2")`)
	s.run("s1", `mem.Goal("make tea").Steps("boil water", "pour water")`)
	s.run("s1", `mem.Drop("make-tea", "no kettle")`)
	s.run("s1", `mem.Goal("make tea")`)
	s.run("s1", `mem.Forget("make-tea-2")`)
	// A long task: a refusal and a drop name a few steps and count the
	// rest, and an outline folds a long run of closed steps.
	s.run("s1", `mem.Goal("port the tests to gno").Steps("arith", "bytes", "crypto", "errors", "fmt", "io", "math", "sort", "strings", "time")`)
	s.run("s1", `mem.Done("port-tests-gno", "all ported")`)
	s.run("s1", `for i := 1; i <= 7; i++ { mem.Done("port-tests-gno/"+strconv.Itoa(i), "ported") }`, `"strconv"`)
	s.run("s1", `println(mem.Show("port-tests-gno"))`)
	s.run("s1", `mem.Goal("tidy the docs").Steps("a", "b", "c", "d", "e", "f", "g")`)
	s.run("s1", `mem.Drop("tidy-docs", "moved to the docs session")`)
	s.holding = func(text string) ([]string, []string, bool) {
		if text == "sk-live-4f9c2b" {
			return []string{"bump-gno-pin"}, []string{"item"}, false
		}
		return nil, nil, false
	}
	s.run("s1", `mem.Note("bump-gno-pin", "the token is sk-live-4f9c2b")`)
	s.run("s1", `mem.Redact("sk-live-4f9c2b")`)
	s.run("s1", `mem.Redact("nothing")`)
	s.now = time.Date(2026, 12, 2, 9, 0, 0, 0, time.UTC)
	s.run("s1", `println(mem.Show())`)
	s.run("s1", `println(mem.JSON("bump-gno-pin"))`)

	golden := filepath.Join("testdata", "gnomem", "v2", "story.golden")
	if os.Getenv("GNOMEM_V2_WRITE") != "" {
		require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o755))
		require.NoError(t, os.WriteFile(golden, []byte(s.out.String()), 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	require.Equal(t, string(want), s.out.String())

	// The same mem, rebuilt from the Ops alone.
	rebuilt, err := openMemRealm(newBase(), t.TempDir())
	require.NoError(t, err)
	defer rebuilt.close()
	rebuilt.clock = s.r.clock
	for _, c := range s.calls {
		require.NoError(t, rebuilt.apply(c.Ops, c.At))
	}
	s.check(rebuilt, "the rebuild")
	for _, code := range []string{`println(mem.Show())`, `println(mem.JSON())`, `println(mem.Show("bump-gno-pin"))`} {
		live, err := s.r.run(code, "s1")
		require.NoError(t, err, live.Output)
		again, err := rebuilt.run(code, "s1")
		require.NoError(t, err, again.Output)
		require.Equal(t, live.Output, again.Output, code)
	}
}
