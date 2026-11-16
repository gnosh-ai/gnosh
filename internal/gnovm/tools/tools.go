// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

// Package gnovmtools is the agent's tools for GnoVM machines: gnovm_run,
// gnovm_new, gnovm_list and gnovm_drop. The machines themselves are package
// gnovm's.
//
// New makes the four from an Options, which holds everything they are given
// from outside: the way to the GnoVM, the project, and one function for each
// thing the surrounding harness knows and the tools do not (a session's
// stack of frames in mem, its /goal, the close check) or does with what a
// call to mem kept (the frames it pushed and popped, the task list, an
// erasure, the rules). options.go says exactly when each is called and what
// it must return.
//
//	ts := gnovmtools.New(gnovmtools.Options{
//		Client:  gnovmtools.Lazy(vms), // the program's *gnovm.Lazy
//		Project: project,
//		// MemStack, OnMemFrames and the rest, as the harness grows them.
//	})
//	ts.Tools()                          // all four, for the main agent
//	ts.Tool(gnovmtools.ListToolName)    // one, for an agent that only looks
//
// What a model reads is the earlier gnosh's, word for word: the tools'
// names, their descriptions and their arguments', every result and every
// error. The tools ask nobody for permission: each says what a call would do
// (core.Describer), and the harness decides; only deleting a VM is something
// a person must be asked about. No call may run beside a gnovm_run call, so
// none of the four says that it may run beside others, and nothing but its
// context ends a call: one that waits on other sessions may take minutes.
//
// A gnovm_run call on mem that pushed or popped a frame keeps those frames
// in its result's Meta under "frames" (MetaFrames, FramesOf), which is what
// a session's stack is derived from.
package gnovmtools

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/gnovm"
)

// The tools' names.
const (
	RunToolName  = "gnovm_run"
	NewToolName  = "gnovm_new"
	ListToolName = "gnovm_list"
	DropToolName = "gnovm_drop"
)

// maxResult caps a result's text.
const maxResult = 30_000

const runDescription = `Run Go-shaped code in a Gno virtual machine (the GnoVM, gno.land's interpreter for its Go dialect) and see its value and output.

- Input can be an expression ("len(s) * 2"), statements ("x := 3; x++"), declarations (func, type, var, const, with methods; in any order), imports, or a mix; a trailing expression's value is shown, and println and fmt.Println output is returned.
- Packages you use (fmt, strings, strconv, sort, ...) are imported for you, and x = v or x := v on a new name declares it.
- State stays between calls in the same VM, like a REPL. With no name you get your session's scratch VM; code with a package clause (package main + func main) runs as a whole program in a fresh VM, like go run.
- Redeclaring a name in a VM is refused: assign with =, use a new name, or pass reset: true (memory-only VMs).
- Gas: 1 gas is about 1 ns of work; a call that runs out of gas, memory or time is stopped and keeps nothing (the VM is as it was before the call).
- Gno is Go without goroutines, reflection, os, net, sync, encoding/json or unsafe; there is the Gno standard library (strings, strconv, sort, math, time, regexp, bytes, unicode, encoding/hex|base64|csv, crypto/sha256, ...) and fmt, which formats as Go's does.`

const newDescription = `Make a named Gno VM, or change one's settings. Names: a lowercase letter, then up to 31 lowercase letters, digits, - or _. Name your session's scratch VM "scratch" to change its settings.

- memory_mb: memory cap, 1 to 64 (default 16); it can only grow.
- gas: gas limit per call (default 1e9; 1 gas is about 1 ns).
- ns_per_gas: this VM's nanoseconds per gas (default 1), for its time estimates and limits.
- persist: keep the VM's state on disk across restarts of gnosh (default false); once on, it stays on.
- pinned: never evict it from memory to make room for others.
- description: what the VM is for, shown by gnovm_list.`

const listDescription = `List the Gno VMs of this project, with the host's memory, the VMs' memory budget and use, and each VM's settings, memory, gas used, journal size and state; and the packages a VM can import.`

const dropDescription = `Evict a Gno VM from memory (action "evict": it keeps its state and is rebuilt when next used), or delete it and its saved state (action "delete": asks the user first).`

