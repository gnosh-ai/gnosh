package agent

import (
	"context"
	"slices"

	"charm.land/fantasy"
	gnovmtools "github.com/charmbracelet/crush/internal/gnovm/tools"
)

// memLine goes last on every prompt the user sends a session whose model
// has mem and Gno: when to use them, where a model that answers without
// thinking first still reads it, right before its reply. In the system
// prompt, twenty thousand tokens before the question, a Qwen with thinking
// off skipped it after small talk and gave a sum it added in its head. It
// is kept with the prompt as a hidden part, as hook output is, so a rebuilt
// conversation reads as it went out and a local server's cache of it
// holds; the system prompt says how mem and the machines work.
const memLine = "<system_reminder>Use mem and Gno whenever reasonable: work things out in Gno rather than in your head, and answer from what it returns. Keep goals, steps, facts and reminders in mem, and look there before answering from memory. Call nothing done until you've checked it and mem shows it done.</system_reminder>"

// memLineBlock is memLine for a prompt that takes it, "" for one that
// does not.
func memLineBlock(call SessionAgentCall) string {
	if !call.memLine {
		return ""
	}
	return memLine
}

// offersMem reports whether a turn on channel offers the model mem and
// Gno: gnovm_run is among its tools and the agent is not a sub-agent,
// which mem does not remind either.
func (a *sessionAgent) offersMem(ctx context.Context, channel string) bool {
	return !a.isSubAgent && slices.ContainsFunc(a.requestTools(ctx, channel), func(tool fantasy.AgentTool) bool {
		return tool.Info().Name == gnovmtools.RunToolName
	})
}
