// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovmtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/gnovm"
)

// These tests hold the tools to what spec/gnosh/machines.md section 9 and
// spec/gnosh/integration-contract.md section 5 say of them. Those that run
// code use the client's own host in this process (newLazy); those that are
// about what the tool does around a call put a scripted client in its
// place.

// What differs from one run to the next in a result: the times, the gas a
// call used and the memory it holds.
var (
	vmStatusRe  = regexp.MustCompile(`· \S+ of (\S+) gas \(≈\S+ at (\S+) ns/gas; ran \S+\) · memory \S+ MB of (\S+) MB ·`)
	memStatusRe = regexp.MustCompile(`· mem · ran \S+ ·`)
)

// steady is a result with what differs from run to run made the same.
func steady(s string) string {
	s = vmStatusRe.ReplaceAllString(s, "· GAS of $1 gas (≈T at $2 ns/gas; ran T) · memory M MB of $3 MB ·")
	return memStatusRe.ReplaceAllString(s, "· mem · ran T ·")
}

// A run in the session's scratch VM shows the value, then the output under
// its heading, then one status line, then the notes: the exact text of
// machines.md section 9.5. State stays from one call to the next.
func TestScratchResultForms(t *testing.T) {
	run := toolsOver(newLazy(t), Options{}).Tool(RunToolName)
	scratch := gnovm.ScratchName("session-1")

	resp := call(t, run, `{"code": "x := 20\nprintln(\"hi\")\nx + 22"}`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "42\n"+
		"output:\n"+
		"hi\n"+
		"ok · VM "+scratch+" · GAS of 1G gas (≈T at 1 ns/gas; ran T) · memory M MB of 16.0 MB · kept as entry 1\n"+
		"note: new scratch VM "+scratch+": memory only, it ends with this gnosh process; name a VM with gnovm_new to keep state\n",
		steady(resp.Content))
	require.Nil(t, resp.Meta, "only a call to mem that pushes or pops keeps anything with its result")

	// A value alone, read from the state the call before left; nothing new
	// to keep.
	resp = call(t, run, `{"code": "x * 2"}`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "40\nok · VM "+scratch+" · GAS of 1G gas (≈T at 1 ns/gas; ran T) · memory M MB of 16.0 MB · nothing to keep\n", steady(resp.Content))

	// Output alone has no heading; printing changes nothing in the VM.
	resp = call(t, run, `{"code": "println(\"only output\")"}`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "only output\nok · VM "+scratch+" · GAS of 1G gas (≈T at 1 ns/gas; ran T) · memory M MB of 16.0 MB · nothing to keep\n", steady(resp.Content))

	// A change is kept as the next entry.
	resp = call(t, run, `{"code": "x++"}`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "ok · VM "+scratch+" · GAS of 1G gas (≈T at 1 ns/gas; ran T) · memory M MB of 16.0 MB · kept as entry 2\n", steady(resp.Content))

	// The call's own gas limit shows in the status line.
	resp = call(t, run, `{"code": "x", "gas": 5e6}`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "21\nok · VM "+scratch+" · GAS of 5M gas (≈T at 1 ns/gas; ran T) · memory M MB of 16.0 MB · nothing to keep\n", steady(resp.Content))

	// A program runs whole in a VM of its own, thrown away after.
	resp = call(t, run, `{"code": "package main\n\nfunc main() { println(\"from a program\") }"}`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "from a program\nok · a fresh program VM · GAS of 1G gas (≈T at 1 ns/gas; ran T) · memory M MB of 16.0 MB · thrown away after\n", steady(resp.Content))
}

// A failure of the model's code is a result the model reads, never a
// failure of the harness: nothing ran (a refusal, code that does not
// parse), or nothing was kept (a panic, a limit), each under its own
// prefix, with the call that gets past a limit.
func TestScratchFailures(t *testing.T) {
	run := toolsOver(newLazy(t), Options{}).Tool(RunToolName)
	scratch := gnovm.ScratchName("session-1")
	failed := "failed · VM " + scratch + " · GAS of 1G gas (≈T at 1 ns/gas; ran T) · memory M MB of 16.0 MB · nothing kept\n"

	// Code that does not parse: nothing ran.
	resp := call(t, run, `{"code": "func ("}`)
	require.True(t, resp.IsError)
	require.True(t, strings.HasPrefix(resp.Content, "ERROR — nothing ran: syntax error: "), resp.Content)
	require.NotContains(t, resp.Content, "\n", "a call that did not run has no status line")

	// Code that does not compile: it parsed, so the VM was made, and
	// nothing was kept.
	resp = call(t, run, `{"code": "undefinedName + 1"}`)
	require.True(t, resp.IsError)
	require.True(t, strings.HasPrefix(resp.Content, "ERROR — nothing from this call was kept: "), resp.Content)
	require.Contains(t, resp.Content, "undefinedName")
	require.Contains(t, steady(resp.Content), failed)

	// A panic at run time, with what the code printed before it.
	resp = call(t, run, `{"code": "println(\"before\")\npanic(\"boom\")"}`)
	require.True(t, resp.IsError)
	require.True(t, strings.HasPrefix(resp.Content, "ERROR — nothing from this call was kept: panic: boom\n"), resp.Content)
	require.Contains(t, resp.Content, "\noutput before it stopped:\nbefore\n")
	require.Contains(t, steady(resp.Content), failed)

	// Out of gas: four times the limit is the next thing to try.
	resp = call(t, run, `{"code": "for {}", "gas": 1e6}`)
	require.True(t, resp.IsError)
	require.True(t, strings.HasPrefix(resp.Content, "ERROR — nothing from this call was kept: out of gas\n"+
		"Next: give it more gas, e.g. \"gas\": 4000000, or do less per call.\n"+
		"failed · VM "+scratch+" · "), resp.Content)
	require.Contains(t, resp.Content, " of 1M gas (")

	// Over the memory cap: the cap can be raised, by the name the model
	// knows its scratch VM by.
	resp = call(t, run, `{"code": "var keep [][]byte\nfor i := 0; i < 2000; i++ { keep = append(keep, make([]byte, 10000)) }"}`)
	require.True(t, resp.IsError)
	require.True(t, strings.HasPrefix(resp.Content, "ERROR — nothing from this call was kept: memory cap exceeded\n"+
		"Next: raise the cap with gnovm_new {\"name\": \"scratch\", \"memory_mb\": 32}, or keep less.\n"), resp.Content)

	// A program has no cap to raise: its VM is made for the one call.
	resp = call(t, run, `{"code": "package main\n\nfunc main() {\n\tvar keep [][]byte\n\tfor i := 0; i < 2000; i++ { keep = append(keep, make([]byte, 10000)) }\n}"}`)
	require.True(t, resp.IsError)
	require.True(t, strings.HasPrefix(resp.Content, "ERROR — nothing from this call was kept: memory cap exceeded\n"+
		"Next: keep less in memory; a program VM has the default cap.\n"+
		"failed · a fresh program VM · "), resp.Content)

	// The fixed refusals of the tool itself, before anything is asked of
	// the VMs.
	for input, want := range map[string]string{
		`{}`:                                  "code is required",
		`{"code": "  \n"}`:                    "code is required",
		`{"code": "1", "gas": -5}`:            "gas must be between 1 and 1e11",
		`{"code": "1", "gas": 0.5}`:           "gas must be between 1 and 1e11",
		`{"code": "1", "gas": 2e11}`:          "gas must be between 1 and 1e11",
		`{"code": "1", "gas": 1e400}`:         "gas must be between 1 and 1e11",
		`{"code": "1", "gas": "plenty"}`:      "gas must be a number",
		`{"code": 7}`:                         "code must be a string",
		`{"code": "1", "reset": "sometimes"}`: "reset must be true or false",
		`["code"]`:                            "the arguments must be one JSON object",
	} {
		resp = call(t, run, input)
		require.True(t, resp.IsError, input)
		require.Equal(t, want, resp.Content, input)
	}

	// A VM that is not there: nothing ran.
	resp = call(t, run, `{"name": "nosuch", "code": "1"}`)
	require.True(t, resp.IsError)
	require.Equal(t, "ERROR — nothing ran: there is no VM named nosuch; make it with gnovm_new, or leave out name to use your scratch VM", resp.Content)
}

// Redeclaring a name is refused, and reset starts a memory-only VM over.
func TestScratchReset(t *testing.T) {
	run := toolsOver(newLazy(t), Options{}).Tool(RunToolName)
	scratch := gnovm.ScratchName("session-1")
	resp := call(t, run, `{"code": "func helper() int { return 1 }"}`)
	require.False(t, resp.IsError, resp.Content)

	resp = call(t, run, `{"code": "func helper() int { return 2 }"}`)
	require.True(t, resp.IsError)
	require.Equal(t, "ERROR — nothing ran: helper is already declared in this VM. Assign to a variable with =, or use a new name; to start over, pass reset: true (or use a fresh VM)", resp.Content)

	resp = call(t, run, `{"code": "func helper() int { return 2 }\nhelper()", "reset": true}`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "2\n"+
		"ok · VM "+scratch+" · GAS of 1G gas (≈T at 1 ns/gas; ran T) · memory M MB of 16.0 MB · kept as entry 1\n"+
		"note: reset VM "+scratch+"\n", steady(resp.Content))
}

// A result is cut at 30,000 characters, and says how many more there were.
func TestResultIsCutAt30000(t *testing.T) {
	run := toolsOver(newLazy(t), Options{}).Tool(RunToolName)
	resp := call(t, run, `{"code": "for i := 0; i < 5000; i++ { println(\"0123456789\") }"}`)
	require.False(t, resp.IsError)
	m := regexp.MustCompile(`^((?:0123456789\n)+0?1?2?3?4?5?6?7?8?9?)\n… \((\d+) more characters not shown\)$`).FindStringSubmatch(resp.Content)
	require.NotNil(t, m, "the result is not its first 30,000 characters and a count of the rest:\n…%s", resp.Content[max(0, len(resp.Content)-200):])
	require.Equal(t, 30000, len(m[1]))
	// 5,000 lines of eleven characters, the status line and no note.
	var more int
	fmt.Sscan(m[2], &more)
	require.True(t, more > 25000 && more < 25300, "%d more characters", more)

	// The cut is made between characters, never inside one.
	long := strings.Repeat("a", maxResult-1) + "é" + strings.Repeat("b", 10)
	got := truncate(long)
	require.True(t, utf8.ValidString(got))
	require.Equal(t, strings.Repeat("a", maxResult-1)+"\n… (12 more characters not shown)", got)
	require.Equal(t, strings.Repeat("a", maxResult), truncate(strings.Repeat("a", maxResult)))
}