// RunParams is gnovm_run's input.
type RunParams struct {
	Code  string  `json:"code" description:"The code to run."`
	Name  string  `json:"name,omitempty" description:"The VM to run it in. Leave it out for your scratch VM, or, for code with a package clause, a fresh VM."`
	Gas   float64 `json:"gas,omitempty" description:"Gas limit for this call (default the VM's, 1e9 = about 1 s of work)."`
	Reset bool    `json:"reset,omitempty" description:"Start a memory-only VM over before running."`
}

// NewParams is gnovm_new's input.
type NewParams struct {
	Name        string   `json:"name" description:"The VM's name; \"scratch\" for your session's scratch VM."`
	MemoryMB    *float64 `json:"memory_mb,omitempty" description:"Memory cap in MB, 1 to 64; only grows."`
	Gas         *float64 `json:"gas,omitempty" description:"Gas limit per call."`
	NsPerGas    *float64 `json:"ns_per_gas,omitempty" description:"Nanoseconds per gas for this VM, 0.1 to 100."`
	Persist     *bool    `json:"persist,omitempty" description:"Keep its state on disk across restarts."`
	Pinned      *bool    `json:"pinned,omitempty" description:"Never evict it to make room."`
	Description *string  `json:"description,omitempty" description:"What it is for."`
}

// ListParams is gnovm_list's input: none.
type ListParams struct{}

// DropParams is gnovm_drop's input.
type DropParams struct {
	Name   string `json:"name" description:"The VM."`
	Action string `json:"action" description:"\"evict\" or \"delete\"." enum:"evict,delete"`
}

// MemGoalsFunc is told the goals a call to mem changed, as the call's
// session sees them: what that session's task list should show.
type MemGoalsFunc func(ctx context.Context, session string, goals []gnovm.MemGoal)

// MemFramesFunc is told the frames a call to mem pushed and popped, in
// order, with the call's id, and answers what the call's result tells the
// model besides mem's own output: on a pop, what was removed from its
// context and what that changed ("" for nothing to add).
type MemFramesFunc func(ctx context.Context, sessionID, callID string, frames []gnovm.MemFrame) string

// MetaFrames is the key under which gnovm_run keeps, in a result's Meta, the
// frames a call to mem pushed and popped, from which a session's stack is
// derived (FRAMES.md). The value is a []gnovm.MemFrame, in the call's order;
// as JSON, a list of {"kind", "id", "purpose", "result", "thoughts",
// "session"}. It is there only when the call pushed or popped.
const MetaFrames = "frames"

// FramesOf is the frames a gnovm_run result keeps in its Meta (MetaFrames),
// whether the result comes straight from the tool or was stored as JSON and
// read back; nil when it keeps none.
func FramesOf(meta map[string]any) []gnovm.MemFrame {
	switch v := meta[MetaFrames].(type) {
	case nil:
		return nil
	case []gnovm.MemFrame:
		return v
	default:
		return framesFromJSON(v)
	}
}

// NamerFunc names the texts one call keeps in mem without naming them
// (mem.Todo(task), mem.Think(text), mem.Fact(text)), all at once so the
// names tell them apart: text → a short lowercase dashed name; a text left
// out is named by mem from its words.
type NamerFunc func(ctx context.Context, sessionID string, texts []string) map[string]string

// CheckCloseFunc checks closes a call to mem makes (gnovm.MemClose) before
// the call is kept: "" approves them all, anything else is the refusal the
// model is shown.
type CheckCloseFunc func(ctx context.Context, sessionID, callID string, closes []gnovm.MemClose) string

// runTool is gnovm_run, running in a project's VMs.
type runTool struct{ o *Options }

// Spec is what the model is told about gnovm_run.
func (t runTool) Spec() core.ToolSpec {
	return core.ToolSpec{Name: RunToolName, Description: runDescription, InputSchema: schemaOf(RunParams{})}
}

