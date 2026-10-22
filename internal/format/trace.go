package format

import (
	"encoding/json"
	"fmt"
	"io"
)

// ToolTrace prints the --verbose trace of a non-interactive run's tool
// calls: a line for each call when it is made, and a second line, marked,
// when its result says it went wrong: "(failed)" for a tool error, and
// "(exit N)" for a command that exited non-zero. A call is printed once
// however often the message carrying it is republished. A nil *ToolTrace
// traces nothing.
type ToolTrace struct {
	out    io.Writer
	lines  map[string]string // call ID -> "name summary"
	marked map[string]bool
}

// NewToolTrace returns a trace that writes to out.
func NewToolTrace(out io.Writer) *ToolTrace {
	return &ToolTrace{out: out, lines: map[string]string{}, marked: map[string]bool{}}
}

// Call records a tool call whose input is complete, printing it the first
// time it is seen.
func (t *ToolTrace) Call(id, name, summary string) {
	if t == nil || id == "" {
		return
	}
	if _, seen := t.lines[id]; seen {
		return
	}
	line := name
	if summary != "" {
		line += " " + summary
	}
	t.lines[id] = line
	_, _ = fmt.Fprintf(t.out, "tool %s\n", line)
}

// Result records the result of a tool call, given whether it is a tool
// error and its metadata, printing the call again, marked, the first time
// a result says the call went wrong. A command's exit status comes from
// the exit_code in its metadata, which the bash tool records.
func (t *ToolTrace) Result(id string, isError bool, metadata string) {
	if t == nil || t.marked[id] {
		return
	}
	line, traced := t.lines[id]
	if !traced {
		return
	}
	var mark string
	if isError {
		mark = "(failed)"
	} else {
		var meta struct {
			ExitCode int `json:"exit_code"`
		}
		if json.Unmarshal([]byte(metadata), &meta) == nil && meta.ExitCode != 0 {
			mark = fmt.Sprintf("(exit %d)", meta.ExitCode)
		}
	}
	if mark == "" {
		return
	}
	t.marked[id] = true
	_, _ = fmt.Fprintf(t.out, "tool %s %s\n", line, mark)
}