// Stopping the turn ends a running call at once. What comes back is a
// result the model can read, not a failure of the harness, and the VM is
// as it was.
func TestCancellationMidCall(t *testing.T) {
	run := toolsOver(newLazy(t), Options{}).Tool(RunToolName)
	call(t, run, `{"code": "x := 7"}`)

	ctx, cancel := context.WithCancel(context.Background())
	stop := time.AfterFunc(20*time.Millisecond, cancel)
	defer stop.Stop()
	start := time.Now()
	resp := callAs(t, ctx, "session-1", run, "call-cancelled", `{"code": "x = 8\nfor {}", "gas": 1e11}`)
	took := time.Since(start)
	require.True(t, ctx.Err() != nil, "the call came back before it was stopped, after %s:\n%s", took, resp.Content)
	require.True(t, took < 10*time.Second, "a stopped call took %s to come back", took)
	require.True(t, resp.IsError)
	require.True(t, strings.HasPrefix(resp.Content, "ERROR — nothing from this call was kept: cancelled\n"), resp.Content)
	require.Contains(t, resp.Content, "· nothing kept\n")

	resp = call(t, run, `{"code": "x"}`)
	require.False(t, resp.IsError, resp.Content)
	require.True(t, strings.HasPrefix(resp.Content, "7\n"), resp.Content)
}

// A named VM is made with gnovm_new, changed with it, listed by gnovm_list
// and dropped by gnovm_drop, each in the words of machines.md sections 9.2
// to 9.4.
func TestNamedVM(t *testing.T) {
	at := time.Date(2026, 10, 30, 9, 12, 44, 0, time.UTC)
	l := gnovm.NewLazy(gnovm.ClientOptions{InProcess: true, Options: gnovm.Options{
		Dir: t.TempDir(),
		Now: func() time.Time { return at },
		Probe: func() (gnovm.HostMemory, error) {
			return gnovm.HostMemory{Total: 64000 << 20, Available: 12034 << 20, Source: "cgroup"}, nil
		},
	}})
	t.Cleanup(l.Close)
	ts := toolsOver(l, Options{})
	run, mk, list, drop := ts.Tool(RunToolName), ts.Tool(NewToolName), ts.Tool(ListToolName), ts.Tool(DropToolName)

	resp := call(t, list, `{}`)
	require.False(t, resp.IsError, resp.Content)
	lines := strings.Split(resp.Content, "\n")
	require.Equal(t, "host memory: 12034.0 MB available of 64000.0 MB (cgroup)", lines[0])
	require.True(t, regexp.MustCompile(`^VM budget: \d+\.\d MB, \d+\.\d MB in use \(standard library \d+\.\d MB\)$`).MatchString(lines[1]), lines[1])
	require.Equal(t, "no VMs yet: gnovm_run makes your scratch VM, gnovm_new a named one", lines[2])
	require.Equal(t, "importable: fmt, "+strings.Join(gnovm.ImportablePackages(), ", "), lines[3])
	require.Equal(t, "Built on gno.land (https://gno.land) · built on gnosh (https://gnosh.ai)", lines[4])
	require.Equal(t, []string{""}, lines[5:])

	resp = call(t, mk, `{"name": "tasks"}`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "Made VM tasks with memory_mb 16, gas 1G per call, 1 ns/gas, memory only.", resp.Content)
	resp = call(t, mk, `{"name": "tasks", "memory_mb": 32, "persist": true, "pinned": true, "description": "the release checklist"}`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "VM tasks now has memory_mb 32, gas 1G per call, 1 ns/gas, persistent, pinned.", resp.Content)
	resp = call(t, mk, `{"name": "calc", "gas": 2e6, "ns_per_gas": 2.5, "memory_mb": "8"}`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "Made VM calc with memory_mb 8, gas 2M per call, 2.5 ns/gas, memory only.", resp.Content)

	// What the VMs refuse comes back as a call that did not run.
	for input, want := range map[string]string{
		`{}`:                                 "name is required",
		`{"name": " "}`:                      "name is required",
		`{"name": "tasks", "gas": 1e12}`:     "gas must be between 1 and 1e11",
		`{"name": "tasks", "memory_mb": 16}`: "ERROR — nothing ran: memory_mb can only grow (VM tasks has 32); for less, make a new VM",
		`{"name": "big", "memory_mb": 65}`:   "ERROR — nothing ran: memory_mb must be between 1 and 64",
		`{"name": "Tasks"}`:                  "ERROR — nothing ran: a VM name is a lowercase letter, then up to 31 lowercase letters, digits, - or _",
		`{"name": "mem", "persist": false}`:  "ERROR — nothing ran: mem is your memory, kept by gnosh and made when first used: run gnovm_run with name \"mem\"; it has no settings to change",
	} {
		resp = call(t, mk, input)
		require.True(t, resp.IsError, input)
		require.Equal(t, want, resp.Content, input)
	}

	resp = call(t, run, `{"name": "tasks", "code": "var items = []string{\"tag\"}\nlen(items)"}`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "1\nok · VM tasks · GAS of 1G gas (≈T at 1 ns/gas; ran T) · memory M MB of 32.0 MB · kept as entry 1\n", steady(resp.Content))

	resp = call(t, list, `{}`)
	require.False(t, resp.IsError, resp.Content)
	lines = strings.Split(resp.Content, "\n")
	require.Equal(t, 7, len(lines), resp.Content)
	require.True(t, regexp.MustCompile(`^- calc: memory_mb 8, gas 2M per call, 2\.5 ns/gas, memory only; 0 entries \(0 KB\), 0 gas used, last used never; on disk$`).MatchString(lines[2]), lines[2])
	require.True(t, regexp.MustCompile(`^- tasks: memory_mb 32, gas 1G per call, 1 ns/gas, persistent, pinned; 1 entries \(0 KB\), \S+ gas used, last used 2026-10-30 09:12:44; in memory \(\d+\.\d MB of its cap, \d+\.\d MB counted\) — the release checklist$`).MatchString(lines[3]), lines[3])
	require.True(t, strings.HasPrefix(lines[4], "importable: fmt, "), lines[4])

	// A VM at the largest cap has no more to be given.
	resp = call(t, mk, `{"name": "big", "memory_mb": 64}`)
	require.False(t, resp.IsError, resp.Content)
	resp = call(t, run, `{"name": "big", "gas": 1e11, "code": "var keep [][]byte\nfor i := 0; i < 70; i++ { keep = append(keep, make([]byte, 1000000)) }"}`)
	require.True(t, resp.IsError)
	require.True(t, strings.HasPrefix(resp.Content, "ERROR — nothing from this call was kept: memory cap exceeded\n"+
		"Next: keep less in memory; 64 MB is the most a VM may have.\n"+
		"failed · VM big · "), resp.Content)
	resp = call(t, drop, `{"name": "big", "action": "delete"}`)
	require.False(t, resp.IsError, resp.Content)

	// Evicting keeps the VM's state; it is rebuilt when next used.
	resp = call(t, drop, `{"name": "tasks", "action": "evict"}`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "Evicted VM tasks from memory; it is rebuilt from its journal when next used.", resp.Content)
	resp = call(t, run, `{"name": "tasks", "code": "items[0]"}`)
	require.False(t, resp.IsError, resp.Content)
	require.True(t, strings.HasPrefix(resp.Content, "\"tag\"\n"), resp.Content)
	require.Contains(t, resp.Content, "\nnote: rebuilt VM tasks from 1 journal entries in ")

	// Deleting removes it; the session's own scratch VM goes by "scratch".
	resp = call(t, drop, `{"name": "tasks", "action": "delete"}`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "Deleted VM tasks.", resp.Content)
	resp = call(t, run, `{"name": "tasks", "code": "1"}`)
	require.True(t, resp.IsError)
	require.Equal(t, "ERROR — nothing ran: there is no VM named tasks; make it with gnovm_new, or leave out name to use your scratch VM", resp.Content)
	call(t, run, `{"code": "y := 1"}`)
	resp = call(t, drop, `{"name": "scratch", "action": "evict"}`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "Evicted VM "+gnovm.ScratchName("session-1")+" from memory; it is rebuilt from its journal when next used.", resp.Content)

	for input, want := range map[string]string{
		`{"action": "delete"}`:                 "name is required",
		`{"name": "calc"}`:                     `action must be "evict" or "delete"`,
		`{"name": "calc", "action": "remove"}`: `action must be "evict" or "delete"`,
		`{"name": "gone", "action": "evict"}`:  "ERROR — nothing ran: there is no VM named gone",
		`{"name": "mem", "action": "delete"}`:  "ERROR — nothing ran: mem is your memory: it is never dropped; the user erases from it with /mem forget",
	} {
		resp = call(t, drop, input)
		require.True(t, resp.IsError, input)
		require.Equal(t, want, resp.Content, input)
	}
}

// renderList shows the host, the budget, each VM and why workers ended, as
// machines.md section 9.3 gives the lines.
func TestRenderList(t *testing.T) {
	mbs := func(n float64) int64 { return int64(n * (1 << 20)) }
	got := renderList(gnovm.ListResult{
		Memory:    gnovm.HostMemory{Total: uint64(mbs(64000)), Available: uint64(mbs(12034)), Source: "cgroup"},
		Budget:    mbs(1024),
		Used:      mbs(61.3),
		BaseBytes: mbs(44.9),
		VMs: []gnovm.VMInfo{
			{
				Name:    "tasks",
				Options: gnovm.VMOptions{MemoryMB: 16, Gas: 1e9, NsPerGas: 1, Persist: true, Description: "the release checklist"},
				Entries: 12, JournalSize: 3 << 10, LifetimeGas: 4_200_000,
				LastUsed: time.Date(2026, 10, 30, 9, 12, 44, 0, time.UTC),
				Resident: true, MemUsed: mbs(0.3), Charge: mbs(18.2),
			},
			{Name: "old", Options: gnovm.VMOptions{MemoryMB: 8, Gas: 5e5, NsPerGas: 0.5}, Broken: "replaying entry 3: boom"},
			{Name: "idle", Options: gnovm.VMOptions{MemoryMB: 64, Gas: 1e11, NsPerGas: 100, Persist: true, Pinned: true}, Entries: 2, JournalSize: 900},
		},
	}, []string{"exited", "killed by gnosh: no heartbeat for 11s"})
	require.Equal(t, "host memory: 12034.0 MB available of 64000.0 MB (cgroup)\n"+
		"VM budget: 1024.0 MB, 61.3 MB in use (standard library 44.9 MB)\n"+
		"- tasks: memory_mb 16, gas 1G per call, 1 ns/gas, persistent; 12 entries (3 KB), 4.2M gas used, last used 2026-10-30 09:12:44; in memory (0.3 MB of its cap, 18.2 MB counted) — the release checklist\n"+
		"- old: memory_mb 8, gas 500k per call, 0.5 ns/gas, memory only; 0 entries (0 KB), 0 gas used, last used never; BROKEN: replaying entry 3: boom\n"+
		"- idle: memory_mb 64, gas 100G per call, 100 ns/gas, persistent, pinned; 2 entries (0 KB), 0 gas used, last used never; on disk\n"+
		"the VM worker ended 2 time(s) in this gnosh; last: killed by gnosh: no heartbeat for 11s\n"+
		"importable: fmt, "+strings.Join(gnovm.ImportablePackages(), ", ")+"\n"+
		"Built on gno.land (https://gno.land) · built on gnosh (https://gnosh.ai)\n", got)

	got = renderList(gnovm.ListResult{ProbeErr: "no /proc/meminfo"}, nil)
	require.True(t, strings.HasPrefix(got, "host memory: unknown (no /proc/meminfo)\nVM budget: 0.0 MB, 0.0 MB in use (standard library 0.0 MB)\nno VMs yet: gnovm_run makes your scratch VM, gnovm_new a named one\nimportable: fmt, "), got)
}