// Describe says what a call would run, for the harness's permission rules
// and for a person who is asked: the code is the preview, whole, under the
// VM's name. It never fails: a call whose arguments cannot be used is told
// so by Run, in the words the model has always read.
func (t runTool) Describe(call core.ToolCall, _ *core.ToolEnv) (core.Action, error) {
	var p RunParams
	_ = decodeParams(call.Input, &p)
	// With no name the host picks the session's scratch VM, or a fresh one
	// for a program: which, it says in the result.
	summary := "Run Gno code"
	switch name := strings.TrimSpace(p.Name); {
	case name == gnovm.MemVM:
		summary = "Run Gno code in mem"
	case name != "" && vmArg(name) == "scratch":
		summary = "Run Gno code in the session's scratch VM"
	case name != "":
		summary = "Run Gno code in VM " + name
	}
	if p.Reset {
		summary += ", starting the VM over first"
	}
	return core.Action{
		Kind:    core.ActionOther,
		Summary: summary,
		Preview: &core.Display{Kind: "command", Title: p.Lead(), Text: p.Code},
	}, nil
}

// Lead is what goes before a call's code where a person is shown the call:
// the VM's name, unless it is mem or a scratch VM, then the call's gas limit
// and its reset, when it sets them.
func (p RunParams) Lead() string {
	var parts []string
	if name := strings.TrimSpace(p.Name); name != "" && name != gnovm.MemVM && vmArg(name) != "scratch" {
		parts = append(parts, name)
	}
	if gas, err := gasParam(p.Gas); err != nil {
		parts = append(parts, fmt.Sprintf("gas %g", p.Gas))
	} else if gas != 0 {
		parts = append(parts, "gas "+count(gas))
	}
	if p.Reset {
		parts = append(parts, "reset")
	}
	return strings.Join(parts, " · ")
}

// heading is what heads a call where a person is shown its result: the
// call's code, whole, after its Lead.
func (p RunParams) heading() string {
	if lead := p.Lead(); lead != "" {
		return lead + " · " + p.Code
	}
	return p.Code
}

// shown is a call's result as a person is shown it: the call's heading over
// what the model reads.
func (p RunParams) shown(res core.ToolResult) core.ToolResult {
	res.Display = &core.Display{Kind: "command", Title: p.heading(), Text: core.TextOf(res.Content)}
	return res
}

// Run runs the call's code and returns what the model reads of it. A failure
// of the code, a refusal and a VM that cannot be used are results the model
// reads; the error is for a harness that gave the tool nothing to work with.
// Nothing but ctx ends a call: one whose mem.Next() waits for other sessions
// may take minutes.
func (t runTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	var p RunParams
	if err := decodeParams(call.Input, &p); err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	res, err := t.run(ctx, p, call, env)
	if err != nil {
		return core.ToolResult{}, err
	}
	return p.shown(res), nil
}

