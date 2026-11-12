// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package api

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// MemVM is the name the tools give mem, the agent's memory: not a VM
// but a realm on disk, made when first used.
const MemVM = "mem"

// RedactedText is what a Redact puts in place of a secret.
const RedactedText = "(redacted)"

// MinErased is the shortest text Scrub replaces wherever it is: a
// shorter one, a step titled "a", would mangle text that never held it.
// (Redact refuses a secret under 6 bytes, for the same reason.)
const MinErased = 6

// RunRequest is one gnovm_run call.
type RunRequest struct {
	// Project is the project's directory; VMs belong to a project.
	Project string
	// Session is the calling session's id, for its scratch VM.
	Session string
	// Name is the VM; "" means the session's scratch VM, or, for code
	// with a package clause, a fresh VM for just this call.
	Name string
	Code string
	// Gas is the call's gas limit; 0 means the VM's.
	Gas int64
	// Reset starts a memory-only VM over before running.
	Reset bool
	// Req identifies the call, so its outcome can be asked about later.
	Req string
	// Goal is the goal in mem the user's /goal binds the session to, which
	// the session's own mem calls may not drop or forget.
	Goal string `json:",omitempty"`
	// Frames is the session's stack of frames in mem, bottom to top: the
	// frames it has pushed and not popped (FRAMES.md), which the agent
	// derives from its own rows.
	Frames []string `json:",omitempty"`
	// Fresh is the frame on top of Frames when the agent's round that
	// entered it was its last: nothing came between that round and this
	// call's. mem refuses to pop it then, for the model never did it.
	Fresh string `json:",omitempty"`
	// Names are names made for the texts the call keeps without naming
	// them (mem.Think(text), mem.Fact(text)), by text: the agent asks its
	// small model before the call; the realm falls back to the words.
	Names map[string]string `json:",omitempty"`
	// WaitPipe is the wake pipe the client made, pending in mem's
	// waiters/, for an input whose Next may wait for working sessions;
	// SubAgent marks a sub-agent's session, which never waits (PARALLEL.md).
	WaitPipe string `json:",omitempty"`
	SubAgent bool   `json:",omitempty"`
	// CheckCloses has mem keep nothing of an input that closes a todo with
	// nothing under it as done, and hand the close to the agent in
	// RunResult.Closes, unless Checked names it (MemClose.Checked): the
	// agent checks it against what was done in the todo, then runs the
	// input again with it in Checked, or refuses it (CLOSE-CHECK.md).
	CheckCloses bool     `json:",omitempty"`
	Checked     []string `json:",omitempty"`
}

// RunResult is what a call did.
type RunResult struct {
	VM        string
	Value     string
	Output    string
	Gas       int64
	GasLimit  int64
	NsPerGas  float64
	Wall      time.Duration
	MemUsed   int64
	MemCap    int64
	Journaled bool
	Seq       int64
	// Program is set when the code ran as a program in a fresh VM that
	// was thrown away after.
	Program bool
	Notes   []string
	// MemGoals are the goals a call to mem changed, as its session sees
	// them, for the session's task list.
	MemGoals []MemGoal `json:",omitempty"`
	// Erased is what a call to mem erased, for gnosh's own copies of mem's
	// text (gnovm_run hands it to OnMemErased).
	Erased *MemErasure `json:",omitempty"`
	// Rules are the rules a call to mem set, changed or removed, which the
	// user is told of (gnovm_run hands them to OnMemRules): every session
	// is given them as the user's own words.
	Rules []MemRule `json:",omitempty"`
	// Frames are the frames a call to mem pushed and popped, in order
	// (gnovm_run keeps them with its result and hands them to OnMemFrames).
	Frames []MemFrame `json:",omitempty"`
	// Waits is what the input's Next waits for: the client waits on its
	// pipe until mem changes or a holder's turn ends, then runs mem.Next()
	// again (memwait.go).
	Waits []MemWait `json:",omitempty"`
	// Closes are the closes the agent is to check before the input is kept
	// (RunRequest.CheckCloses); with them, Err is set and nothing was kept.
	Closes []MemClose `json:",omitempty"`
	// Err is the run's failure; nothing from the call was kept. (The wire
	// carries it apart, keeping its kind; see WireRun.)
	Err error `json:"-"`
}

// NewRequest is one gnovm_new call.
type NewRequest struct {
	Project string
	// Session is the calling session: it may name its own scratch VM, as
	// "scratch" or by its full name, to change its settings.
	Session     string
	Name        string
	MemoryMB    *int
	Gas         *int64
	NsPerGas    *float64
	Persist     *bool
	Pinned      *bool
	Description *string
}

