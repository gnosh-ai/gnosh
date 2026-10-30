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
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// klashlibs/fmt formats with Go's fmt; each case is what Go prints.
func TestFmtFormatsAsGoDoes(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `type T struct {
	N int
	S string
}
type P struct {
	Name string
	Tags []string
	Next *P
}
type Shout string
func (s Shout) String() string { return string(s) + "!" }`)
	for code, want := range map[string]string{
		`fmt.Sprintf("%-6s|%6s|", "ab", "cd")`:                                `"ab    |    cd|"`,
		`fmt.Sprintf("%5.2f %e %g", 3.14159, 1234.5, 0.000012)`:               `" 3.14 1.234500e+03 1.2e-05"`,
		`fmt.Sprintf("%x %X %o %b %08b %c %U", 255, 255, 8, 5, 5, 'G', 'G')`:  `"ff FF 10 101 00000101 G U+0047"`,
		`fmt.Sprintf("%q %v %d", "hi", true, -7)`:                             `"\"hi\" true -7"`,
		`fmt.Sprintf("%v|%+v|%#v", T{1, "x"}, T{1, "x"}, T{1, "x"})`:          `"{1 x}|{N:1 S:x}|repl.T{N:1, S:\"x\"}"`,
		`fmt.Sprint([]int{1, 2}, map[string]int{"b": 2, "a": 1}, &T{3, "y"})`: `"[1 2] map[a:1 b:2] &{3 y}"`,
		`fmt.Sprintf("%v", []T{{1, "a"}, {2, "b"}})`:                          `"[{1 a} {2 b}]"`,
		`fmt.Sprintf("%3d|%x", []int{1, 22}, []int{10, 255})`:                 `"[  1  22]|[a ff]"`,
		`fmt.Sprintf("%+v", P{"a", []string{"x"}, &P{Name: "b"}})`:            `"{Name:a Tags:[x] Next:&{Name:b Tags:[] Next:<nil>}}"`,
		`fmt.Sprintf("%s %x %q", []byte("hi"), []byte("hi"), []byte("hi"))`:   `"hi 6869 \"hi\""`,
		`fmt.Sprint(Shout("hey"), " ", errors.New("bad"))`:                    `"hey! bad"`,
		`fmt.Sprintf("%T %T %T %T", 1, "s", T{}, []int{})`:                    `"int string repl.T []int"`,
		`fmt.Sprint(nil, []int(nil), map[int]bool{3: true, 1: false})`:        `"<nil> [] map[1:false 3:true]"`,
		`fmt.Sprintf("%d%%", 50)`:                                             `"50%"`,
		`fmt.Sprintf("%[2]d %[1]d", 1, 2)`:                                    `"2 1"`,
		`fmt.Sprintf("%*d|%-*d|", 4, 7, 3, 8)`:                                `"   7|8  |"`,
		`fmt.Sprintf("%d", "x")`:                                              `"%!d(string=x)"`,
		`fmt.Sprintln("a", 1, 2.5) == "a 1 2.5\n"`:                            `true`,
	} {
		require.Equal(t, want, run(t, h, "", code).Value, code)
	}

	res := run(t, h, "", `fmt.Printf("%-4s=%3d\n", "n", 42)`)
	require.Equal(t, "n   = 42\n", res.Output)
}

func TestFmtErrorfWraps(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `base := errors.New("disk full")
other := errors.New("other")
w := fmt.Errorf("saving %s: %w", "notes", base)
two := fmt.Errorf("%w and %w", base, other)`)
	require.Equal(t, `"saving notes: disk full"`, run(t, h, "", `w.Error()`).Value)
	require.Equal(t, "true", run(t, h, "", `errors.Is(w, base)`).Value)
	require.Equal(t, "true", run(t, h, "", `errors.Unwrap(w) == base`).Value)
	require.Equal(t, `"disk full and other"`, run(t, h, "", `two.Error()`).Value)
	require.Equal(t, "true", run(t, h, "", `errors.Is(two, other)`).Value)
	require.Equal(t, "false", run(t, h, "", `errors.Is(fmt.Errorf("plain %v", base), base)`).Value)
}

func TestFmtRefusesAHugeWidth(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	res := runErr(t, h, "", `fmt.Sprintf("%999999999d", 1)`)
	require.Contains(t, res.Err.Error(), "fmt: a width or precision over 65536")
	res = runErr(t, h, "", `fmt.Sprintf("%*d", 1<<30, 1)`)
	require.Contains(t, res.Err.Error(), "is over 65536")
}

func TestFmtChargesPerElement(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	run(t, h, "", `xs := make([]int, 100)
var sink string
sink = fmt.Sprint(0)`) // the first use of fmt imports it
	one := run(t, h, "", `sink = fmt.Sprint(1)`).Gas
	many := run(t, h, "", `sink = fmt.Sprint(xs)`).Gas
	require.GreaterOrEqual(t, many-one, int64(100*fmtElemGas))
}

// Go's fmt uses a String or Error method only for %v, %s, %q, %x and %X;
// %p would print an address, which differs on every rebuild.
func TestFmtMethodsAndAddresses(t *testing.T) {
	t.Parallel()
	h := newTestHost(t, t.TempDir())
	require.Equal(t, `"3 March March time.Month"`, run(t, h, "", `fmt.Sprintf("%d %v %s %T", time.March, time.March, time.March, time.March)`).Value)
	require.Contains(t, runErr(t, h, "", `fmt.Sprintf("%p", []int{1})`).Err.Error(), "%p prints an address")
}

// A print through a VM's own fmt is a call like any other: kept.
func TestFmtOfTheVMIsNotTrusted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h1 := newTestHost(t, dir)
	persist := true
	_, _, err := h1.New(context.Background(), NewRequest{Project: "/proj", Name: "log", Persist: &persist})
	require.NoError(t, err)
	run(t, h1, "log", `var lines []string
type L struct{}
func (L) Println(a ...any) { lines = append(lines, "x") }
var fmt L`)
	require.True(t, run(t, h1, "log", `fmt.Println("hi")`).Journaled)
	require.NoError(t, h1.Close())
	h2 := newTestHost(t, dir)
	require.Equal(t, "1", run(t, h2, "log", `len(lines)`).Value)
}