// The status line of machines.md section 9.5, for a VM, a program and mem,
// kept, read, failed and thrown away.
func TestStatusLine(t *testing.T) {
	vm := gnovm.RunResult{VM: "scratch-1a2b3c4d", Gas: 1_200_000, GasLimit: 1e9, NsPerGas: 1, Wall: 1400 * time.Microsecond, MemUsed: 314573, MemCap: 16 << 20, Journaled: true, Seq: 7}
	require.Equal(t, "ok · VM scratch-1a2b3c4d · 1.2M of 1G gas (≈1.2ms at 1 ns/gas; ran 1.4ms) · memory 0.3 MB of 16.0 MB · kept as entry 7", statusLine(&vm))
	vm.Journaled = false
	require.Equal(t, "ok · VM scratch-1a2b3c4d · 1.2M of 1G gas (≈1.2ms at 1 ns/gas; ran 1.4ms) · memory 0.3 MB of 16.0 MB · nothing to keep", statusLine(&vm))
	vm.Err = errors.New("boom")
	require.Equal(t, "failed · VM scratch-1a2b3c4d · 1.2M of 1G gas (≈1.2ms at 1 ns/gas; ran 1.4ms) · memory 0.3 MB of 16.0 MB · nothing kept", statusLine(&vm))

	prog := gnovm.RunResult{VM: "(program)", Program: true, Gas: 950, GasLimit: 2_500_000, NsPerGas: 2.5, Wall: 3 * time.Second, MemCap: 16 << 20}
	require.Equal(t, "ok · a fresh program VM · 950 of 2.5M gas (≈2µs at 2.5 ns/gas; ran 3s) · memory 0.0 MB of 16.0 MB · thrown away after", statusLine(&prog))

	mem := gnovm.RunResult{VM: gnovm.MemVM, Wall: 31*time.Millisecond + 400*time.Microsecond, Journaled: true, Seq: 23}
	require.Equal(t, "ok · mem · ran 31ms · kept as entry 23", statusLine(&mem))
	mem.Journaled, mem.Seq = false, 0
	require.Equal(t, "ok · mem · ran 31ms · nothing to keep", statusLine(&mem))
	mem.Err = errors.New("nothing to pop")
	require.Equal(t, "failed · mem · ran 31ms · nothing kept", statusLine(&mem))
}

// renderRun puts a result together in the order of machines.md section
// 9.5: the value and the output (or the error, with what was printed before
// it), then what gnosh adds about frames, then the status line, then the
// notes.
func TestRenderRun(t *testing.T) {
	res := gnovm.RunResult{VM: gnovm.MemVM, Output: "done `x` \"do x\" → did it\n\n", Wall: 12 * time.Millisecond, Journaled: true, Seq: 4, Notes: []string{"mem's store caught up with 2 journal entries", "second"}}
	require.Equal(t, "done `x` \"do x\" → did it\n"+
		"Everything since you entered `x` was removed from your context.\n"+
		"ok · mem · ran 12ms · kept as entry 4\n"+
		"note: mem's store caught up with 2 journal entries\n"+
		"note: second\n", renderRun(&res, "Everything since you entered `x` was removed from your context.\n\n"))

	res = gnovm.RunResult{VM: gnovm.MemVM, Output: "added `y` \"do y\"\n", Wall: time.Millisecond, Err: errors.New("nothing to pop: you are at the root")}
	require.Equal(t, "ERROR — nothing from this call was kept: nothing to pop: you are at the root\n"+
		"output before it stopped:\n"+
		"added `y` \"do y\"\n"+
		"failed · mem · ran 1ms · nothing kept\n", renderRun(&res, ""))

	res = gnovm.RunResult{VM: "v", Value: "3", GasLimit: 1e9, NsPerGas: 1, MemCap: 1 << 20}
	require.Equal(t, "3\nok · VM v · 0 of 1G gas (≈0s at 1 ns/gas; ran 0s) · memory 0.0 MB of 1.0 MB · nothing to keep\n", renderRun(&res, ""))
}

// What the harness is told about the four: their names, in order, the
// earlier gnosh's descriptions word for word, and each argument with its
// name, its type and what the model is told of it. None says it may run
// beside other calls, so the loop runs each alone and in order.
func TestSpecs(t *testing.T) {
	ts := New(Options{})
	want := []struct{ name, description, schema string }{
		{"gnovm_run", runDescription, `{"type":"object","properties":{` +
			`"code":{"type":"string","description":"The code to run."},` +
			`"name":{"type":"string","description":"The VM to run it in. Leave it out for your scratch VM, or, for code with a package clause, a fresh VM."},` +
			`"gas":{"type":"number","description":"Gas limit for this call (default the VM's, 1e9 = about 1 s of work)."},` +
			`"reset":{"type":"boolean","description":"Start a memory-only VM over before running."}},` +
			`"required":["code"],"additionalProperties":false}`},
		{"gnovm_new", newDescription, `{"type":"object","properties":{` +
			`"name":{"type":"string","description":"The VM's name; \"scratch\" for your session's scratch VM."},` +
			`"memory_mb":{"type":"number","description":"Memory cap in MB, 1 to 64; only grows."},` +
			`"gas":{"type":"number","description":"Gas limit per call."},` +
			`"ns_per_gas":{"type":"number","description":"Nanoseconds per gas for this VM, 0.1 to 100."},` +
			`"persist":{"type":"boolean","description":"Keep its state on disk across restarts."},` +
			`"pinned":{"type":"boolean","description":"Never evict it to make room."},` +
			`"description":{"type":"string","description":"What it is for."}},` +
			`"required":["name"],"additionalProperties":false}`},
		{"gnovm_list", listDescription, `{"type":"object","properties":{},"required":[],"additionalProperties":false}`},
		{"gnovm_drop", dropDescription, `{"type":"object","properties":{` +
			`"name":{"type":"string","description":"The VM."},` +
			`"action":{"type":"string","description":"\"evict\" or \"delete\".","enum":["evict","delete"]}},` +
			`"required":["name","action"],"additionalProperties":false}`},
	}
	tools := ts.Tools()
	require.Equal(t, len(want), len(tools))
	for i, w := range want {
		spec := tools[i].Spec()
		require.Equal(t, w.name, spec.Name)
		require.Equal(t, w.description, spec.Description)
		require.Equal(t, w.schema, string(spec.InputSchema))
		require.True(t, json.Valid(spec.InputSchema), w.name)
		_, describes := tools[i].(core.Describer)
		require.True(t, describes, "%s says what a call would do", w.name)
		_, parallel := tools[i].(interface{ Parallel(core.ToolCall) bool })
		require.False(t, parallel, "%s must run alone: no call may run beside gnovm_run", w.name)
		require.True(t, ts.Tool(w.name) == tools[i], w.name)
	}
	require.Nil(t, ts.Tool("gnovm_goals"))
	require.True(t, strings.HasPrefix(runDescription, "Run Go-shaped code in a Gno virtual machine"))
}

// What a call would do, for the harness's permission rules and for the
// person who is asked: one plain line each. Only deleting a VM is something
// a person must be asked about, and its line is the one they were asked
// with before.
func TestDescribe(t *testing.T) {
	ts := New(Options{})
	describe := func(tool, input string) (core.Action, error) {
		t.Helper()
		return ts.Tool(tool).(core.Describer).Describe(core.ToolCall{ID: "c", Name: tool, Input: json.RawMessage(input)}, testEnv("session-1"))
	}
	for _, c := range []struct{ tool, input, summary string }{
		{RunToolName, `{"code": "1 + 1"}`, "Run Gno code"},
		{RunToolName, `{"name": "mem", "code": "mem.Show()"}`, "Run Gno code in mem"},
		{RunToolName, `{"name": "tasks", "code": "len(items)"}`, "Run Gno code in VM tasks"},
		{RunToolName, `{"name": "scratch", "code": "x", "reset": true}`, "Run Gno code in the session's scratch VM, starting the VM over first"},
		{RunToolName, `{"name": "scratch-1a2b3c4d", "code": "x"}`, "Run Gno code in the session's scratch VM"},
		{RunToolName, `{}`, "Run Gno code"},
		{RunToolName, `{"code": 7}`, "Run Gno code"},
		{NewToolName, `{"name": "tasks", "persist": true}`, "Make Gno VM tasks, or change its settings"},
		{NewToolName, `{}`, "Make a Gno VM, or change one's settings"},
		{ListToolName, `{}`, "List this project's Gno VMs"},
		{DropToolName, `{"name": "tasks", "action": "evict"}`, "Evict Gno VM tasks from memory; it keeps its state"},
		{DropToolName, `{"name": "tasks", "action": "delete"}`, "Delete Gno VM tasks and its saved state"},
	} {
		act, err := describe(c.tool, c.input)
		require.NoError(t, err, c.input)
		require.Equal(t, core.ActionOther, act.Kind, c.input)
		require.Equal(t, c.summary, act.Summary, c.input)
		require.Empty(t, act.Paths, c.input)
		require.Equal(t, "", act.Command+act.URL, c.input)
		// Only a delete throws away what cannot be got back, so only a
		// delete says that a person must be asked whatever the rules allow.
		require.Equal(t, strings.HasPrefix(c.summary, "Delete "), act.Confirm, c.input)
	}

	// A delete that could not run says so before anyone is asked about it,
	// in the words Run would use.
	for input, want := range map[string]string{
		`{"action": "delete"}`:             "name is required",
		`{"name": "", "action": "delete"}`: "name is required",
		`{"name": 3, "action": "delete"}`:  "name must be a string",
	} {
		act, err := describe(DropToolName, input)
		require.True(t, err != nil, input)
		require.Equal(t, want, err.Error(), input)
		require.Equal(t, core.ActionOther, act.Kind, input)
		require.Equal(t, "Delete a Gno VM and its saved state", act.Summary, input)
		require.True(t, act.Confirm, input)
	}
	// Nobody is asked about any other call, so whatever is wrong with its
	// arguments is Run's to say, in the words the model has always read.
	for _, input := range []string{`{"name": "tasks"}`, `{"name": "tasks", "action": "rm"}`, `{"action": "evict"}`, `"delete tasks"`, `{"code": 7}`} {
		for _, tool := range []string{RunToolName, NewToolName, ListToolName, DropToolName} {
			act, err := describe(tool, input)
			require.NoError(t, err, tool+" "+input)
			require.Equal(t, core.ActionOther, act.Kind, tool+" "+input)
			require.True(t, act.Summary != "", tool+" "+input)
			require.False(t, act.Confirm, tool+" "+input)
		}
	}

}

