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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The tests here are the mistakes Qwen made in the real-model scenarios
// (testdata/scenarios), each of which cost a failed call and a round trip.

func runErr(t *testing.T, h *Host, name, code string) *RunResult {
	t.Helper()
	res, err := h.Run(context.Background(), RunRequest{Project: "/proj", Session: "s1", Name: name, Code: code, Req: "r"})
	require.NoError(t, err, code)
	require.Error(t, res.Err, code)
	return res
}

func TestUsedPackagesAreImported(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	// The first prime-sum run: a loop, then fmt.Println, and no import.
	res := run(t, h, "", `var sum int64
var count int
for n := 2; count < 10; n++ {
	isPrime := true
	for d := 2; d*d <= n; d++ {
		if n%d == 0 {
			isPrime = false
			break
		}
	}
	if isPrime {
		sum += int64(n)
		count++
	}
}
fmt.Println(sum)`)
	require.Equal(t, "129\n", res.Output)
	require.Contains(t, res.Notes, "imported fmt for you")

	res = run(t, h, "", `strings.ToUpper("a") + strconv.Itoa(count)`)
	require.Equal(t, `"A10"`, res.Value)
	require.Contains(t, res.Notes, "imported strings, strconv for you")

	// Imported once, the package stays bound: no note the second time.
	res = run(t, h, "", `fmt.Sprint(count)`)
	require.Equal(t, `"10"`, res.Value)
	require.NotContains(t, strings.Join(res.Notes, "\n"), "imported")
}

func TestAutoImportsAreJournaled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h1 := newTestHost(t, dir)
	persist := true
	_, _, err := h1.New(context.Background(), NewRequest{Project: "/proj", Name: "words", Persist: &persist})
	require.NoError(t, err)
	run(t, h1, "words", `func shout(s string) string { return strings.ToUpper(s) + "!" }`)
	require.NoError(t, h1.Close())

	// Replay must bind strings again, or shout's file would not compile.
	h2 := newTestHost(t, dir)
	require.Equal(t, `"HI!"`, run(t, h2, "words", `shout("hi")`).Value)
}

func TestAutoImportLeavesDeclaredNamesAlone(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	// A VM's own fmt, and one the input declares, are not packages.
	run(t, h, "", `type T struct{ N int }
var fmt = T{3}`)
	res := run(t, h, "", `fmt.N`)
	require.Equal(t, "3", res.Value)
	require.Empty(t, res.Notes)

	res = run(t, h, "", `func f() int {
	strings := T{4}
	return strings.N
}
f()`)
	require.Equal(t, "4", res.Value)
	require.Empty(t, res.Notes)

	// A named import is used by its name; fmt is still imported by itself.
	res = run(t, h, "", `import u "strings"
u.ToLower("A")`)
	require.Equal(t, `"a"`, res.Value)
	require.Empty(t, res.Notes)
}

func TestImportByName(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]string{
		"fmt": "fmt", "strings": "strings", "rand": "math/rand", "utf8": "unicode/utf8",
		"sha256": "crypto/sha256", "hex": "encoding/hex", "url": "net/url",
	} {
		got, ok := importByName(name)
		require.True(t, ok, name)
		require.Equal(t, want, got, name)
	}
	for _, name := range []string{"unsafe", "os", "json", "reflect", "sync", "nosuch"} {
		_, ok := importByName(name)
		require.False(t, ok, name)
	}
}

func TestAssignmentToNewNamesDeclares(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	// The resume-checklist run: a type, then tasks = ..., then a func.
	run(t, h, "", `type task struct {
	id   int
	desc string
	done bool
}

tasks = []task{
	{1, "LICENSE", false},
	{2, ".gitignore", false},
}

func pending() []int {
	var out []int
	for _, t := range tasks {
		if !t.done {
			out = append(out, t.id)
		}
	}
	return out
}`)
	require.Equal(t, "2", run(t, h, "", `len(pending())`).Value)

	// Once declared, = assigns.
	run(t, h, "", `tasks = tasks[:1]`)
	require.Equal(t, "1", run(t, h, "", `len(tasks)`).Value)

	// Several names, a blank among them, and a later = in the same input.
	res := run(t, h, "", `a, _, b = 1, 2, 3
a = a + b
a`)
	require.Equal(t, "4", res.Value)

	// A name the input declares is assigned, not declared again.
	res = run(t, h, "", `var n int
n = 5
n`)
	require.Equal(t, "5", res.Value)

	// A right side that uses the name needs it to exist already.
	res = runErr(t, h, "", `m = append(m, 1)`)
	require.Contains(t, res.Err.Error(), "name m not declared")
}