// VMOptions are a VM's settings, kept in its machine.json.
type VMOptions struct {
	MemoryMB    int     `json:"memory_mb"`
	Gas         int64   `json:"gas"`
	NsPerGas    float64 `json:"ns_per_gas"`
	Persist     bool    `json:"persist"`
	Pinned      bool    `json:"pinned,omitempty"`
	Description string  `json:"description,omitempty"`
}

// VMInfo describes a VM.
type VMInfo struct {
	Name        string
	Options     VMOptions
	Scratch     bool
	Resident    bool
	MemUsed     int64
	Charge      int64
	Entries     int64
	LifetimeGas int64
	JournalSize int64
	LastUsed    time.Time
	Broken      string
}

// ListResult is the state of the host and of a project's VMs.
type ListResult struct {
	Memory    HostMemory
	ProbeErr  string
	Budget    int64
	Used      int64
	BaseBytes int64
	VMs       []VMInfo
}

// HostMemory is what the host gnosh runs on can give it.
type HostMemory struct {
	Total       uint64 // bytes; 0 when unknown
	Available   uint64 // bytes obtainable without swapping; 0 when unknown
	CgroupLimit uint64 // tightest cgroup memory.max, in bytes; 0 when none
	Source      string // how Available was found, shown by gnovm_list
}

// MemFrame is a frame a call to mem pushed or popped. gnosh keeps
// them with the call's tool result, derives a session's stack of frames
// from them, and on a pop removes from the model's context everything since
// the push (FRAMES.md).
type MemFrame struct {
	// Kind is "push" or "pop".
	Kind string `json:"kind"`
	// Id is the frame: `fix-flaky`, `fix-flaky/find-cause`.
	Id string `json:"id"`
	// Purpose is the frame's purpose, for the stack line and the sidebar.
	Purpose string `json:"purpose,omitempty"`
	// Result is a pop's result.
	Result string `json:"result,omitempty"`
	// Thoughts is how many thoughts the frame holds when it is popped.
	Thoughts int `json:"thoughts,omitempty"`
	// Session is the session that made it.
	Session string `json:"session"`
}

// MemClose is a close of a todo with nothing under it, as done, that mem
// asks gnosh to check before it keeps the input making it
// (RunRequest.CheckCloses, CLOSE-CHECK.md).
type MemClose struct {
	// Id is the todo, Title its task, Result what the input closes it
	// with ("" when it gives none: Done of a todo not on the stack), and
	// Thoughts the thoughts kept on it, one a line.
	Id       string `json:"id"`
	Title    string `json:"title,omitempty"`
	Result   string `json:"result,omitempty"`
	Thoughts string `json:"thoughts,omitempty"`
	// Entered is set when the input itself entered the todo, which was not
	// on the session's stack before it; Worked, when the input changed mem
	// after entering it, beyond entering and closing it.
	Entered bool `json:"entered,omitempty"`
	Worked  bool `json:"worked,omitempty"`
}

// Checked is how RunRequest.Checked names a close gnosh approved; AnyResult,
// a todo approved whatever it is closed with in the rest of the call (a
// result made of the time differs on every run).
func (c MemClose) Checked() string   { return c.Id + "\x00" + c.Result }
func (c MemClose) AnyResult() string { return c.Id + "\x00*" }

// MemGoal is a goal a call to mem changed, as a session sees it.
type MemGoal struct {
	Name   string    `json:"name"`
	Title  string    `json:"title"`
	Status string    `json:"status"`
	Next   string    `json:"next"`
	Steps  []MemStep `json:"steps"`
}

// MemStep is a step of a MemGoal.
type MemStep struct {
	Name   string `json:"name"`
	Title  string `json:"title"`
	Status string `json:"status"`
	// For is "user" for a question or a request for the user; Doing is the
	// session that started the step.
	For   string `json:"for"`
	Doing string `json:"doing"`
}

// MemRule is a rule a call to mem set, changed or removed.
type MemRule struct {
	Name string `json:"name"`
	Text string `json:"text,omitempty"`
	Was  string `json:"was,omitempty"`
}

// MemWait is a todo a waiting Next waits for, and the session holding it.
type MemWait struct {
	Id      string `json:"id"`
	Session string `json:"session"`
}