// How a gnovm_run call shows to a person: before it runs, its code, whole,
// under the VM's name; after, the same heading over what the model reads.
// The VM's name leads unless it is mem or a scratch VM, and the gas limit
// and reset follow it when the call sets them.
func TestHowACallShows(t *testing.T) {
	ts := New(Options{Project: "/proj", Client: func() (Client, error) {
		return &fakeClient{run: func(context.Context, gnovm.RunRequest) (*gnovm.RunResult, error) {
			return &gnovm.RunResult{VM: "tasks", Value: "3", GasLimit: 4e6, NsPerGas: 1, MemCap: 16 << 20}, nil
		}}, nil
	}})
	run := ts.Tool(RunToolName)
	for _, c := range []struct{ input, lead string }{
		{`{"code": "mem.Show()", "name": "mem"}`, ""},
		{`{"code": "1 + 1"}`, ""},
		{`{"code": "x", "name": "scratch"}`, ""},
		{`{"code": "x", "name": "scratch-1a2b3c4d"}`, ""},
		{`{"code": "len(items)", "name": "tasks"}`, "tasks"},
		{`{"code": "x := 1\nx++", "name": "tasks", "gas": 4e6, "reset": true}`, "tasks · gas 4M · reset"},
		{`{"code": "x", "gas": 1e9}`, "gas 1G"},
		{`{"code": "x", "reset": true}`, "reset"},
	} {
		var p RunParams
		require.NoError(t, json.Unmarshal([]byte(c.input), &p))
		require.Equal(t, c.lead, p.Lead(), c.input)

		act, err := run.(core.Describer).Describe(core.ToolCall{ID: "c", Name: RunToolName, Input: json.RawMessage(c.input)}, testEnv("session-1"))
		require.NoError(t, err)
		require.Equal(t, &core.Display{Kind: "command", Title: c.lead, Text: p.Code}, act.Preview, c.input)

		heading := p.Code
		if c.lead != "" {
			heading = c.lead + " · " + p.Code
		}
		resp := call(t, run, c.input)
		require.Equal(t, &core.Display{Kind: "command", Title: heading, Text: resp.Content}, resp.Display, c.input)
		require.True(t, strings.HasPrefix(resp.Content, "3\nok · VM tasks · "), resp.Content)
	}
}

// fakeClient is a scripted client: what the tool asks of the GnoVM, and
// what it is answered.
type fakeClient struct {
	mu         sync.Mutex
	reqs       []gnovm.RunRequest
	run        func(ctx context.Context, req gnovm.RunRequest) (*gnovm.RunResult, error)
	kept       func(ctx context.Context, project, name, req string) (bool, error)
	keptFrames func(ctx context.Context, project, req string) ([]gnovm.MemFrame, bool, error)
	asked      []string // the Kept and KeptFrames questions, as "project name req"
}

func (f *fakeClient) Run(ctx context.Context, req gnovm.RunRequest) (*gnovm.RunResult, error) {
	f.mu.Lock()
	// The tool goes on adding to Checked after the call: keep it as it was.
	req.Checked = append([]string(nil), req.Checked...)
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	return f.run(ctx, req)
}

func (f *fakeClient) New(context.Context, gnovm.NewRequest) (gnovm.VMInfo, bool, error) {
	return gnovm.VMInfo{}, false, errors.New("not scripted")
}

func (f *fakeClient) List(context.Context, string) (gnovm.ListResult, error) {
	return gnovm.ListResult{}, errors.New("not scripted")
}

func (f *fakeClient) Drop(context.Context, string, string, string) error {
	return errors.New("not scripted")
}

func (f *fakeClient) Kept(ctx context.Context, project, name, req string) (bool, error) {
	f.mu.Lock()
	f.asked = append(f.asked, project+" "+name+" "+req)
	f.mu.Unlock()
	return f.kept(ctx, project, name, req)
}

func (f *fakeClient) KeptFrames(ctx context.Context, project, req string) ([]gnovm.MemFrame, bool, error) {
	f.mu.Lock()
	f.asked = append(f.asked, project+" mem "+req)
	f.mu.Unlock()
	return f.keptFrames(ctx, project, req)
}

func (f *fakeClient) Deaths() []string { return nil }

// over is the four tools over a scripted client.
func (f *fakeClient) over(o Options) *Set {
	o.Client = func() (Client, error) { return f, nil }
	if o.Project == "" {
		o.Project = "/proj"
	}
	return New(o)
}

// The request the tool sends is what integration-contract.md section 5.2
// lists: the project, the session, the call's own id, the arguments, and
// for a call on mem what the harness knows of the session, each asked for
// once, in order, before the call runs.
func TestRequest(t *testing.T) {
	var order []string
	said := func(what string) { order = append(order, what) }
	names := map[string]string{"make the port configurable": "port-flag", "add a --port flag": "add-flag"}
	f := &fakeClient{run: func(context.Context, gnovm.RunRequest) (*gnovm.RunResult, error) {
		said("run")
		return &gnovm.RunResult{VM: gnovm.MemVM}, nil
	}}
	run := f.over(Options{
		SubAgent:  func(_ context.Context, s string) bool { said("sub-agent " + s); return true },
		BoundGoal: func(_ context.Context, s string) string { said("goal " + s); return "ship-it" },
		MemStack: func(_ context.Context, s string) []string {
			said("stack " + s)
			return []string{"ship-it", "ship-it/build"}
		},
		MemFresh: func(_ context.Context, s, id string) string { said("fresh " + s + " " + id); return "ship-it/build" },
		Namer: func(_ context.Context, s string, texts []string) map[string]string {
			said("namer " + s + " " + strings.Join(texts, "|"))
			return names
		},
		CheckCloses: func(context.Context, string, string, []gnovm.MemClose) string { said("check"); return "" },
	}).Tool(RunToolName)

	code := "mem.Pop(\"built\")\nmem.Todo(\"make the port configurable\", \"add a --port flag\")"
	input, _ := json.Marshal(map[string]any{"name": "mem", "code": code, "gas": 1e9, "reset": true})
	resp := callAs(t, context.Background(), "session-9", run, "toolu_01", string(input))
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, []string{
		"sub-agent session-9", "goal session-9", "stack session-9", "fresh session-9 toolu_01",
		"namer session-9 make the port configurable|add a --port flag", "run",
	}, order)
	require.Equal(t, []gnovm.RunRequest{{
		Project: "/proj", Session: "session-9", Name: "mem", Code: code, Gas: 1_000_000_000, Reset: true, Req: "toolu_01",
		Goal: "ship-it", Frames: []string{"ship-it", "ship-it/build"}, Fresh: "ship-it/build", Names: names,
		SubAgent: true, CheckCloses: true, Checked: []string{},
	}}, normalized(f.reqs))

	// Fresh is asked only of a call that may pop, in a session that stands
	// in a frame; names only for code that keeps a text without one.
	order, f.reqs = nil, nil
	callAs(t, context.Background(), "session-9", run, "toolu_02", `{"name": "mem", "code": "mem.Show()"}`)
	require.Equal(t, []string{"sub-agent session-9", "goal session-9", "stack session-9", "run"}, order)
	require.Equal(t, "", f.reqs[0].Fresh)
	require.Nil(t, f.reqs[0].Names)

	// A call on a VM carries nothing of mem's: no stack, no goal, no
	// names, no close check.
	order, f.reqs = nil, nil
	callAs(t, context.Background(), "session-9", run, "toolu_03", `{"name": "tasks", "code": "mem.Pop(\"x\")\nmem.Think(\"a thought\")"}`)
	require.Equal(t, []string{"sub-agent session-9", "run"}, order)
	require.Equal(t, []gnovm.RunRequest{{Project: "/proj", Session: "session-9", Name: "tasks", Code: "mem.Pop(\"x\")\nmem.Think(\"a thought\")", Req: "toolu_03", SubAgent: true, Checked: []string{}}}, normalized(f.reqs))

	// With nothing set, a call on mem is the first working run's: no
	// stack, no goal, nothing fresh, no names, no close check.
	f.reqs = nil
	bare := f.over(Options{}).Tool(RunToolName)
	callAs(t, context.Background(), "session-9", bare, "toolu_04", string(input))
	require.Equal(t, []gnovm.RunRequest{{Project: "/proj", Session: "session-9", Name: "mem", Code: code, Gas: 1_000_000_000, Reset: true, Req: "toolu_04", Checked: []string{}}}, normalized(f.reqs))

	// An empty stack is never asked about a fresh frame.
	order, f.reqs = nil, nil
	root := f.over(Options{
		MemStack: func(context.Context, string) []string { return nil },
		MemFresh: func(context.Context, string, string) string { said("fresh"); return "x" },
	}).Tool(RunToolName)
	callAs(t, context.Background(), "session-9", root, "toolu_05", `{"name": "mem", "code": "mem.Done(\"x\", \"done\")"}`)
	require.Equal(t, []string{"run"}, order)
	require.Equal(t, "", f.reqs[0].Fresh)
}