func TestTrailingPrintShowsNoValue(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	res := run(t, h, "", `x := 3682913
fmt.Println(x)`)
	require.Empty(t, res.Value) // not "8, undefined"
	require.Equal(t, "3682913\n", res.Output)

	res = run(t, h, "", `println("hi")`)
	require.Empty(t, res.Value)
	require.Equal(t, "hi\n", res.Output)
}

// A lone expression before the end is shown in its place, not dropped: a
// model that ended with `waits` then `total` was shown the total alone and
// made the terms up (QA, 2026-10-30). A call there stays a statement, its
// results dropped as Go drops them.
func TestLoneExpressionsAreShownInPlace(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `waits := []int{200, 400}
total := 600`)
	shown := run(t, h, "", `waits`).Value
	require.NotEmpty(t, shown)

	res := run(t, h, "", `println("waits:")
waits
println("total:")
total`)
	require.Equal(t, "waits:\n"+shown+"\ntotal:\n", res.Output)
	require.Equal(t, "600", res.Value)

	res = run(t, h, "", `func two() int { return 2 }
two()
two() + 1`)
	require.Empty(t, res.Output)
	require.Equal(t, "3", res.Value)

	// An error after a lone expression names its own line.
	res = runErr(t, h, "", `waits
for i := 0; i < 1; i++ { nope() }`)
	require.Contains(t, res.Err.Error(), ":2:")
}

func TestNilRendersAsNil(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	require.Equal(t, "nil", run(t, h, "", `error(nil)`).Value)
	require.Equal(t, `"x", nil`, run(t, h, "", `func f() (string, error) { return "x", nil }
f()`).Value)
}

func TestHintNamesTheVMThatDeclaresAName(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	persist := true
	_, _, err := h.New(context.Background(), NewRequest{Project: "/proj", Name: "tasks", Persist: &persist})
	require.NoError(t, err)
	run(t, h, "tasks", `var Tasks []string
func Show() string { return strings.Join(Tasks, "\n") }`)

	// The model forgot name: "tasks" and ran in its scratch VM.
	res := runErr(t, h, "", `Show()`)
	require.Contains(t, res.Notes, `Show is declared in VM tasks, not in this one: run this with name "tasks"`)

	// The same from the journal, once tasks is out of memory.
	require.NoError(t, h.Drop(context.Background(), "/proj", "tasks", "evict"))
	res = runErr(t, h, "", `Tasks = append(Tasks, "x")`)
	require.Contains(t, strings.Join(res.Notes, "\n"), `Tasks is declared in VM tasks`)

	// A first assignment declares a name, but not one another VM has: the
	// model meant that VM's, and a scratch copy would hide the mistake.
	res = runErr(t, h, "", `Tasks = []string{"y"}`)
	require.Contains(t, strings.Join(res.Notes, "\n"), `Tasks is declared in VM tasks`)
	require.Equal(t, `"x"`, run(t, h, "tasks", `strings.Join(Tasks, ",") + "x"`).Value)
}

func TestHintSaysAVMNameIsNotAValue(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	persist := true
	_, _, err := h.New(context.Background(), NewRequest{Project: "/proj", Name: "memory", Persist: &persist})
	require.NoError(t, err)
	run(t, h, "memory", `Facts := map[string]string{}`)
	res := runErr(t, h, "memory", `for k := range memory.Facts {
	println(k)
}`)
	require.Contains(t, strings.Join(res.Notes, "\n"), "memory is this VM's name, not a value in it")
}

func TestHintListsAStructsFields(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `type task struct {
	id   int
	desc string
	done bool
}
var tasks = []task{{1, "a", false}}`)
	res := runErr(t, h, "", `t0 := tasks[0]
println(t0.ID)`)
	require.Contains(t, res.Notes, "task has no field ID; its fields are id, desc, done")
}