// MemErasure is what a Redact or a Forget took out of mem, for gnosh to
// take out of its own copies of mem's text too (MEMPLAN §5): the <mem>
// block saved with a session's first prompt, a session's /goal binding, its
// task list, its summaries. It holds the very text erased, so it is never
// logged and never written anywhere.
type MemErasure struct {
	// Texts are the texts erased, each with what took its place: a secret,
	// "(redacted)"; a forgotten item's, fact's or rule's text,
	// "(forgotten)".
	Texts map[string]string `json:"texts,omitempty"`
	// Names are the items, facts and rules forgotten, by the names they
	// had; Renamed are the goals renamed, old name to new: a goal forgotten,
	// or whose name held a secret, is forgotten-<n> now.
	Names   []string          `json:"names,omitempty"`
	Renamed map[string]string `json:"renamed,omitempty"`
}

// Scrub takes what e erased out of s, a copy of mem's text: a line that
// names something forgotten, in backticks as mem prints names, goes; a goal
// renamed for a secret in its name is named anew; and each erased text, as
// typed or Go-quoted, gives way to what took its place.
func (e MemErasure) Scrub(s string) string {
	if len(e.Names) > 0 && strings.Contains(s, "`") {
		lines := strings.Split(s, "\n")
		kept := lines[:0]
		for _, line := range lines {
			if !e.namesForgotten(line) {
				kept = append(kept, line)
			}
		}
		s = strings.Join(kept, "\n")
	}
	for old, new := range e.Renamed {
		s = strings.ReplaceAll(s, "`"+old+"`", "`"+new+"`")
		s = strings.ReplaceAll(s, "`"+old+"/", "`"+new+"/")
	}
	texts := make([]string, 0, len(e.Texts))
	for t := range e.Texts {
		if len(t) >= MinErased {
			texts = append(texts, t)
		}
	}
	// The longest first, so a text that holds another goes whole.
	sort.Slice(texts, func(i, j int) bool { return len(texts[i]) > len(texts[j]) })
	for _, t := range texts {
		s = strings.ReplaceAll(s, t, e.Texts[t])
		if q := QuotedInner(t); q != t {
			s = strings.ReplaceAll(s, q, e.Texts[t])
		}
	}
	return s
}

// namesForgotten reports whether line names, in backticks, something e
// forgot, or a step under it.
func (e MemErasure) namesForgotten(line string) bool {
	for _, n := range e.Names {
		if strings.Contains(line, "`"+n+"`") || strings.Contains(line, "`"+n+"/") {
			return true
		}
	}
	return false
}

// Secrets are the texts e redacted, which a session's visible messages may
// still hold: Scrub leaves those alone.
func (e MemErasure) Secrets() []string {
	var out []string
	for t, with := range e.Texts {
		if with == RedactedText && len(t) >= MinErased {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// QuotedInner is s as it sits inside a Go-quoted string: strconv.Quote
// without the quotes.
func QuotedInner(s string) string {
	q := strconv.Quote(s)
	return q[1 : len(q)-1]
}

// GoalBinding is a session's binding to a goal in its project's mem.
type GoalBinding struct {
	// Condition is what the user typed after /goal.
	Condition string `json:"condition"`
	// Instance is the id of the mem the goal is in; a mem made anew has
	// another.
	Instance string `json:"instance"`
	Name     string `json:"name"`
	Title    string `json:"title"`
	// Added says that binding added the goal, which unbinding then drops.
	Added bool `json:"added,omitempty"`
	// Checks is how many times the judge was asked, and Verdict what it
	// last said.
	Checks  int    `json:"checks,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	// Asked is what held the goal back when a turn last ended on the
	// checkpoint question: the same at the next turn's end ends the turn.
	Asked string `json:"asked,omitempty"`
}

// GoalTurn is what gnosh does with a session's goal as a turn ends.
type GoalTurn struct {
	// Do is one of:
	//   - "continue": send Prompt, hidden, and go on;
	//   - "checkpoint": send Prompt, which asks whether there is a real
	//     reason to stop, and go on;
	//   - "end": end the turn, the goal still bound; Notice says why;
	//   - "judge": the goal is closed as done: ask the judge whether the
	//     condition holds, showing it Show;
	//   - "rebind": mem was made anew; bind the condition again;
	//   - "clear": unbind; Notice says why.
	Do     string `json:"do"`
	Prompt string `json:"prompt,omitempty"`
	Notice string `json:"notice,omitempty"`
	// Show is mem.Show(name): the goal's steps, results and notes, the
	// agent's own claims, for the judge.
	Show string `json:"show,omitempty"`
}

// ClosedGoal is a goal a session closed as done, for the judge.
type ClosedGoal struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	// Seq is the journal entry that closed it: closed again after it was
	// reopened, it is judged again.
	Seq int64 `json:"seq"`
	// Show is mem.Show(name): its steps, results and notes, the agent's
	// own claims.
	Show string `json:"show"`
}
