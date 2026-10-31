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
	"strconv"
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
	// holding answers Redact's question, as gnosh's journal would.
	holding func(string, []memOp) ([]string, []string, bool)
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

func (s *memStory) run(who, code string) {
	call, err := s.r.commitNext(s.r.call(code, memContext{caller: who, holding: s.holding}))
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

// TestMemGrammars is the golden list of the names and waits mem takes, and
// of what it says to each it refuses (MEMPLAN §12):
// testdata/gnomem/v2/grammar.golden, which GNOMEM_V2_WRITE=1 rewrites. A
// fact's and a rule's given name follow one grammar, and a todo's name is
// made from its task; a wait is a day written 2026-12-01, or words, and a
// day written any other way is refused, naming today; a wait for the next
// session is none.
func TestMemGrammars(t *testing.T) {
	s := newMemStory(t)
	for _, name := range []string{
		"cmd/test", "a", "x1", "2fa/setup", "api/rate-limit", "models/qwen3-27b", "a/b/c/d", strings.Repeat("a", 64),
		"", "Cmd/Test", "CMD", "cmd test", "cmd.test", "cmd_test", "cmd//test", "/cmd", "cmd/", "-cmd", "cmd-",
		"cmd--test", "123", "1/cmd", "café/menu", "naïve", "cmd\ttest", "\xff\xfe", "日本", strings.Repeat("a", 65),
	} {
		s.run("s1", `mem.Fact(`+strconv.Quote(name)+`, "x")`)
	}
	s.run("user", `mem.Rule("No-Emoji", "no emoji in commit messages")`)
	s.run("user", `mem.Rule("style/emoji", "no emoji in commit messages")`)
	// A todo's name is made from its task: filler words dropped, at most 4
	// words and 32 bytes, a hyphenated word kept whole, a key skipped, and
	// -2 when the name is taken; a task an open todo has is not given twice.
	for _, task := range []string{
		"Ship It", "ship it!", "ship/it", "ship-it", "Ship-It", "page the on-call engineer",
		"rotate the key sk-live-4f9c2b3a8d7e now", "which Go file declares the most functions?", "日本", "",
	} {
		s.run("s1", `mem.Todo(`+strconv.Quote(task)+`)`)
	}
	s.run("s1", `mem.Todo("renew the cert")`)
	for _, wait := range []string{
		"2026-12-01", "PR #6399 merged", "the user signs off", "",
		"Dec 1", "December 1st", "tomorrow", "next week", "in 2 days", "+3d", "12/01", "1/12/2026",
		"until 2026-12-01", "Monday", "eod", "2026-02-30", "2026-13-01", "2026-10-01", "renew-cert", "ship-it",
		"next conversation", "PR #6399 merged", "until next time we talk", "next time CI is green", "the next release",
	} {
		s.run("s1", `mem.Wait("renew-cert", `+strconv.Quote(wait)+`)`)
	}

	golden := filepath.Join("testdata", "gnomem", "v2", "grammar.golden")
	if os.Getenv("GNOMEM_V2_WRITE") != "" {
		require.NoError(t, os.WriteFile(golden, []byte(s.out.String()), 0o644))
	}
	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	require.Equal(t, string(want), s.out.String())
}

// TestMemStory runs a story through every call of gnomem v2, and checks
// its transcript against testdata/gnomem/v2/story.golden (GNOMEM_V2_WRITE=1
// rewrites it). Then it rebuilds the mem from the story's Ops alone, as
// gnosh rebuilds from its journal, and checks that the rebuilt mem shows
// and answers JSON exactly as the live one.
func TestMemStory(t *testing.T) {
	s := newMemStory(t)
	s.run("s1", `println(mem.Show())`)
	s.run("s1", `r := mem.Todo("release v2"); r.Todo("bump the version"); r.Todo("tag it"); r.Todo("CI is green")`)
	s.run("s1", `mem.Done("release-v2/bump-version", "now 2.0.0")`)
	s.run("s1", `mem.Ask("release-v2/tag", "which key signs the tag?")`)
	s.run("s1", `mem.Done("release-v2/tag", "tagged")`)
	s.run("s1", `mem.Done("release-v2/tag/which-key-signs-tag", "the YubiKey")`)
	s.run("s1", `mem.Note("release-v2/tag", "the tag needs the release key")`)
	s.run("s1", `mem.Done("Release-v2/tag", "tagged")`)
	s.run("s1", `mem.Done("2", "tagged")`)
	s.run("s1", `mem.Todo("Release v2")`)
	s.run("s1", `mem.Todo("renew the TLS cert").Wait("Dec 1")`)
	s.run("s1", `mem.Todo("renew the TLS cert").Wait("2026-12-01")`)
	s.run("s1", `mem.Todo("bump the gno pin").Wait("PR #6399 merged")`)
	s.run("s2", `mem.Start("release-v2/tag")`)
	s.run("s1", `println(mem.Next("release-v2"))`)
	s.run("s1", `println(mem.Show())`)
	s.run("s1", `mem.Fact("cmd/test", "go test ./...")`)
	s.run("s1", `mem.Fact("cmd/build", "make build")`)
	s.run("s1", `mem.Fact("cmd/test", "go test -race ./...")`)
	s.run("s1", `mem.Fact("cmd/tests", "go test ./internal/...")`)
	s.run("s1", `mem.Fact("Cmd/Test", "x")`)
	s.run("user", `mem.Rule("push-after-landing", "push gnosh main to GitHub only after land-branch.sh prints landed:")`)
	s.run("s1", `mem.Drop("bump-gno-pin", "the PR was closed")`)
	s.run("s1", `mem.Reopen("bump-gno-pin")`)
	s.run("s1", `mem.Done("release-v2/tag", "tagged v2.0.0")`)
	s.run("s2", `mem.Done("release-v2/tag", "tagged v2.0.0")`)
	s.run("s1", `mem.Done("release-v2/ci-green", "go test ./... → ok")`)
	s.run("s1", `mem.Done("release-v2", "shipped")`)
	s.run("s1", `println(mem.Show())`)
	s.run("s1", `println(mem.Show("release-v2"))`)
	s.run("s1", `println(mem.Show("cmd"))`)
	s.run("s1", `mem.Thought("tag-needs-release-key")`)
	s.run("s1", `mem.Frame("release-v2")`)
	s.run("s1", `mem.Frame("release-v2/tag")`)
	s.run("s1", `mem.Forget("release-v2")`)
	s.run("s1", `tea := mem.Todo("make tea"); tea.Todo("boil water"); tea.Todo("pour water")`)
	s.run("s1", `mem.Drop("make-tea", "no kettle")`)
	s.run("s1", `mem.Todo("make tea")`)
	s.run("s1", `mem.Forget("make-tea-2")`)
	// A long task: a refusal and a drop name a few todos and count the
	// rest, and an outline folds a long run of closed todos.
	s.run("s1", `p := mem.Todo("port the tests to gno"); for _, n := range []string{"arith", "bytes", "crypto", "errors", "fmt", "io", "math", "sort", "strings", "time"} { p.Todo(n) }`)
	s.run("s1", `mem.Done("port-tests-gno", "all ported")`)
	s.run("s1", `for _, n := range []string{"arith", "bytes", "crypto", "errors", "fmt", "io", "math"} { mem.Done("port-tests-gno/"+n, "ported") }`)
	s.run("s1", `println(mem.Show("port-tests-gno"))`)
	s.run("s1", `d := mem.Todo("tidy the docs"); for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} { d.Todo(n) }`)
	s.run("s1", `mem.Drop("tidy-docs", "moved to the docs session")`)
	// A question goes with its todo; a task is not a name, and a failed
	// input says which of what it printed is not in mem.
	s.run("s1", `mem.Todo("rotate the staging key").Ask("which day suits you?")`)
	s.run("s1", `mem.Todo("ping the team"); mem.Ask("ping the team", "who is on call?")`)
	// The real-model pass's slips: a name given beside the task, a name made
	// of all a task's words, and a fact before a failure.
	s.run("s1", `mem.Todo("make go test ./... pass", "until-green")`)
	s.run("s1", `mem.Todo("which Go file declares the most functions?"); mem.Note("which-go-file-declares-the-most-functions", "b.go has 5")`)
	s.run("s1", `mem.Fact("cmd/deploy", "make ship ENV=prod"); mem.Note("qa", "Lin does QA")`)
	s.holding = func(text string, _ []memOp) ([]string, []string, bool) {
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
		require.NoError(t, rebuilt.applyNow(c.Ops, c.At))
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