// normalized is requests with an empty Checked made the same as none, for
// comparing.
func normalized(reqs []gnovm.RunRequest) []gnovm.RunRequest {
	out := append([]gnovm.RunRequest(nil), reqs...)
	for i := range out {
		if len(out[i].Checked) == 0 {
			out[i].Checked = []string{}
		}
	}
	return out
}

// Where the project and the session come from: the options first, then
// what the harness gives the call.
func TestProjectAndSession(t *testing.T) {
	f := &fakeClient{run: func(context.Context, gnovm.RunRequest) (*gnovm.RunResult, error) {
		return &gnovm.RunResult{VM: "v"}, nil
	}}
	client := func() (Client, error) { return f, nil }
	runWith := func(o Options, env *core.ToolEnv) (gnovm.RunRequest, error) {
		t.Helper()
		f.reqs = nil
		o.Client = client
		_, err := New(o).Tool(RunToolName).Run(context.Background(), core.ToolCall{ID: "c1", Name: RunToolName, Input: json.RawMessage(`{"code": "1"}`)}, env)
		if err != nil || len(f.reqs) == 0 {
			return gnovm.RunRequest{}, err
		}
		return f.reqs[0], nil
	}

	req, err := runWith(Options{Project: "/work/proj"}, &core.ToolEnv{Cwd: "/work/proj/sub", Roots: []string{"/elsewhere"}, SessionID: "s1"})
	require.NoError(t, err)
	require.Equal(t, "/work/proj s1", req.Project+" "+req.Session)

	// With no project in the options: the first of the roots, the session's
	// project, and not wherever a shell command left the working directory.
	req, err = runWith(Options{}, &core.ToolEnv{Cwd: "/work/proj/sub", Roots: []string{"/work/proj", "/work/added"}, SessionID: "s1"})
	require.NoError(t, err)
	require.Equal(t, "/work/proj", req.Project)
	req, err = runWith(Options{}, &core.ToolEnv{Cwd: "/work/solo", SessionID: "s1"})
	require.NoError(t, err)
	require.Equal(t, "/work/solo", req.Project)

	// A sub-agent's calls can be given a session of their own.
	req, err = runWith(Options{Session: func(_ context.Context, env *core.ToolEnv) string { return env.SessionID + "/agent-7" }},
		&core.ToolEnv{Cwd: "/p", SessionID: "s1"})
	require.NoError(t, err)
	require.Equal(t, "s1/agent-7", req.Session)

	// What the tool cannot run without is the harness's failure, not a
	// result for the model.
	for name, c := range map[string]struct {
		o   Options
		env *core.ToolEnv
	}{
		"no ToolEnv":           {Options{Project: "/p"}, nil},
		"no project directory": {Options{}, &core.ToolEnv{SessionID: "s1"}},
		"no session id":        {Options{}, &core.ToolEnv{Cwd: "/p"}},
	} {
		_, err = runWith(c.o, c.env)
		require.True(t, err != nil && strings.Contains(err.Error(), name), "%s: %v", name, err)
		require.Empty(t, f.reqs, name)
	}
	_, err = New(Options{Project: "/p"}).Tool(RunToolName).Run(context.Background(), core.ToolCall{ID: "c", Input: json.RawMessage(`{"code": "1"}`)}, testEnv("s1"))
	require.True(t, err != nil && strings.Contains(err.Error(), "no Options.Client"), "%v", err)

	// A client that answers nothing at all is the harness's failure too.
	f.run = func(context.Context, gnovm.RunRequest) (*gnovm.RunResult, error) { return nil, nil }
	_, err = runWith(Options{Project: "/p"}, testEnv("s1"))
	require.True(t, err != nil && strings.Contains(err.Error(), "neither a result nor an error"), "%v", err)

	// gnovm_list needs no session.
	res, err := New(Options{Client: client}).Tool(ListToolName).Run(context.Background(), core.ToolCall{ID: "c"}, &core.ToolEnv{Cwd: "/p"})
	require.NoError(t, err)
	require.Equal(t, "ERROR — nothing ran: not scripted", core.TextOf(res.Content))
}

// When the VMs cannot be reached every tool says so, to the model, and the
// next call asks again.
func TestVMsNotAvailable(t *testing.T) {
	tries := 0
	ts := New(Options{Project: "/proj", Client: func() (Client, error) {
		tries++
		return nil, errors.New("gnovm: starting the worker: no such file")
	}})
	for i, c := range []struct{ tool, input string }{
		{RunToolName, `{"code": "1"}`},
		{NewToolName, `{"name": "tasks"}`},
		{ListToolName, `{}`},
		{DropToolName, `{"name": "tasks", "action": "evict"}`},
	} {
		resp := call(t, ts.Tool(c.tool), c.input)
		require.True(t, resp.IsError, c.tool)
		require.Equal(t, "the Gno VMs are not available: gnovm: starting the worker: no such file", resp.Content, c.tool)
		require.Equal(t, i+1, tries, "each call asks for the client again")
	}
	// The arguments are checked before the VMs are asked for.
	call(t, ts.Tool(RunToolName), `{}`)
	require.Equal(t, 4, tries)

	// The program's own hold on its client says why it cannot be had, each
	// time it is asked: here, for want of a data directory.
	lazy := New(Options{Project: "/proj", Client: Lazy(gnovm.NewLazy(gnovm.ClientOptions{InProcess: true}))})
	for range 2 {
		resp := call(t, lazy.Tool(RunToolName), `{"code": "1"}`)
		require.True(t, resp.IsError)
		require.Equal(t, "the Gno VMs are not available: gnovm: a data directory is required", resp.Content)
	}
}

// After a call mem kept, what it kept is handed on in the order of
// integration-contract.md section 5.3.4: the frames first, whose text goes
// into the result between mem's own lines and the status line, then the
// task list, an erasure, the rules. Each is given a context that stopping
// the turn does not reach, with the call's values still in it; and the
// frames are kept with the result, whoever was told.
func TestAfterAKeptCall(t *testing.T) {
	type key struct{}
	frames := []gnovm.MemFrame{
		{Kind: "pop", Id: "ship/build", Purpose: "build", Result: "built", Thoughts: 2, Session: "session-1"},
		{Kind: "push", Id: "ship/test", Purpose: "test", Session: "session-1"},
	}
	goals := []gnovm.MemGoal{{Name: "ship", Title: "ship it", Next: "ship/test"}}
	erased := &gnovm.MemErasure{Names: []string{"old-fact"}}
	rules := []gnovm.MemRule{{Name: "fmt", Text: "run gofmt"}}

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), key{}, "the call's own"))
	defer cancel()
	f := &fakeClient{run: func(context.Context, gnovm.RunRequest) (*gnovm.RunResult, error) {
		cancel() // the person stops the turn while mem keeps the call
		return &gnovm.RunResult{VM: gnovm.MemVM, Output: "done `ship/build` \"build\" → built\nin `ship/test` \"test\": do it now\n", Wall: 40 * time.Millisecond,
			Journaled: true, Seq: 9, Notes: []string{"a note"}, Frames: frames, MemGoals: goals, Erased: erased, Rules: rules}, nil
	}}
	var order []string
	told := func(c context.Context, what string) {
		order = append(order, what)
		require.NoError(t, c.Err(), "%s was given a context the stop reached", what)
		require.Equal(t, "the call's own", c.Value(key{}), what)
	}
	run := f.over(Options{
		OnMemFrames: func(c context.Context, s, id string, fr []gnovm.MemFrame) string {
			told(c, "frames "+s+" "+id)
			require.Equal(t, frames, fr)
			return "Everything since you entered `ship/build` was removed from your context: this result stays.\n"
		},
		OnMemGoals: func(c context.Context, s string, g []gnovm.MemGoal) {
			told(c, "goals "+s)
			require.Equal(t, goals, g)
		},
		OnMemErased: func(c context.Context, s string, e gnovm.MemErasure) {
			told(c, "erased "+s)
			require.Equal(t, *erased, e)
		},
		OnMemRules: func(c context.Context, s string, r []gnovm.MemRule) {
			told(c, "rules "+s)
			require.Equal(t, rules, r)
		},
	}).Tool(RunToolName)

	resp := callAs(t, ctx, "session-1", run, "toolu_pop", `{"name": "mem", "code": "mem.Pop(\"built\")\nmem.Next()"}`)
	require.True(t, ctx.Err() != nil)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, []string{"frames session-1 toolu_pop", "goals session-1", "erased session-1", "rules session-1"}, order)
	require.Equal(t, "done `ship/build` \"build\" → built\n"+
		"in `ship/test` \"test\": do it now\n"+
		"Everything since you entered `ship/build` was removed from your context: this result stays.\n"+
		"ok · mem · ran 40ms · kept as entry 9\n"+
		"note: a note\n", resp.Content)

	// Kept with the result, under "frames", in the shape frames.md section
	// 3 gives, and read back the same once the result has been stored as
	// JSON.
	require.Equal(t, map[string]any{"frames": frames}, resp.Meta)
	require.Equal(t, frames, FramesOf(resp.Meta))
	stored, err := json.Marshal(resp.Meta)
	require.NoError(t, err)
	require.Equal(t, `{"frames":[`+
		`{"kind":"pop","id":"ship/build","purpose":"build","result":"built","thoughts":2,"session":"session-1"},`+
		`{"kind":"push","id":"ship/test","purpose":"test","session":"session-1"}]}`, string(stored))
	var back map[string]any
	require.NoError(t, json.Unmarshal(stored, &back))
	require.Equal(t, frames, FramesOf(back))
	require.Nil(t, FramesOf(nil))
	require.Nil(t, FramesOf(map[string]any{"frames": "not frames"}))

	// With nobody to tell, the frames are still kept with the result, and
	// nothing is added to its text.
	bare := f.over(Options{}).Tool(RunToolName)
	resp = callAs(t, context.Background(), "session-1", bare, "toolu_pop", `{"name": "mem", "code": "mem.Pop(\"built\")\nmem.Next()"}`)
	require.Equal(t, frames, FramesOf(resp.Meta))
	require.Equal(t, "done `ship/build` \"build\" → built\nin `ship/test` \"test\": do it now\nok · mem · ran 40ms · kept as entry 9\nnote: a note\n", resp.Content)

	// A call that kept nothing of the kind tells nobody: a read, and a
	// call that failed.
	order = nil
	f.run = func(context.Context, gnovm.RunRequest) (*gnovm.RunResult, error) {
		return &gnovm.RunResult{VM: gnovm.MemVM, Output: "mem: nothing yet\n", Wall: time.Millisecond}, nil
	}
	resp = callAs(t, context.Background(), "session-1", run, "toolu_show", `{"name": "mem", "code": "mem.Show()"}`)
	require.Empty(t, order)
	require.Nil(t, resp.Meta)
	f.run = func(context.Context, gnovm.RunRequest) (*gnovm.RunResult, error) {
		return &gnovm.RunResult{VM: gnovm.MemVM, Wall: time.Millisecond, Err: errors.New("nothing to pop: you are at the root"),
			Frames: frames, MemGoals: goals, Erased: erased, Rules: rules}, nil
	}
	resp = callAs(t, context.Background(), "session-1", run, "toolu_bad", `{"name": "mem", "code": "mem.Pop(\"x\")"}`)
	require.True(t, resp.IsError)
	require.Empty(t, order, "nothing of a failed call was kept, so nobody is told")
	require.Nil(t, resp.Meta)
	require.Equal(t, "ERROR — nothing from this call was kept: nothing to pop: you are at the root\nfailed · mem · ran 1ms · nothing kept\n", resp.Content)
}

