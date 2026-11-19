package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/cli"
)

// resultOnly is a session that can say what the result line needs, and no
// more.
type resultOnly struct{ Controls }

func (resultOnly) Info() SessionInfo   { return SessionInfo{ID: "s-1"} }
func (resultOnly) Usage() agent.Totals { return agent.Totals{} }

// The result is the last line a run writes: what the session says as it
// closes (a notice of a hook's, of the memory's) goes to standard error
// alone. In stream-json it went out as one more line after the result,
// which a reader takes for the start of another (the print mode review of
// 2026-11-19).
func TestNothingAfterTheResult(t *testing.T) {
	var out, errs bytes.Buffer
	f := newPrinter(newProgram(Options{Stdout: &out, Stderr: &errs}), cli.Invocation{OutputFormat: cli.FormatStreamJSON})
	f.c = resultOnly{}
	f.Event(&agent.TextEvent{Text: "Done."})
	f.report(resultOnly{}, agent.TurnResult{Reason: agent.EndCompleted, Text: "Done."})
	f.Event(&agent.NoticeEvent{Level: agent.LevelWarning, Text: "a SessionEnd hook failed"})
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, `"type":"result"`) {
		t.Errorf("the last line is %s", last)
	}
	if !strings.Contains(errs.String(), "a SessionEnd hook failed") {
		t.Errorf("standard error: %q", errs.String())
	}
}