func (t runTool) run(ctx context.Context, p RunParams, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	o := t.o
	if strings.TrimSpace(p.Code) == "" {
		return core.ErrorResult("code is required"), nil
	}
	gas, err := gasParam(p.Gas)
	if err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	project, session, err := o.place(ctx, env, RunToolName)
	if err != nil {
		return core.ToolResult{}, err
	}
	c, err := o.client()
	if err != nil {
		return core.ErrorResult("the Gno VMs are not available: " + err.Error()), nil
	}
	req := gnovm.RunRequest{Project: project, Session: session, Name: p.Name, Code: p.Code, Gas: gas, Reset: p.Reset, Req: call.ID, SubAgent: o.SubAgent != nil && o.SubAgent(ctx, session)}
	if o.BoundGoal != nil && p.Name == gnovm.MemVM {
		req.Goal = o.BoundGoal(ctx, session)
	}
	if o.MemStack != nil && p.Name == gnovm.MemVM {
		req.Frames = o.MemStack(ctx, session)
	}
	if o.MemFresh != nil && p.Name == gnovm.MemVM && len(req.Frames) > 0 && (strings.Contains(p.Code, "Pop(") || strings.Contains(p.Code, "Done(")) {
		req.Fresh = o.MemFresh(ctx, session, call.ID)
	}
	if o.Namer != nil && p.Name == gnovm.MemVM {
		if texts := unnamedTexts(p.Code); len(texts) > 0 {
			req.Names = o.Namer(ctx, session, texts)
		}
	}
	req.CheckCloses = o.CheckCloses != nil && p.Name == gnovm.MemVM
	res, err := c.Run(ctx, req)
	// mem kept nothing of a call with a close to check: check them,
	// then run the same request, names and all, with them approved.
	// Each run can only bring closes not approved yet; a todo
	// approved once in the call is not asked about again, though
	// its result changed between runs (one made of the time).
	checked := false
	for err == nil && res != nil && res.Err != nil && len(res.Closes) > 0 && req.CheckCloses {
		var fresh []gnovm.MemClose
		for _, cl := range res.Closes {
			if !slices.Contains(req.Checked, cl.Checked()) && !slices.Contains(req.Checked, cl.AnyResult()) {
				fresh = append(fresh, cl)
			}
		}
		if len(fresh) == 0 {
			// mem asked again about what it was told was approved:
			// never so; stop rather than loop.
			break
		}
		checked = true
		if refusal := o.CheckCloses(ctx, session, call.ID, fresh); refusal != "" {
			if o.ClosesKept != nil {
				o.ClosesKept(context.WithoutCancel(ctx), session, call.ID, false)
			}
			// What the run printed was of a call never kept: a "done"
			// there would read as kept.
			res.Err = errors.New(refusal)
			res.Closes, res.Output = nil, ""
			return core.ErrorResult(renderRun(res, "")), nil
		}
		for _, cl := range fresh {
			req.Checked = append(req.Checked, cl.Checked(), cl.AnyResult())
		}
		res, err = c.Run(ctx, req)
	}
	if err == nil && res == nil {
		return core.ToolResult{}, errors.New("gnovmtools: the GnoVM client answered gnovm_run with neither a result nor an error")
	}
	if checked && o.ClosesKept != nil {
		o.ClosesKept(context.WithoutCancel(ctx), session, call.ID, err == nil && res.Err == nil)
	}
	if err != nil && strings.Contains(err.Error(), "worker ended before it replied") {
		if p.Name == gnovm.MemVM {
			return memOutcome(ctx, c, o, project, session, call.ID, err), nil
		}
		return core.ErrorResult(unknownOutcome(ctx, c, project, session, p, call.ID, err)), nil
	}
	if err != nil {
		return core.ErrorResult(refusalText(err)), nil
	}
	if res.Err != nil {
		return core.ErrorResult(renderRun(res, "")), nil
	}
	// The frames are the session's stack whatever happens to the
	// turn: Esc after a call kept its pop or push must not lose them.
	var frames string
	if o.OnMemFrames != nil && len(res.Frames) > 0 {
		frames = o.OnMemFrames(context.WithoutCancel(ctx), session, call.ID, res.Frames)
	}
	text := renderRun(res, frames)
	// So is what else mem kept: the todos shown, an erasure's
	// copies, the rules.
	kept := context.WithoutCancel(ctx)
	if o.OnMemGoals != nil && len(res.MemGoals) > 0 {
		o.OnMemGoals(kept, session, res.MemGoals)
	}
	if o.OnMemErased != nil && res.Erased != nil {
		o.OnMemErased(kept, session, *res.Erased)
	}
	if o.OnMemRules != nil && len(res.Rules) > 0 {
		o.OnMemRules(kept, session, res.Rules)
	}
	if len(res.Frames) > 0 {
		return withFrames(core.TextResult(text), res.Frames), nil
	}
	return core.TextResult(text), nil
}

// withFrames keeps the frames a call to mem pushed and popped with its
// result, where the harness stores them with the result's row.
func withFrames(res core.ToolResult, frames []gnovm.MemFrame) core.ToolResult {
	res.Meta = map[string]any{MetaFrames: frames}
	return res
}