// The close-check loop of integration-contract.md section 5.3.5: while a
// run brings closes that are not approved yet, they are checked; approved,
// the same request runs again with them named; refused, the refusal is the
// call's error and nothing the run printed is shown. Afterwards the check
// is told whether mem kept the call.
func TestCloseCheckLoop(t *testing.T) {
	a := gnovm.MemClose{Id: "stats/min", Title: "add Min", Result: "Min added in stats.go"}
	b := gnovm.MemClose{Id: "stats/max", Title: "add Max", Result: "Max added in stats.go"}
	pending := func(closes ...gnovm.MemClose) *gnovm.RunResult {
		return &gnovm.RunResult{VM: gnovm.MemVM, Output: "done `stats/min` \"add Min\" → Min added in stats.go\n", Wall: 2 * time.Millisecond,
			Notes: []string{"a note"}, Closes: closes, Err: errors.New("closes to check")}
	}
	var script []*gnovm.RunResult // what each run answers, in order
	f := &fakeClient{}
	f.run = func(context.Context, gnovm.RunRequest) (*gnovm.RunResult, error) {
		res := script[0]
		script = script[1:]
		return res, nil
	}
	var asked [][]gnovm.MemClose
	var verdicts []string // what the check answers, in order
	var kept []string
	run := f.over(Options{
		CheckCloses: func(_ context.Context, s, id string, closes []gnovm.MemClose) string {
			require.Equal(t, "session-1 toolu_close", s+" "+id)
			asked = append(asked, closes)
			v := verdicts[0]
			verdicts = verdicts[1:]
			return v
		},
		ClosesKept: func(c context.Context, s, id string, k bool) {
			require.NoError(t, c.Err())
			kept = append(kept, fmt.Sprintf("%s %s %v", s, id, k))
		},
	}).Tool(RunToolName)
	input := `{"name": "mem", "code": "mem.Pop(\"Min added in stats.go\")\nmem.Next()\nmem.Pop(\"Max added in stats.go\")"}`
	reset := func() { f.reqs, asked, kept = nil, nil, nil }

	// Approve: the first run brings a, which is approved; the second brings
	// a again and b, of which only b is asked about; the third is kept.
	script = []*gnovm.RunResult{pending(a), pending(a, b), {VM: gnovm.MemVM, Output: "done both\n", Wall: time.Millisecond, Journaled: true, Seq: 5}}
	verdicts = []string{"", ""}
	resp := callAs(t, context.Background(), "session-1", run, "toolu_close", input)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "done both\nok · mem · ran 1ms · kept as entry 5\n", resp.Content)
	require.Equal(t, [][]gnovm.MemClose{{a}, {b}}, asked)
	require.Equal(t, 3, len(f.reqs))
	require.Empty(t, f.reqs[0].Checked)
	require.Equal(t, []string{"stats/min\x00Min added in stats.go", "stats/min\x00*"}, f.reqs[1].Checked)
	require.Equal(t, []string{"stats/min\x00Min added in stats.go", "stats/min\x00*", "stats/max\x00Max added in stats.go", "stats/max\x00*"}, f.reqs[2].Checked)
	for _, req := range f.reqs {
		require.True(t, req.CheckCloses)
		require.Equal(t, "toolu_close", req.Req, "the same request runs again")
	}
	require.Equal(t, []string{"session-1 toolu_close true"}, kept)

	// Refuse: the refusal is the call's error, with nothing of what the run
	// printed; the notes and the status line stand.
	reset()
	script = []*gnovm.RunResult{pending(a)}
	verdicts = []string{"not popped: the check of `stats/min` found: no edit of stats.go is shown. Do what is missing."}
	resp = callAs(t, context.Background(), "session-1", run, "toolu_close", input)
	require.True(t, resp.IsError)
	require.Equal(t, "ERROR — nothing from this call was kept: not popped: the check of `stats/min` found: no edit of stats.go is shown. Do what is missing.\n"+
		"failed · mem · ran 2ms · nothing kept\n"+
		"note: a note\n", resp.Content)
	require.Equal(t, 1, len(f.reqs))
	require.Equal(t, []string{"session-1 toolu_close false"}, kept)
	require.Nil(t, resp.Meta)

	// Approve after the retry: the model sends the call again, the close is
	// asked about anew (what was approved lasts one call), approved, and
	// kept.
	reset()
	script = []*gnovm.RunResult{pending(a), {VM: gnovm.MemVM, Output: "done `stats/min` \"add Min\" → Min added in stats.go\n", Wall: time.Millisecond, Journaled: true, Seq: 6}}
	verdicts = []string{""}
	resp = callAs(t, context.Background(), "session-1", run, "toolu_close", input)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, [][]gnovm.MemClose{{a}}, asked)
	require.Empty(t, f.reqs[0].Checked)
	require.Equal(t, []string{"session-1 toolu_close true"}, kept)

	// Approved, but the run that follows fails for another reason: the
	// check is told the call was not kept.
	reset()
	script = []*gnovm.RunResult{pending(a), {VM: gnovm.MemVM, Wall: time.Millisecond, Err: errors.New("`stats/max` has open todos")}}
	verdicts = []string{""}
	resp = callAs(t, context.Background(), "session-1", run, "toolu_close", input)
	require.True(t, resp.IsError)
	require.Equal(t, []string{"session-1 toolu_close false"}, kept)

	// A todo approved once is not asked about again though its result
	// changed between runs; and closes that are all approved already end
	// the loop rather than turn it for ever.
	reset()
	changed := a
	changed.Result = "Min added at 12:00:01"
	script = []*gnovm.RunResult{pending(a), pending(changed)}
	verdicts = []string{""}
	resp = callAs(t, context.Background(), "session-1", run, "toolu_close", input)
	require.True(t, resp.IsError)
	require.Equal(t, [][]gnovm.MemClose{{a}}, asked)
	require.Equal(t, 2, len(f.reqs))
	require.True(t, strings.HasPrefix(resp.Content, "ERROR — nothing from this call was kept: closes to check\n"), resp.Content)
	require.Equal(t, []string{"session-1 toolu_close false"}, kept)

	// A call with no close to check asks nothing and tells nothing; so does
	// every call when no check is set.
	reset()
	script = []*gnovm.RunResult{{VM: gnovm.MemVM, Output: "mem: nothing yet\n", Wall: time.Millisecond}}
	callAs(t, context.Background(), "session-1", run, "toolu_close", `{"name": "mem", "code": "mem.Show()"}`)
	require.Empty(t, asked)
	require.Empty(t, kept)
	reset()
	script = []*gnovm.RunResult{pending(a)}
	unchecked := f.over(Options{}).Tool(RunToolName)
	resp = callAs(t, context.Background(), "session-1", unchecked, "toolu_close", input)
	require.True(t, resp.IsError)
	require.False(t, f.reqs[0].CheckCloses)
	require.Equal(t, 1, len(f.reqs))
}

