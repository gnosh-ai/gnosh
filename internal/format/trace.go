package format

import (
	"fmt"
	"io"
)

// ToolTrace prints the --verbose trace of a non-interactive run's tool
// calls: a line for each call when it is made, and a second line, marked
// "(failed)", when its result comes back as an error. A call is printed
// once however often the message carrying it is republished. A nil
// *ToolTrace traces nothing.
type ToolTrace struct {
	out    io.Writer
	lines  map[string]string // call ID -> "name summary"
	failed map[string]bool
}

// NewToolTrace returns a trace that writes to out.
func NewToolTrace(out io.Writer) *ToolTrace {
	return &ToolTrace{out: out, lines: map[string]string{}, failed: map[string]bool{}}
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

// Result records the result of a tool call, printing the call again,
// marked "(failed)", the first time its result is an error.
func (t *ToolTrace) Result(id string, isError bool) {
	if t == nil || !isError || t.failed[id] {
		return
	}
	line, traced := t.lines[id]
	if !traced {
		return
	}
	t.failed[id] = true
	_, _ = fmt.Fprintf(t.out, "tool %s (failed)\n", line)
}