// unnamedTexts is the texts code keeps in mem without naming them: the one
// string literal of each Think(…), on mem or on a Frame, and of each
// mem.Fact(…) called with a single argument that is not a fact's name, and
// every string literal of each Todo(…), on mem or on a Frame, and
// mem.Later(…), a plan's task and its todos; in order, each once. Code that
// does not parse as statements has none: mem names the texts from their
// words then.
func unnamedTexts(code string) []string {
	f, err := parser.ParseFile(token.NewFileSet(), "", "package p\nfunc _() {\n"+code+"\n}\n", parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var texts []string
	seen := map[string]bool{}
	add := func(arg ast.Expr) {
		lit, ok := arg.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		if text, err := strconv.Unquote(lit.Value); err == nil && text != "" && !seen[text] {
			seen[text] = true
			texts = append(texts, text)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, _ := sel.X.(*ast.Ident)
		onMem := id != nil && id.Name == "mem"
		switch {
		case sel.Sel.Name == "Todo" || sel.Sel.Name == "Later" && onMem:
			for _, a := range call.Args {
				add(a)
			}
		case len(call.Args) != 1:
		case sel.Sel.Name == "Think":
			add(call.Args[0])
		case sel.Sel.Name == "Fact" && onMem && !memNameLike(call.Args[0]):
			add(call.Args[0])
		}
		return true
	})
	return texts
}

// memNameLike reports whether arg is a string literal that reads as a
// name, `cmd/test`: mem.Fact of one reads a fact rather than keeping one.
func memNameLike(arg ast.Expr) bool {
	lit, ok := arg.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	text, err := strconv.Unquote(lit.Value)
	return err == nil && memNameRe.MatchString(text)
}

var memNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*(/[a-z0-9]+(-[a-z0-9]+)*)*$`)

// gasParam turns a JSON number (a model writes 1e9) into a gas limit.
func gasParam(g float64) (int64, error) {
	switch {
	case g == 0:
		return 0, nil
	case g < 1 || g > 1e11 || math.IsNaN(g):
		return 0, errors.New("gas must be between 1 and 1e11")
	}
	return int64(g), nil
}

// unknownOutcome resolves a run whose worker died before replying: the next
// worker is asked whether the call was kept.
func unknownOutcome(ctx context.Context, c Client, project, session string, p RunParams, req string, err error) string {
	name := p.Name
	if name == "" || name == "scratch" {
		name = gnovm.ScratchName(session)
	}
	kept, kerr := c.Kept(ctx, project, name, req)
	switch {
	case kerr != nil:
		return err.Error() + "; asking whether it was kept failed too: " + kerr.Error()
	case kept:
		return err.Error() + ". It was kept: the VM has its effects, but its output was lost."
	}
	return err.Error() + ". It was not kept: nothing from it is in the VM."
}

// memOutcome resolves a mem call whose worker died before replying: the
// next worker is asked whether mem kept it, and if it did, the frames it
// pushed and popped reach the session's stack all the same, as they would
// have with its reply.
func memOutcome(ctx context.Context, c Client, o *Options, project, session, req string, err error) core.ToolResult {
	frames, kept, kerr := c.KeptFrames(context.WithoutCancel(ctx), project, req)
	switch {
	case kerr != nil:
		return core.ErrorResult(err.Error() + "; asking whether it was kept failed too: " + kerr.Error())
	case !kept:
		return core.ErrorResult(err.Error() + ". It was not kept: nothing from it is in mem.")
	}
	text := err.Error() + ". It was kept: mem has its effects, but its output was lost; mem.Show() says where you stand."
	if len(frames) == 0 {
		return core.TextResult(text)
	}
	if o.OnMemFrames != nil {
		if f := o.OnMemFrames(context.WithoutCancel(ctx), session, req, frames); f != "" {
			text += "\n" + f
		}
	}
	return withFrames(core.TextResult(text), frames)
}

// refusalText is the text for a call that did not run.
func refusalText(err error) string {
	return "ERROR — nothing ran: " + err.Error()
}

// renderRun is a run's result as the model reads it: the value and output
// first, then what gnosh adds about frames (what a pop removed), then one
// status line, then notes.
func renderRun(res *gnovm.RunResult, frames string) string {
	var b strings.Builder
	if res.Err != nil {
		fmt.Fprintf(&b, "ERROR — nothing from this call was kept: %s\n", res.Err)
		if stack := gnovm.ErrorStack(res.Err); stack != "" {
			fmt.Fprintf(&b, "%s\n", stack)
		}
		if hint := nextStep(res); hint != "" {
			fmt.Fprintf(&b, "%s\n", hint)
		}
		if res.Output != "" {
			fmt.Fprintf(&b, "output before it stopped:\n%s\n", strings.TrimRight(res.Output, "\n"))
		}
	} else {
		if res.Value != "" {
			b.WriteString(res.Value + "\n")
		}
		if res.Output != "" {
			if res.Value != "" {
				b.WriteString("output:\n")
			}
			b.WriteString(strings.TrimRight(res.Output, "\n") + "\n")
		}
	}
	if frames != "" {
		b.WriteString(strings.TrimRight(frames, "\n") + "\n")
	}
	b.WriteString(statusLine(res) + "\n")
	for _, n := range res.Notes {
		b.WriteString("note: " + n + "\n")
	}
	return truncate(b.String())
}

func statusLine(res *gnovm.RunResult) string {
	state := "ok"
	if res.Err != nil {
		state = "failed"
	}
	vm := "VM " + res.VM
	if res.Program {
		vm = "a fresh program VM"
	}
	est := time.Duration(float64(res.Gas) * res.NsPerGas)
	kept := "nothing to keep"
	switch {
	case res.Journaled:
		kept = fmt.Sprintf("kept as entry %d", res.Seq)
	case res.Err != nil:
		kept = "nothing kept"
	case res.Program:
		kept = "thrown away after"
	}
	if res.VM == gnovm.MemVM {
		// mem is not a VM with a gas budget and a memory cap of its own.
		return fmt.Sprintf("%s · mem · ran %s · %s", state, res.Wall.Round(time.Millisecond), kept)
	}
	return fmt.Sprintf("%s · %s · %s of %s gas (≈%s at %g ns/gas; ran %s) · memory %s of %s · %s",
		state, vm, count(res.Gas), count(res.GasLimit), est.Round(time.Microsecond), res.NsPerGas,
		res.Wall.Round(time.Microsecond), mb(res.MemUsed), mb(res.MemCap), kept)
}

// nextStep names the call that gets past a limit.
func nextStep(res *gnovm.RunResult) string {
	switch gnovm.LimitKind(res.Err) {
	case "gas":
		return fmt.Sprintf("Next: give it more gas, e.g. \"gas\": %d, or do less per call.", min(res.GasLimit*4, 100_000_000_000))
	case "memory":
		if res.Program {
			return "Next: keep less in memory; a program VM has the default cap."
		}
		capMB := int(res.MemCap >> 20)
		if capMB >= 64 {
			return "Next: keep less in memory; 64 MB is the most a VM may have."
		}
		return fmt.Sprintf("Next: raise the cap with gnovm_new {\"name\": %q, \"memory_mb\": %d}, or keep less.", vmArg(res.VM), min(capMB*2, 64))
	case "wall":
		return "Next: do less per call; the call ran out of time before it ran out of gas."
	}
	return ""
}

// vmArg is how the model names a VM in gnovm_new: its scratch VM as "scratch".
func vmArg(vm string) string {
	if strings.HasPrefix(vm, "scratch-") {
		return "scratch"
	}
	return vm
}

func count(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.3gG", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.3gM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.3gk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func mb(n int64) string { return fmt.Sprintf("%.1f MB", float64(n)/(1<<20)) }

// truncate cuts a result at maxResult bytes, and says how many more there
// were. It cuts between characters, never inside one.
func truncate(s string) string {
	if len(s) <= maxResult {
		return s
	}
	cut := maxResult
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n… (%d more characters not shown)", len(s)-cut)
}

// newTool is gnovm_new.
type newTool struct{ o *Options }

// Spec is what the model is told about gnovm_new.
func (t newTool) Spec() core.ToolSpec {
	return core.ToolSpec{Name: NewToolName, Description: newDescription, InputSchema: schemaOf(NewParams{})}
}

// Describe says which VM a call would make or change. It never fails: a
// call whose arguments cannot be used is told so by Run.
func (t newTool) Describe(call core.ToolCall, _ *core.ToolEnv) (core.Action, error) {
	var p NewParams
	_ = decodeParams(call.Input, &p)
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return core.Action{Kind: core.ActionOther, Summary: "Make a Gno VM, or change one's settings"}, nil
	}
	return core.Action{Kind: core.ActionOther, Summary: "Make Gno VM " + name + ", or change its settings"}, nil
}

// Run makes the VM, or changes the settings the call gives of one that
// exists, and says what it has now.
func (t newTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	o := t.o
	var p NewParams
	if err := decodeParams(call.Input, &p); err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	if strings.TrimSpace(p.Name) == "" {
		return core.ErrorResult("name is required"), nil
	}
	project, session, err := o.place(ctx, env, NewToolName)
	if err != nil {
		return core.ToolResult{}, err
	}
	req := gnovm.NewRequest{
		Project: project, Session: session, Name: p.Name,
		Persist: p.Persist, Pinned: p.Pinned, Description: p.Description, NsPerGas: p.NsPerGas,
	}
	if p.MemoryMB != nil {
		m := int(*p.MemoryMB)
		req.MemoryMB = &m
	}
	if p.Gas != nil {
		g, err := gasParam(*p.Gas)
		if err != nil {
			return core.ErrorResult(err.Error()), nil
		}
		req.Gas = &g
	}
	c, err := o.client()
	if err != nil {
		return core.ErrorResult("the Gno VMs are not available: " + err.Error()), nil
	}
	info, created, err := c.New(ctx, req)
	if err != nil {
		return core.ErrorResult(refusalText(err)), nil
	}
	verb := "VM " + info.Name + " now has"
	if created {
		verb = "Made VM " + info.Name + " with"
	}
	return core.TextResult(fmt.Sprintf("%s %s.", verb, describe(info.Options))), nil
}

func describe(o gnovm.VMOptions) string {
	parts := []string{
		fmt.Sprintf("memory_mb %d", o.MemoryMB),
		fmt.Sprintf("gas %s per call", count(o.Gas)),
		fmt.Sprintf("%g ns/gas", o.NsPerGas),
	}
	if o.Persist {
		parts = append(parts, "persistent")
	} else {
		parts = append(parts, "memory only")
	}
	if o.Pinned {
		parts = append(parts, "pinned")
	}
	return strings.Join(parts, ", ")
}

// listTool is gnovm_list.
type listTool struct{ o *Options }

// Spec is what the model is told about gnovm_list.
func (t listTool) Spec() core.ToolSpec {
	return core.ToolSpec{Name: ListToolName, Description: listDescription, InputSchema: schemaOf(ListParams{})}
}

// Describe says that a call only lists.
func (t listTool) Describe(core.ToolCall, *core.ToolEnv) (core.Action, error) {
	return core.Action{Kind: core.ActionOther, Summary: "List this project's Gno VMs"}, nil
}

// Run lists the project's VMs. It takes no arguments, and reads none.
func (t listTool) Run(ctx context.Context, _ core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	o := t.o
	project, _, err := o.place(ctx, env, ListToolName)
	if err != nil {
		return core.ToolResult{}, err
	}
	c, err := o.client()
	if err != nil {
		return core.ErrorResult("the Gno VMs are not available: " + err.Error()), nil
	}
	list, err := c.List(ctx, project)
	if err != nil {
		return core.ErrorResult(refusalText(err)), nil
	}
	return core.TextResult(truncate(renderList(list, c.Deaths()))), nil
}

func renderList(l gnovm.ListResult, deaths []string) string {
	var b strings.Builder
	if l.ProbeErr != "" {
		fmt.Fprintf(&b, "host memory: unknown (%s)\n", l.ProbeErr)
	} else {
		fmt.Fprintf(&b, "host memory: %s available of %s (%s)\n", mb(int64(l.Memory.Available)), mb(int64(l.Memory.Total)), l.Memory.Source)
	}
	fmt.Fprintf(&b, "VM budget: %s, %s in use (standard library %s)\n", mb(l.Budget), mb(l.Used), mb(l.BaseBytes))
	if len(l.VMs) == 0 {
		b.WriteString("no VMs yet: gnovm_run makes your scratch VM, gnovm_new a named one\n")
	}
	for _, vm := range l.VMs {
		state := "on disk"
		if vm.Resident {
			state = fmt.Sprintf("in memory (%s of its cap, %s counted)", mb(vm.MemUsed), mb(vm.Charge))
		}
		if vm.Broken != "" {
			state = "BROKEN: " + vm.Broken
		}
		used := "never"
		if !vm.LastUsed.IsZero() {
			used = vm.LastUsed.Format(time.DateTime)
		}
		fmt.Fprintf(&b, "- %s: %s; %d entries (%s), %s gas used, last used %s; %s",
			vm.Name, describe(vm.Options), vm.Entries, kb(vm.JournalSize), count(vm.LifetimeGas), used, state)
		if vm.Options.Description != "" {
			fmt.Fprintf(&b, " — %s", vm.Options.Description)
		}
		b.WriteString("\n")
	}
	if len(deaths) > 0 {
		fmt.Fprintf(&b, "the VM worker ended %d time(s) in this gnosh; last: %s\n", len(deaths), deaths[len(deaths)-1])
	}
	fmt.Fprintf(&b, "importable: fmt, %s\n", strings.Join(gnovm.ImportablePackages(), ", "))
	b.WriteString("Built on gno.land (https://gno.land) · built on gnosh (https://gnosh.ai)\n")
	return b.String()
}

func kb(n int64) string { return fmt.Sprintf("%d KB", cmp.Or(n>>10, 0)) }

// dropTool is gnovm_drop. Deleting asks the user first: the tool says so in
// what Describe returns, and the harness's permission rules do the asking.
type dropTool struct{ o *Options }

// Spec is what the model is told about gnovm_drop.
func (t dropTool) Spec() core.ToolSpec {
	return core.ToolSpec{Name: DropToolName, Description: dropDescription, InputSchema: schemaOf(DropParams{})}
}

// Describe says what a call would do to which VM. For a delete it is the
// line a person is asked with before the VM is deleted, and the Action says
// that a person must be asked (Confirm), whatever the rules or the mode
// would let run: a deleted VM's saved state cannot be got back. It fails
// only for a delete that could not run, with Run's own words, so that
// nobody is asked about one; any other call whose arguments cannot be used
// is told so by Run.
func (t dropTool) Describe(call core.ToolCall, _ *core.ToolEnv) (core.Action, error) {
	var p DropParams
	err := decodeParams(call.Input, &p)
	if err == nil {
		err = p.check()
	}
	act := core.Action{Kind: core.ActionOther, Summary: "Evict or delete a Gno VM"}
	switch {
	case p.Action == "delete" && err != nil:
		act.Summary = "Delete a Gno VM and its saved state"
		act.Confirm = true
		return act, err
	case p.Action == "delete":
		act.Summary = fmt.Sprintf("Delete Gno VM %s and its saved state", p.Name)
		act.Confirm = true
	case p.Action == "evict" && err == nil:
		act.Summary = fmt.Sprintf("Evict Gno VM %s from memory; it keeps its state", p.Name)
	}
	return act, nil
}

// check says what is wrong with a call's arguments, if anything.
func (p DropParams) check() error {
	if p.Name == "" {
		return errors.New("name is required")
	}
	if p.Action != "evict" && p.Action != "delete" {
		return errors.New(`action must be "evict" or "delete"`)
	}
	return nil
}

// Run evicts the VM from memory, or deletes it. Whether a delete may run
// was settled before the call came here.
func (t dropTool) Run(ctx context.Context, call core.ToolCall, env *core.ToolEnv) (core.ToolResult, error) {
	o := t.o
	var p DropParams
	if err := decodeParams(call.Input, &p); err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	if err := p.check(); err != nil {
		return core.ErrorResult(err.Error()), nil
	}
	project, session, err := o.place(ctx, env, DropToolName)
	if err != nil {
		return core.ToolResult{}, err
	}
	c, err := o.client()
	if err != nil {
		return core.ErrorResult("the Gno VMs are not available: " + err.Error()), nil
	}
	name := p.Name
	if name == "scratch" {
		name = gnovm.ScratchName(session)
	}
	if err := c.Drop(ctx, project, name, p.Action); err != nil {
		return core.ErrorResult(refusalText(err)), nil
	}
	if p.Action == "evict" {
		return core.TextResult("Evicted VM " + name + " from memory; it is rebuilt from its journal when next used."), nil
	}
	return core.TextResult("Deleted VM " + name + "."), nil
}