// A worker that died before it replied (integration-contract.md section
// 5.3.6): the next worker is asked whether the call was kept, and the model
// is told which. For mem, the frames the lost call pushed and popped reach
// the harness exactly as if its reply had arrived.
func TestWorkerDiedBeforeReplying(t *testing.T) {
	died := errors.New("the VM worker ended before it replied (signal: killed); the outcome is unknown until it is asked again")
	scratch := gnovm.ScratchName("session-1")
	f := &fakeClient{run: func(context.Context, gnovm.RunRequest) (*gnovm.RunResult, error) { return nil, died }}
	var told []string
	run := f.over(Options{
		OnMemFrames: func(c context.Context, s, id string, fr []gnovm.MemFrame) string {
			require.NoError(t, c.Err(), "the frames of a kept call are applied whatever happened to the turn")
			told = append(told, fmt.Sprintf("%s %s %d", s, id, len(fr)))
			return "Everything since you entered `x` was removed from your context."
		},
		OnMemGoals: func(context.Context, string, []gnovm.MemGoal) { told = append(told, "goals") },
	}).Tool(RunToolName)

	// A VM: kept, not kept, and the question itself failing.
	for _, c := range []struct {
		input, vm string
		kept      bool
		kerr      error
		want      string
	}{
		{`{"code": "x := 1"}`, scratch, true, nil, died.Error() + ". It was kept: the VM has its effects, but its output was lost."},
		{`{"name": "scratch", "code": "x := 1"}`, scratch, false, nil, died.Error() + ". It was not kept: nothing from it is in the VM."},
		{`{"name": "tasks", "code": "x := 1"}`, "tasks", false, errors.New("gnovm: starting the worker: no such file"), died.Error() + "; asking whether it was kept failed too: gnovm: starting the worker: no such file"},
	} {
		f.asked = nil
		f.kept = func(context.Context, string, string, string) (bool, error) { return c.kept, c.kerr }
		resp := callAs(t, context.Background(), "session-1", run, "toolu_lost", c.input)
		require.True(t, resp.IsError, "the outcome of a VM call whose reply was lost is told as an error: %s", c.input)
		require.Equal(t, c.want, resp.Content)
		require.Equal(t, []string{"/proj " + c.vm + " toolu_lost"}, f.asked)
		require.Nil(t, resp.Meta)
	}

	// mem: not kept, and the question failing, are errors.
	f.asked = nil
	f.keptFrames = func(context.Context, string, string) ([]gnovm.MemFrame, bool, error) { return nil, false, nil }
	resp := callAs(t, context.Background(), "session-1", run, "toolu_lost", `{"name": "mem", "code": "mem.Next()"}`)
	require.True(t, resp.IsError)
	require.Equal(t, died.Error()+". It was not kept: nothing from it is in mem.", resp.Content)
	require.Equal(t, []string{"/proj mem toolu_lost"}, f.asked)
	f.keptFrames = func(context.Context, string, string) ([]gnovm.MemFrame, bool, error) {
		return nil, false, errors.New("mem's journal cannot be read")
	}
	resp = callAs(t, context.Background(), "session-1", run, "toolu_lost", `{"name": "mem", "code": "mem.Next()"}`)
	require.True(t, resp.IsError)
	require.Equal(t, died.Error()+"; asking whether it was kept failed too: mem's journal cannot be read", resp.Content)
	require.Empty(t, told)

	// mem: kept, with nothing pushed or popped. It is no error: mem has
	// the call's effects.
	f.keptFrames = func(context.Context, string, string) ([]gnovm.MemFrame, bool, error) { return nil, true, nil }
	resp = callAs(t, context.Background(), "session-1", run, "toolu_lost", `{"name": "mem", "code": "mem.Fact(\"a\", \"b\")"}`)
	require.False(t, resp.IsError)
	require.Equal(t, died.Error()+". It was kept: mem has its effects, but its output was lost; mem.Show() says where you stand.", resp.Content)
	require.Nil(t, resp.Meta)
	require.Empty(t, told)

	// mem: kept, with frames. They are handed over and kept with the
	// result as a reply's would be, though the turn was stopped meanwhile,
	// and the question is asked with a context the stop does not reach.
	frames := []gnovm.MemFrame{{Kind: "pop", Id: "x", Purpose: "do x", Result: "did x", Session: "session-1"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.run = func(context.Context, gnovm.RunRequest) (*gnovm.RunResult, error) { cancel(); return nil, died }
	f.keptFrames = func(c context.Context, _, _ string) ([]gnovm.MemFrame, bool, error) {
		require.NoError(t, c.Err(), "whether mem kept the call is asked whatever happened to the turn")
		return frames, true, nil
	}
	resp = callAs(t, ctx, "session-1", run, "toolu_lost", `{"name": "mem", "code": "mem.Pop(\"did x\")"}`)
	require.False(t, resp.IsError)
	require.Equal(t, died.Error()+". It was kept: mem has its effects, but its output was lost; mem.Show() says where you stand.\n"+
		"Everything since you entered `x` was removed from your context.", resp.Content)
	require.Equal(t, frames, FramesOf(resp.Meta))
	require.Equal(t, []string{"session-1 toolu_lost 1"}, told)

	// Any other failure to run is told as a call that did not run.
	f.run = func(context.Context, gnovm.RunRequest) (*gnovm.RunResult, error) {
		return nil, errors.New("VM tasks is in use by gnosh process 4242; try again shortly")
	}
	resp = callAs(t, context.Background(), "session-1", run, "toolu_busy", `{"name": "tasks", "code": "1"}`)
	require.True(t, resp.IsError)
	require.Equal(t, "ERROR — nothing ran: VM tasks is in use by gnosh process 4242; try again shortly", resp.Content)
}

// A session in mem, through the tool and the real mem: a todo made and
// entered, a thought kept, the todo popped. The text is mem's own receipts
// (mem.md section 6.2), then what the frames layer adds, then the status
// line; the frames are kept with each result that made any, and the stack
// the harness derives from them is what the next call is told.
func TestMemSession(t *testing.T) {
	var stack []string
	var told []string
	var sent [][]string // the stack each call was sent with
	run := toolsOver(newLazy(t), Options{
		MemStack: func(context.Context, string) []string {
			sent = append(sent, append([]string(nil), stack...))
			return stack
		},
		OnMemFrames: func(_ context.Context, s, id string, events []gnovm.MemFrame) string {
			var said []string
			for _, e := range events {
				require.Equal(t, s, e.Session)
				told = append(told, e.Kind+" "+e.Id)
				if e.Kind == "push" {
					stack = append(stack, e.Id)
				} else if n := len(stack); n > 0 && stack[n-1] == e.Id {
					stack = stack[:n-1]
					said = append(said, "Everything since you entered `"+e.Id+"` was removed from your context: this result stays.")
				}
			}
			return strings.Join(said, "\n")
		},
		OnMemGoals: func(_ context.Context, _ string, goals []gnovm.MemGoal) {
			told = append(told, "task list "+goals[len(goals)-1].Name)
		},
	}).Tool(RunToolName)
	mem := func(code string) response {
		t.Helper()
		input, _ := json.Marshal(map[string]string{"name": "mem", "code": code})
		return call(t, run, string(input))
	}
	const entered = ": do it now, with your tools; once what they return shows it done, pop it with that and what the next todo needs. Everything from here until the pop is removed from your context then, what you were told included; what matters after, keep as a thought first. Your result stays, and nothing you did is undone.\n"

	// A todo made and entered in one call: a push.
	resp := mem(`mem.Todo("find why TestFlaky fails").Push()`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "added `find-why-testflaky-fails` \"find why TestFlaky fails\"\n"+
		"in `find-why-testflaky-fails` \"find why TestFlaky fails\""+entered+
		"ok · mem · ran T · kept as entry 1\n", steady(resp.Content))
	require.Equal(t, []gnovm.MemFrame{{Kind: "push", Id: "find-why-testflaky-fails", Purpose: "find why TestFlaky fails", Session: "session-1"}}, FramesOf(resp.Meta))
	require.Equal(t, []string{"find-why-testflaky-fails"}, stack)

	// A thought kept on it: no frame is pushed or popped, so nothing is
	// kept with the result.
	resp = mem(`mem.Think("the shared port is 8080")`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "thought `shared-port-8080` kept on `find-why-testflaky-fails`\nok · mem · ran T · kept as entry 2\n", steady(resp.Content))
	require.Nil(t, resp.Meta)

	// A read keeps nothing.
	resp = mem(`mem.Thought("shared-port-8080")`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "thought `shared-port-8080` on `find-why-testflaky-fails`: the shared port is 8080\nok · mem · ran T · nothing to keep\n", steady(resp.Content))

	// The pop: mem's lines, then the frames layer's, then the status line.
	resp = mem(`mem.Pop("TestFlaky races on the shared port")`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, "done `find-why-testflaky-fails` \"find why TestFlaky fails\" → TestFlaky races on the shared port\n"+
		"its thoughts, kept: `shared-port-8080`: the shared port is 8080\n"+
		"That closes `find-why-testflaky-fails`, the whole plan: its work is done and stays done, though it is out of your context now. Tell the user what came of it from the results above.\n"+
		"Everything since you entered `find-why-testflaky-fails` was removed from your context: this result stays.\n"+
		"ok · mem · ran T · kept as entry 3\n", steady(resp.Content))
	require.Equal(t, []gnovm.MemFrame{{Kind: "pop", Id: "find-why-testflaky-fails", Purpose: "find why TestFlaky fails", Result: "TestFlaky races on the shared port", Thoughts: 1, Session: "session-1"}}, FramesOf(resp.Meta))
	require.Empty(t, stack)

	// mem was told the stack before each call, as it stood then; and the
	// frames were handed over before the task list.
	require.Equal(t, [][]string{nil, {"find-why-testflaky-fails"}, {"find-why-testflaky-fails"}, {"find-why-testflaky-fails"}}, sent)
	require.Equal(t, []string{
		"push find-why-testflaky-fails", "task list find-why-testflaky-fails",
		"task list find-why-testflaky-fails",
		"pop find-why-testflaky-fails", "task list find-why-testflaky-fails",
	}, told)

	// A refusal of mem's is the model's to read, under the house prefix,
	// with nothing kept and nobody told.
	told = nil
	resp = mem(`mem.Pop("again")`)
	require.True(t, resp.IsError)
	require.True(t, strings.HasPrefix(resp.Content, "ERROR — nothing from this call was kept: nothing to pop: you are at the root"), resp.Content)
	require.True(t, strings.HasSuffix(steady(resp.Content), "\nfailed · mem · ran T · nothing kept\n"), resp.Content)
	require.Nil(t, resp.Meta)
	require.Empty(t, told)
}

// What the harness knows of a session reaches mem with each call, and mem
// acts on it: the names made for a call's unnamed texts, the frame that is
// fresh, the todo the session's /goal binds.
func TestMemIsToldWhatTheHarnessKnows(t *testing.T) {
	var stack []string
	fresh, goal := "", ""
	var asked [][]string
	run := toolsOver(newLazy(t), Options{
		MemStack: func(context.Context, string) []string { return stack },
		OnMemFrames: func(_ context.Context, _, _ string, events []gnovm.MemFrame) string {
			for _, e := range events {
				if e.Kind == "push" {
					stack = append(stack, e.Id)
				} else if n := len(stack); n > 0 && stack[n-1] == e.Id {
					stack = stack[:n-1]
				}
			}
			return ""
		},
		MemFresh:  func(context.Context, string, string) string { return fresh },
		BoundGoal: func(context.Context, string) string { return goal },
		Namer: func(_ context.Context, _ string, texts []string) map[string]string {
			asked = append(asked, texts)
			return map[string]string{"find why TestFlaky fails": "flaky-cause", "the shared port is 8080": "Not A Name"}
		},
	}).Tool(RunToolName)
	mem := func(code string) response {
		t.Helper()
		input, _ := json.Marshal(map[string]string{"name": "mem", "code": code})
		return call(t, run, string(input))
	}

	// The name the small model made is the todo's; a text it named with
	// something that is no name, and one it left out, are named by mem from
	// their words.
	resp := mem("mem.Todo(\"find why TestFlaky fails\").Push()\nmem.Think(\"the shared port is 8080\")\nmem.Later(\"rerun the suite\")")
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, [][]string{{"find why TestFlaky fails", "the shared port is 8080", "rerun the suite"}}, asked)
	require.Contains(t, resp.Content, "added `flaky-cause` \"find why TestFlaky fails\"\n")
	require.Contains(t, resp.Content, "thought `shared-port-8080` kept on `flaky-cause`\n")
	require.Contains(t, resp.Content, "`rerun-suite` \"rerun the suite\"")
	require.Equal(t, []string{"flaky-cause"}, stack)

	// A frame the harness found fresh is not popped: nothing came between
	// entering it and the pop.
	fresh = "flaky-cause"
	resp = mem(`mem.Pop("fixed")`)
	require.True(t, resp.IsError)
	require.True(t, strings.HasPrefix(resp.Content, "ERROR — nothing from this call was kept: not popped: nothing came between your entering `flaky-cause` \"find why TestFlaky fails\" and this pop, so it is not done. Do it, or check that it is done, then pop it."), resp.Content)
	require.Equal(t, []string{"flaky-cause"}, stack, "a refused pop pops nothing")
	require.Nil(t, resp.Meta)
	fresh = ""
	resp = mem(`mem.Pop("fixed")`)
	require.False(t, resp.IsError, resp.Content)
	require.Empty(t, stack)

	// The todo the session's /goal binds is not the session's to drop.
	goal = "rerun-suite"
	resp = mem(`mem.Drop("rerun-suite", "not today")`)
	require.True(t, resp.IsError)
	require.True(t, strings.HasPrefix(resp.Content, "ERROR — nothing from this call was kept: "), resp.Content)
	require.Contains(t, resp.Content, "/goal")
	goal = ""
	resp = mem(`mem.Drop("rerun-suite", "not today")`)
	require.False(t, resp.IsError, resp.Content)
}

// What a call to mem kept besides frames reaches the harness too: a rule
// it set, and what it erased.
func TestMemRulesAndErasures(t *testing.T) {
	var rules []gnovm.MemRule
	var erased []gnovm.MemErasure
	var order []string
	run := toolsOver(newLazy(t), Options{
		OnMemRules: func(_ context.Context, _ string, r []gnovm.MemRule) {
			order = append(order, "rules")
			rules = append(rules, r...)
		},
		OnMemErased: func(_ context.Context, _ string, e gnovm.MemErasure) {
			order = append(order, "erased")
			erased = append(erased, e)
		},
	}).Tool(RunToolName)
	mem := func(code string) response {
		t.Helper()
		input, _ := json.Marshal(map[string]string{"name": "mem", "code": code})
		return call(t, run, string(input))
	}

	resp := mem(`mem.Rule("gofumpt-before-commit", "Always run gofumpt before committing")`)
	require.False(t, resp.IsError, resp.Content)
	require.True(t, strings.HasPrefix(resp.Content, "rule `gofumpt-before-commit` is new; every session will be given it\n"), resp.Content)
	require.Equal(t, []gnovm.MemRule{{Name: "gofumpt-before-commit", Text: "Always run gofumpt before committing"}}, rules)

	resp = mem(`mem.Fact("deploy", "Deploy with the key kz-live-4f9a2c, then run make deploy.")`)
	require.False(t, resp.IsError, resp.Content)
	require.Empty(t, erased)
	resp = mem(`mem.Redact("kz-live-4f9a2c")`)
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, 1, len(erased))
	require.Equal(t, []string{"kz-live-4f9a2c"}, erased[0].Secrets())
	require.Equal(t, []string{"rules", "erased"}, order)
}

