package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gnosh-ai/gnosh/internal/agent"
	"github.com/gnosh-ai/gnosh/internal/cli"
)

// A retry of a sub-agent's request, whose reply had begun to arrive, does
// not throw away what had arrived of the session's own reply: it did, and
// the run printed the rest of its answer alone (the print mode review of
// 2026-11-19). The session's own retry still throws its false start away.
func TestSubAgentRetryKeepsTheMainReply(t *testing.T) {
	var out bytes.Buffer
	f := newPrinter(newProgram(Options{Stdout: &out}), cli.Invocation{OutputFormat: cli.FormatText})
	f.Event(&agent.TextEvent{Text: "The main answer, "})
	f.Event(&agent.RetryEvent{Meta: agent.Meta{Agent: "sub-1"}, Purpose: "main", Discarded: true, Message: "a sub-agent's request failed and is tried again"})
	f.Event(&agent.TextEvent{Text: "whole."})
	f.Event(&agent.ReplyEvent{Purpose: "main"})
	if got := out.String(); got != "The main answer, whole.\n" {
		t.Errorf("printed %q", got)
	}
	out.Reset()
	f.Event(&agent.TextEvent{Text: "A false start"})
	f.Event(&agent.RetryEvent{Purpose: "main", Discarded: true, Message: "the request failed and is tried again"})
	f.Event(&agent.TextEvent{Text: "The answer."})
	f.Event(&agent.ReplyEvent{Purpose: "main"})
	if got := out.String(); !strings.HasSuffix(got, "The answer.\n") || strings.Contains(got, "false start") {
		t.Errorf("printed %q", got)
	}
}