// A call's arguments are read by their names and types; a number or a
// boolean in quotes is read too, a field the tool does not take is passed
// over, and what cannot be read is said in plain words.
func TestDecodeParams(t *testing.T) {
	var run RunParams
	require.NoError(t, decodeParams(json.RawMessage(` {"code": "x", "name": "tasks", "gas": "1e9", "reset": "TRUE", "other": [1]} `), &run))
	require.Equal(t, RunParams{Code: "x", Name: "tasks", Gas: 1e9, Reset: true}, run)

	for _, raw := range []string{``, `null`, `{}`, `{"code": null, "gas": null}`} {
		run = RunParams{}
		require.NoError(t, decodeParams(json.RawMessage(raw), &run), raw)
		require.Equal(t, RunParams{}, run, raw)
	}

	var mk NewParams
	require.NoError(t, decodeParams(json.RawMessage(`{"name": "tasks", "memory_mb": 32, "ns_per_gas": 0.5, "persist": false, "description": ""}`), &mk))
	require.Equal(t, "tasks", mk.Name)
	require.True(t, mk.MemoryMB != nil && *mk.MemoryMB == 32 && mk.NsPerGas != nil && *mk.NsPerGas == 0.5)
	require.True(t, mk.Persist != nil && !*mk.Persist, "a false that was given is not a field left out")
	require.True(t, mk.Description != nil && *mk.Description == "")
	require.True(t, mk.Gas == nil && mk.Pinned == nil)

	for raw, want := range map[string]string{
		`[]`:                     "the arguments must be one JSON object",
		`"code"`:                 "the arguments must be one JSON object",
		`{"code": `:              "the arguments must be one JSON object",
		`{"name": {"n": 1}}`:     "name must be a string",
		`{"memory_mb": "lots"}`:  "memory_mb must be a number",
		`{"memory_mb": "NaN"}`:   "memory_mb must be a number",
		`{"memory_mb": true}`:    "memory_mb must be a number",
		`{"persist": 1}`:         "persist must be true or false",
		`{"pinned": "yes"}`:      "pinned must be true or false",
		`{"description": ["a"]}`: "description must be a string",
		`{"gas": []}`:            "gas must be a number",
	} {
		mk = NewParams{}
		err := decodeParams(json.RawMessage(raw), &mk)
		require.True(t, err != nil, raw)
		require.Equal(t, want, err.Error(), raw)
	}

	// Every field that can be read is set, though one before it cannot.
	var drop DropParams
	err := decodeParams(json.RawMessage(`{"name": 3, "action": "delete"}`), &drop)
	require.True(t, err != nil && err.Error() == "name must be a string", "%v", err)
	require.Equal(t, DropParams{Action: "delete"}, drop)
	run = RunParams{}
	err = decodeParams(json.RawMessage(`{"code": 1, "name": 2, "gas": 1e6, "reset": true}`), &run)
	require.True(t, err != nil && err.Error() == "code must be a string", "%v", err)
	require.Equal(t, RunParams{Gas: 1e6, Reset: true}, run)

	// A number past what a float64 holds is still a number, far out of any
	// range, and converts to an integer safely.
	mk = NewParams{}
	require.NoError(t, decodeParams(json.RawMessage(`{"memory_mb": 1e999, "gas": -1e999}`), &mk))
	require.Equal(t, 1e18, *mk.MemoryMB)
	require.Equal(t, -1e18, *mk.Gas)
}

// A goal as a task list (mem.md section 15): a todo with todos under it is
// one line for each that is not dropped or forgotten; one with none is
// itself.
func TestMemTodos(t *testing.T) {
	names := map[string]string{"build": "ship/build", "test": "ship/test", "docs": "ship/docs", "tag it": "tag"}
	line := func(title string, state LineState) TaskLine {
		return TaskLine{Name: names[title], Title: title, AtWork: title, State: state}
	}
	steps := []gnovm.MemStep{
		{Name: "ship/build", Title: "build", Status: "done"},
		{Name: "ship/test", Title: "test"},
		{Name: "ship/tag", Title: "tag", Status: "dropped"},
		{Name: "ship/docs", Title: "docs", Doing: "s1"},
		{Name: "ship/old", Title: "old", Status: "forgotten"},
	}
	g := gnovm.MemGoal{Name: "ship", Title: "ship it", Next: "ship/test", Steps: steps}
	// The one this session holds is in progress.
	require.Equal(t, []TaskLine{line("build", LineDone), line("test", LineOpen), line("docs", LineWorking)}, MemTodos(g, "s1"))
	// A session that holds none: the one next is in, or is under.
	require.Equal(t, []TaskLine{line("build", LineDone), line("test", LineWorking), line("docs", LineOpen)}, MemTodos(g, "s2"))
	g.Next = "ship/docs/intro"
	require.Equal(t, []TaskLine{line("build", LineDone), line("test", LineOpen), line("docs", LineWorking)}, MemTodos(g, "s2"))

	require.Equal(t, []TaskLine{line("tag it", LineWorking)}, MemTodos(gnovm.MemGoal{Name: "tag", Title: "tag it"}, "s1"))
	require.Equal(t, []TaskLine{line("tag it", LineDone)}, MemTodos(gnovm.MemGoal{Name: "tag", Title: "tag it", Status: "done"}, "s1"))

	// SessionTodos shows the last goal a call changed, and nothing for no
	// session or no goal.
	setter := &todoSetter{todos: map[string][]TaskLine{}}
	show := SessionTodos(setter)
	show(context.Background(), "s1", []gnovm.MemGoal{g, {Name: "tag", Title: "tag it"}})
	require.Equal(t, map[string][]TaskLine{"s1": {line("tag it", LineWorking)}}, setter.todos)
	show(context.Background(), "", []gnovm.MemGoal{g})
	show(context.Background(), "s2", nil)
	require.Equal(t, 1, len(setter.todos))

	// A line's state reads as a word, and a line with nothing said of its
	// state is open.
	require.Equal(t, "open working done", LineOpen.String()+" "+LineWorking.String()+" "+LineDone.String())
	require.Equal(t, "LineState(7)", LineState(7).String())
	require.Equal(t, LineOpen, TaskLine{Title: "docs"}.State)
}

// The parsers take anything a model may write without failing.
func FuzzDecodeParams(f *testing.F) {
	for _, seed := range []string{
		``, `null`, `{}`, `[]`, `{"code": "x := 1", "name": "mem", "gas": 1e9, "reset": true}`,
		`{"name": "tasks", "memory_mb": "32", "persist": "false"}`, `{"gas": 1e999}`, `{"code": 1}`, `{"action": "delete", "name": "x"}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var run RunParams
		if decodeParams(raw, &run) == nil {
			// What was read is read the same from its own JSON.
			again, err := json.Marshal(run)
			if err != nil {
				t.Fatalf("%q was read as arguments that are not JSON: %v", raw, err)
			}
			var back RunParams
			if err := decodeParams(again, &back); err != nil || back != run {
				t.Fatalf("%q was read as %+v, which reads back as %+v (%v)", raw, run, back, err)
			}
		}
		var mk NewParams
		_ = decodeParams(raw, &mk)
		var drop DropParams
		_ = decodeParams(raw, &drop)
		for _, tool := range New(Options{}).Tools() {
			_, _ = tool.(core.Describer).Describe(core.ToolCall{ID: "c", Input: raw}, testEnv("s"))
		}
	})
}

func FuzzUnnamedTexts(f *testing.F) {
	for _, seed := range []string{
		``, `mem.Think("a")`, `mem.Todo("a", "b").Push()`, "mem.Fact(`x`)", `mem.Later(`, "package main\nfunc main() {}", `mem.Think("\xff")`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, code string) {
		seen := map[string]bool{}
		for _, text := range unnamedTexts(code) {
			if text == "" || seen[text] {
				t.Fatalf("%q: the text %q is empty or listed twice", code, text)
			}
			seen[text] = true
		}
	})
}
