package hooks

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/tooldefs"
)

// abandonGrace is how long runOne waits after ctx cancellation for the
// shell goroutine to yield before returning control to the caller and
// letting the goroutine finish on its own. Mirrors the historical
// cmd.WaitDelay = time.Second behavior of the previous os/exec path.
const abandonGrace = time.Second

// runShell is the shell executor used by runOne. It is a package-level
// variable so tests can substitute a blocking or non-yielding
// implementation to exercise the abandon-on-timeout path without
// depending on the scheduling behavior of the real interpreter.
var runShell = shell.Run

// compiledHook pairs a HookConfig with its compiled matcher. A nil
// matcher means "match every tool".
type compiledHook struct {
	cfg     config.HookConfig
	matcher *config.HookMatcher
}

// Runner executes hook commands and aggregates their results.
type Runner struct {
	hooks      []compiledHook
	cwd        string
	projectDir string
}

// NewRunner creates a Runner from the given hook configs. Each hook's
// Matcher is compiled here so the Runner is self-sufficient; callers do
// not have to pre-compile matchers on the config, and reloads or merges
// that rebuild HookConfig values can't silently strip compiled state.
//
// Hooks whose matcher fails to compile are skipped with a warning rather
// than treated as match-everything. ValidateHooks is expected to have
// caught syntax errors earlier, so this is defense in depth.
func NewRunner(hooks []config.HookConfig, cwd, projectDir string) *Runner {
	compiled := make([]compiledHook, 0, len(hooks))
	for _, h := range hooks {
		matcher, err := config.CompileHookMatcher(h.Matcher)
		if err != nil {
			slog.Warn(
				"Hook matcher failed to compile; skipping hook",
				"matcher", h.Matcher,
				"command", h.Command,
				"error", err,
			)
			continue
		}
		compiled = append(compiled, compiledHook{cfg: h, matcher: matcher})
	}
	return &Runner{
		hooks:      compiled,
		cwd:        cwd,
		projectDir: projectDir,
	}
}

// Hooks returns the hook configs the runner was created with, in config
// order. Hooks whose matcher failed to compile at construction are
// omitted. Intended for diagnostics; callers should not rely on ordering
// or identity beyond that.
func (r *Runner) Hooks() []config.HookConfig {
	out := make([]config.HookConfig, len(r.hooks))
	for i, h := range r.hooks {
		out[i] = h.cfg
	}
	return out
}

// Run executes all matching hooks for the given event and tool, returning
// an aggregated result.
func (r *Runner) Run(ctx context.Context, eventName, sessionID, toolName, toolInputJSON string) (AggregateResult, error) {
	id := toolUseID(ctx)
	session, agentID, agentType := toolCallSession(ctx, sessionID)
	envVars := append(BuildEnv(eventName, toolName, session, r.cwd, r.projectDir, toolInputJSON), hookVar("TOOL_USE_ID", id)...)
	envVars = append(envVars, agentVars(agentID, agentType)...)
	p := toolPayload(eventName, session, r.cwd, toolName, toolInputJSON)
	p.ToolUseID = id
	p.AgentID, p.AgentType = agentID, agentType
	p.PermissionMode = permissionMode(ctx)
	return r.run(ctx, r.toolHooks(ctx, toolName), eventName, toolName, toolInputJSON, envVars, marshalPayload(p))
}

// RunPostToolUse executes the PostToolUse hooks whose matcher matches
// toolName, once that tool call has succeeded. output and metadata are
// what the tool returned; the hooks see them as tool_response.
//
// A DecisionDeny result is feedback, not a veto: the call has already
// run, so the caller hands the aggregated Reason to the agent alongside
// the result. Halt still ends the turn. There is nothing left to
// approve or rewrite, so an allow counts as no opinion and UpdatedInput
// is always cleared, on the aggregate and on each hook.
func (r *Runner) RunPostToolUse(ctx context.Context, sessionID, toolName, toolInputJSON, output, metadata string, took time.Duration) (AggregateResult, error) {
	id := toolUseID(ctx)
	session, agentID, agentType := toolCallSession(ctx, sessionID)
	envVars := append(BuildEnv(EventPostToolUse, toolName, session, r.cwd, r.projectDir, toolInputJSON), hookVar("TOOL_USE_ID", id)...)
	envVars = append(envVars, agentVars(agentID, agentType)...)
	p := postPayload(session, r.cwd, toolName, toolInputJSON, output, metadata)
	p.ToolUseID = id
	p.AgentID, p.AgentType = agentID, agentType
	p.PermissionMode = permissionMode(ctx)
	p.DurationMS = took.Milliseconds()
	agg, err := r.run(ctx, r.toolHooks(ctx, toolName), EventPostToolUse, toolName, toolInputJSON, envVars, marshalPayload(p))
	return afterTheCall(agg), err
}

// RunPostToolUseFailure executes the PostToolUseFailure hooks whose
// matcher matches toolName, once that tool call has failed with errText,
// the error the agent reads. As with RunPostToolUse, a block is feedback
// on a call that already ran, Halt ends the turn, and there is nothing
// left to approve or rewrite.
func (r *Runner) RunPostToolUseFailure(ctx context.Context, sessionID, toolName, toolInputJSON, errText string, interrupted bool, took time.Duration) (AggregateResult, error) {
	id := toolUseID(ctx)
	session, agentID, agentType := toolCallSession(ctx, sessionID)
	envVars := append(BuildEnv(EventPostToolUseFailure, toolName, session, r.cwd, r.projectDir, toolInputJSON), hookVar("TOOL_USE_ID", id)...)
	envVars = append(envVars, agentVars(agentID, agentType)...)
	p := toolPayload(EventPostToolUseFailure, session, r.cwd, toolName, toolInputJSON)
	p.ToolUseID = id
	p.AgentID, p.AgentType = agentID, agentType
	p.PermissionMode = permissionMode(ctx)
	p.Error = errText
	p.IsInterrupt = interrupted
	p.DurationMS = took.Milliseconds()
	agg, err := r.run(ctx, r.toolHooks(ctx, toolName), EventPostToolUseFailure, toolName, toolInputJSON, envVars, marshalPayload(p))
	return afterTheCall(agg), err
}

// afterTheCall clears from a hook result what means nothing once a tool
// call has run: an allow or an ask counts as no opinion, and an input
// rewrite is dropped, on the aggregate and on each hook.
func afterTheCall(agg AggregateResult) AggregateResult {
	if agg.Decision == DecisionAllow || agg.Decision == DecisionAsk {
		agg.Decision = DecisionNone
	}
	agg.AskReason = ""
	agg.UpdatedInput = ""
	for i := range agg.Hooks {
		agg.Hooks[i].InputRewrite = false
		if agg.Hooks[i].Decision == DecisionAsk.String() {
			agg.Hooks[i].Decision = DecisionNone.String()
		}
	}
	return agg
}

// RunStop executes every configured hook for the Stop event, which fires
// when the agent is about to finish its turn. Matchers do not apply —
// there is no tool to match, and ValidateHooks rejects a matcher on a
// Stop hook — so every hook the runner holds is executed.
//
// A DecisionDeny result means a hook refused to let the turn end; the
// caller should put the agent back to work with the aggregated Reason.
// Halt means the opposite: end the turn now and do not let any further
// hook continue it. UpdatedInput is meaningless without a tool call and
// is always cleared.
func (r *Runner) RunStop(ctx context.Context, sessionID, lastAssistantMessage string, stopHookActive bool) (AggregateResult, error) {
	envVars := BuildStopEnv(sessionID, r.cwd, r.projectDir, stopHookActive)
	p := stopPayload(sessionID, r.cwd, lastAssistantMessage, stopHookActive)
	p.PermissionMode = permissionMode(ctx)
	agg, err := r.run(ctx, r.everyHook(), EventStop, "", "", envVars, marshalPayload(p))
	agg.UpdatedInput = ""
	return agg, err
}

// RunSubagentStop executes every configured hook for the SubagentStop
// event, which fires when a sub-agent is about to finish, and works
// exactly as RunStop does for the main agent. Matchers do not apply, as
// for Stop, so every hook the runner holds is executed. sessionID is
// the session the sub-agent works for; agentID and agentType name the
// sub-agent (see BuildSubagentStopPayload).
//
// A DecisionDeny result means a hook refused to let the sub-agent
// finish; the caller should put it back to work with the aggregated
// Reason. Halt ends it now, and UpdatedInput is always cleared, as for
// RunStop.
func (r *Runner) RunSubagentStop(ctx context.Context, sessionID, agentID, agentType, lastAssistantMessage string, stopHookActive bool) (AggregateResult, error) {
	envVars := BuildSubagentStopEnv(sessionID, agentID, agentType, r.cwd, r.projectDir, stopHookActive)
	p := subagentStopPayload(sessionID, agentID, agentType, r.cwd, lastAssistantMessage, stopHookActive)
	p.PermissionMode = permissionMode(ctx)
	agg, err := r.run(ctx, r.everyHook(), EventSubagentStop, "", "", envVars, marshalPayload(p))
	agg.UpdatedInput = ""
	return agg, err
}

// RunUserPromptSubmit executes every configured hook for the
// UserPromptSubmit event, which fires when the user sends a prompt and
// before the agent sees it. Matchers do not apply, as for Stop.
//
// A DecisionDeny result or a Halt means a hook blocked the prompt: the
// caller should not send it, and should tell the user the aggregated
// Reason. Otherwise Context is what the hooks want the agent to read
// with the prompt. As in Claude Code, a hook's plain-text stdout counts
// as context for this event; JSON output is read as for any other.
func (r *Runner) RunUserPromptSubmit(ctx context.Context, sessionID, prompt string, attachments []string) (AggregateResult, error) {
	envVars := BuildEnv(EventUserPromptSubmit, "", sessionID, r.cwd, r.projectDir, "")
	p := userPromptSubmitPayload(sessionID, r.cwd, prompt, attachments)
	p.PermissionMode = permissionMode(ctx)
	agg, err := r.run(ctx, r.everyHook(), EventUserPromptSubmit, "", "", envVars, marshalPayload(p))
	agg.UpdatedInput = ""
	return agg, err
}

// RunSessionStart executes the SessionStart hooks whose matcher matches
// source, before the first prompt the process sends to the session.
// Source is the SessionStartSourceClear, SessionStartSourceResume,
// SessionStartSourceStartup or SessionStartSourceCompact name of what
// started the session; hooks with no matcher match every source, as in
// Claude Code.
//
// A SessionStart hook cannot stop the prompt, as in Claude Code: the
// caller sends the prompt whatever the aggregated result says, and
// tells the user about any hook that objected or did not run to a
// verdict. Context is what the hooks want the agent to read with the
// prompt, as for UserPromptSubmit: a hook's plain-text stdout counts as
// context too. UpdatedInput is meaningless without a tool call and is
// always cleared.
func (r *Runner) RunSessionStart(ctx context.Context, sessionID, model, source string) (AggregateResult, error) {
	envVars := append(BuildEnv(EventSessionStart, "", sessionID, r.cwd, r.projectDir, ""), hookVar("SESSION_START_SOURCE", source)...)
	payload := BuildSessionStartPayload(sessionID, r.cwd, model, source)
	agg, err := r.run(ctx, r.matchingHooks(source), EventSessionStart, "", "", envVars, payload)
	agg.UpdatedInput = ""
	return agg, err
}

// RunPreCompact executes the PreCompact hooks whose matcher matches the
// summary's trigger, just before a session's conversation is
// summarized. Trigger is the PreCompactTriggerManual or
// PreCompactTriggerAuto name of what set the summary going; hooks with
// no matcher match both, as in Claude Code.
//
// A PreCompact hook cannot stop the summary, as in Claude Code: the
// caller runs the summary whatever the aggregated result says, and
// tells the user about any hook that did not run to a verdict.
// UpdatedInput is meaningless without a tool call and is always
// cleared.
func (r *Runner) RunPreCompact(ctx context.Context, sessionID, trigger string) (AggregateResult, error) {
	envVars := append(BuildEnv(EventPreCompact, "", sessionID, r.cwd, r.projectDir, ""), hookVar("PRE_COMPACT_TRIGGER", trigger)...)
	payload := BuildPreCompactPayload(sessionID, r.cwd, trigger)
	agg, err := r.run(ctx, r.matchingHooks(trigger), EventPreCompact, "", "", envVars, payload)
	agg.UpdatedInput = ""
	return agg, err
}

// RunPostCompact executes the PostCompact hooks whose matcher matches the
// summary's trigger, once a session's conversation has been summarized
// to summary. As in Claude Code a PostCompact hook has no say: the caller
// tells the user about any hook that did not run to a verdict, and
// UpdatedInput is always cleared.
func (r *Runner) RunPostCompact(ctx context.Context, sessionID, trigger, summary string) (AggregateResult, error) {
	envVars := append(BuildEnv(EventPostCompact, "", sessionID, r.cwd, r.projectDir, ""), hookVar("POST_COMPACT_TRIGGER", trigger)...)
	payload := BuildPostCompactPayload(sessionID, r.cwd, trigger, summary)
	agg, err := r.run(ctx, r.matchingHooks(trigger), EventPostCompact, "", "", envVars, payload)
	agg.UpdatedInput = ""
	return agg, err
}

// RunSessionEnd executes the SessionEnd hooks whose matcher matches the
// reason, when a session ends. Reason is the
// SessionEndReasonClear, SessionEndReasonOther or
// SessionEndReasonPromptInputExit name of why the session ended; hooks
// with no matcher match every reason, as in Claude Code.
//
// As in Claude Code a SessionEnd hook cannot stop anything and what it
// prints is ignored: the caller exits whatever the aggregated result
// says. A hook that fails is logged by runOne. UpdatedInput is
// meaningless without a tool call and is always cleared.
func (r *Runner) RunSessionEnd(ctx context.Context, sessionID, reason string) (AggregateResult, error) {
	envVars := BuildSessionEndEnv(sessionID, r.cwd, r.projectDir, reason)
	payload := BuildSessionEndPayload(sessionID, r.cwd, reason)
	agg, err := r.run(ctx, r.matchingHooks(reason), EventSessionEnd, "", "", envVars, payload)
	agg.UpdatedInput = ""
	return agg, err
}

// run executes the given hooks concurrently and aggregates their
// results. Callers choose which hooks apply and build the stdin payload
// and environment for the event.
func (r *Runner) run(
	ctx context.Context,
	matching []matchedHook,
	eventName, toolName, toolInputJSON string,
	envVars []string,
	payload []byte,
) (AggregateResult, error) {
	if len(matching) == 0 {
		return AggregateResult{Decision: DecisionNone}, nil
	}

	// Deduplicate by command string. A command is a guard only if it
	// matched as nothing more anywhere.
	index := make(map[string]int, len(matching))
	var deduped []matchedHook
	for _, h := range matching {
		if i, ok := index[h.Command]; ok {
			deduped[i].guardOnly = deduped[i].guardOnly && h.guardOnly
			continue
		}
		index[h.Command] = len(deduped)
		deduped = append(deduped, h)
	}

	results := make([]HookResult, len(deduped))
	var wg sync.WaitGroup
	wg.Add(len(deduped))

	for i, h := range deduped {
		go func(idx int, hook matchedHook) {
			defer wg.Done()
			results[idx] = r.runOne(ctx, hook.HookConfig, eventName, envVars, payload)
			if hook.guardOnly && results[idx].Decision == DecisionAllow {
				results[idx].Decision = DecisionNone
				slog.Info("A hook matched this tool only as a guard, by the name of a Claude Code tool that takes other input, so its allow does not approve the call",
					"hook", hook.DisplayName(), "matcher", hook.Matcher, "tool", toolName)
			}
		}(i, h)
	}
	wg.Wait()

	agg := aggregate(results, toolInputJSON)
	agg.Hooks = make([]HookInfo, len(deduped))
	for i, h := range deduped {
		agg.Hooks[i] = HookInfo{
			Name:         h.DisplayName(),
			Event:        eventName,
			Matcher:      h.Matcher,
			Decision:     results[i].Decision.String(),
			Halt:         results[i].Halt,
			Reason:       results[i].Reason,
			InputRewrite: results[i].UpdatedInput != "",
			Error:        results[i].Error,
			Message:      results[i].SystemMessage,

			CouldNotEvaluate: results[i].CouldNotEvaluate,
		}
	}
	slog.Info(
		"Hook completed",
		"event", eventName,
		"tool", toolName,
		"hooks", len(deduped),
		"decision", agg.Decision.String(),
	)
	return agg, nil
}

// matchedHook is a hook that matched an event.
type matchedHook struct {
	config.HookConfig

	// guardOnly is set for a hook that matched a tool call only by the
	// name of a Claude Code tool whose job the tool does with input of
	// another shape (see toolHooks): it can block the call, but its allow
	// does not approve it.
	guardOnly bool
}

// everyHook returns every hook, for an event matchers do not apply to.
func (r *Runner) everyHook() []matchedHook {
	matched := make([]matchedHook, len(r.hooks))
	for i, h := range r.hooks {
		matched[i] = matchedHook{HookConfig: h.cfg}
	}
	return matched
}

// matchingHooks returns the hooks whose matcher matches any of names (a
// SessionStart source, a PreCompact trigger or a SessionEnd reason), or
// that have no matcher, which matches everything.
func (r *Runner) matchingHooks(names ...string) []matchedHook {
	var matched []matchedHook
	for _, h := range r.hooks {
		if h.matcher.Matches(names...) {
			matched = append(matched, matchedHook{HookConfig: h.cfg})
		}
	}
	return matched
}

// toolHooks returns the hooks a call of toolName runs, in config order:
// those whose matcher matches one of its names (see toolNames), and, as
// guards only, those that match the name of a Claude Code tool whose job
// it does with input of another shape (tooldefs.ClaudeCodeGuardNamesOf).
// So a hook guarding Claude Code's Edit|Write also guards lsp_rename,
// which Claude Code has no tool like, while one approving Write calls,
// having checked Write's input, does not approve a download.
func (r *Runner) toolHooks(ctx context.Context, toolName string) []matchedHook {
	names := toolNames(ctx, toolName)
	guardNames := tooldefs.ClaudeCodeGuardNamesOf(toolName)
	var matched []matchedHook
	for _, h := range r.hooks {
		switch {
		case h.matcher.Matches(names...):
			matched = append(matched, matchedHook{HookConfig: h.cfg})
		case len(guardNames) > 0 && h.matcher.Matches(guardNames...):
			matched = append(matched, matchedHook{HookConfig: h.cfg, guardOnly: true})
		}
	}
	return matched
}

// toolNames are the names a tool call's matchers are tested against: the
// tool's, the name it had before the rename, and Claude Code's names for
// it, so that a hook written for Claude Code ("Bash", "Edit|Write",
// "Read", "mcp__github__.*") matches what it matches there.
func toolNames(ctx context.Context, toolName string) []string {
	names := []string{toolName, tooldefs.LegacyName(toolName)}
	names = append(names, tooldefs.ClaudeCodeNamesOf(toolName)...)
	return append(names, toolAliases(ctx)...)
}

// runOne executes a single hook command and returns its result.
//
// Execution goes through Crush's embedded POSIX shell (shell.Run) so the
// same interpreter, builtins, and coreutils are visible to hooks as to
// the bash tool. BlockFuncs are intentionally omitted: hooks are
// user-authored config that carry the same trust as a shell alias.
//
// A hook that fails to yield after its deadline has passed is abandoned
// after abandonGrace so the caller never blocks longer than
// timeout + abandonGrace. Ownership of the stdout and stderr buffers is
// strictly single-goroutine:
//   - before receiving from `done`, only the goroutine writes to them;
//   - after `done` delivers a value, the goroutine is finished and the
//     outer frame reads them;
//   - on the abandon path, the goroutine may still be writing and the
//     outer frame must not touch them again.
//
// On UserPromptSubmit and SessionStart, stdout that is not a JSON object
// is the hook's context, as Claude Code reads those hooks' output.
func (r *Runner) runOne(parentCtx context.Context, hook config.HookConfig, eventName string, envVars []string, payload []byte) HookResult {
	plainStdoutIsContext := eventName == EventUserPromptSubmit || eventName == EventSessionStart
	timeout := hook.TimeoutDuration(eventName)
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runShell(ctx, shell.RunOptions{
			Command: hook.Command,
			Cwd:     r.cwd,
			Env:     envVars,
			Stdin:   bytes.NewReader(payload),
			Stdout:  &stdout,
			Stderr:  &stderr,
		})
	}()

	var err error
	select {
	case err = <-done:
		// Normal path: goroutine has finished, buffers are safe to read.
	case <-ctx.Done():
		select {
		case err = <-done:
			// Interpreter yielded within the grace period; safe to read.
		case <-time.After(abandonGrace):
			slog.Warn(
				"Hook did not yield after cancel; abandoning goroutine",
				"command", hook.Command,
				"timeout", timeout,
			)
			// The goroutine may still be writing to stdout/stderr; do
			// not read either buffer below this point.
			return couldNotRun(hook, fmt.Sprintf("timed out after %s and did not stop", timeout))
		}
	}

	if shell.IsInterrupt(err) {
		// Distinguish timeout from parent cancellation. A cancelled turn
		// is the user's doing, not a hook failure, so it carries no Error.
		if parentCtx.Err() != nil {
			slog.Debug("Hook cancelled by parent context", "command", hook.Command)
			return HookResult{Decision: DecisionNone}
		}
		slog.Warn("Hook timed out", "command", hook.Command, "timeout", timeout)
		return couldNotRun(hook, fmt.Sprintf("timed out after %s", timeout))
	}

	if err != nil {
		exitCode := shell.ExitCode(err)
		switch exitCode {
		case 2:
			// Exit code 2 = block this tool call. Stderr is the reason.
			reason := strings.TrimSpace(stderr.String())
			if reason == "" {
				reason = "blocked by hook"
			}
			return HookResult{
				Decision: DecisionDeny,
				Reason:   reason,
			}
		case CouldNotEvaluateExitCode:
			// Exit code 3 = the hook could not check. Stderr says why.
			why := strings.TrimSpace(stderr.String())
			slog.Warn("Hook could not evaluate", "command", hook.Command, "stderr", why)
			return HookResult{
				Decision:         DecisionDeny,
				CouldNotEvaluate: true,
				Reason:           fmt.Sprintf("hook %q could not check this: %s. %s", hook.DisplayName(), cmp.Or(strings.TrimSuffix(why, "."), "it gave no reason"), checkDidNotPass),
				Error:            cmp.Or(firstLine(why), "it gave no reason"),
			}
		case HaltExitCode:
			// Exit code 49 = halt the whole turn. Stderr is the reason.
			reason := strings.TrimSpace(stderr.String())
			if reason == "" {
				reason = "turn halted by hook"
			}
			return HookResult{
				Decision: DecisionDeny,
				Halt:     true,
				Reason:   reason,
			}
		default:
			stderrText := strings.TrimSpace(stderr.String())
			detail := stderrText
			if detail == "" && !shell.IsExitStatus(err) {
				// The shell never ran the hook — most often the script
				// path is wrong — so it wrote nothing to stderr and the
				// reason is in the error itself.
				detail = err.Error()
			}
			if !shell.IsExitStatus(err) {
				slog.Warn("Hook did not start", "command", hook.Command, "error", err)
				return couldNotRun(hook, "did not start: "+firstLine(detail))
			}
			if exitCode == 126 || exitCode == 127 || exitCode > 128 {
				// The hook, or a command it runs, could not be found or
				// run, or the hook was killed: it checked nothing.
				slog.Warn("Hook could not run", "command", hook.Command, "exit_code", exitCode, "stderr", stderrText)
				return couldNotRun(hook, hookFailureMessage(exitCode, detail))
			}
			if stderrText != "" {
				// An error that says why: the hook ran into trouble, a
				// check that crashed or a tool that failed, and did not
				// check. Claude Code lets it pass as a non-blocking error.
				slog.Warn("Hook failed", "command", hook.Command, "exit_code", exitCode, "stderr", stderrText)
				failure := hookFailureMessage(exitCode, stderrText)
				return HookResult{
					Decision:         DecisionDeny,
					CouldNotEvaluate: true,
					Reason:           fmt.Sprintf("hook %q failed (%s), so it did not check this. %s", hook.DisplayName(), failure, checkDidNotPass),
					Error:            failure,
				}
			}
			// A silent non-zero exit is a test that came out false, as
			// [[ $f == *.go ]] && gofmt -w "$f" exits 1 on every other
			// file: no opinion, and no failure either.
			slog.Debug("Hook exited non-zero without a word; no opinion", "command", hook.Command, "exit_code", exitCode)
			return HookResult{Decision: DecisionNone}
		}
	}

	// Exit code 0 — parse stdout JSON.
	if out := strings.TrimSpace(stdout.String()); plainStdoutIsContext && out != "" && !isJSONObject(out) {
		return HookResult{Decision: DecisionNone, Context: out}
	}
	result := parseStdout(stdout.String())
	if result.Error != "" {
		// An answer that cannot be read is no answer: a hook that wrote
		// "decision": "Deny" meant to deny.
		reason := fmt.Sprintf("hook %q gave an answer Klash could not read (%s)", hook.DisplayName(), result.Error)
		if result.Reason != "" {
			reason += "; its reason: " + result.Reason
		}
		result.Decision = DecisionDeny
		result.CouldNotEvaluate = true
		result.Reason = reason + ". An answer that cannot be read does not pass."
	}
	slog.Debug(
		"Hook executed",
		"command", hook.Command,
		"decision", result.Decision.String(),
	)
	return result
}

// maxHookErrorLen bounds the stderr carried into a hook's Error, which is
// shown on a single line in the TUI. The full stderr is in the log.
const maxHookErrorLen = 200

// checkDidNotPass ends the reason given for a hook that could not check.
const checkDidNotPass = "A check that could not run does not pass."

// couldNotRun is the result of a hook that could not run to a verdict:
// it never started, it or a command it runs was not found or not
// executable, or it timed out. It checked nothing, so it blocks: Klash
// fails closed, where Claude Code lets such a hook pass.
func couldNotRun(hook config.HookConfig, failure string) HookResult {
	return HookResult{
		Decision:         DecisionDeny,
		CouldNotEvaluate: true,
		Reason:           fmt.Sprintf("hook %q could not run (%s). %s", hook.DisplayName(), failure, checkDidNotPass),
		Error:            failure,
	}
}

// firstLine is the first line of s, cut to fit a status line.
func firstLine(s string) string {
	first, _, _ := strings.Cut(s, "\n")
	// Truncate by rune, not byte, so a cut never splits a character.
	if r := []rune(first); len(r) > maxHookErrorLen {
		first = string(r[:maxHookErrorLen]) + "…"
	}
	return first
}

// hookFailureMessage summarises how a hook failed, for display:
// the exit code, what it usually means, and the first line of stderr.
func hookFailureMessage(exitCode int, stderrText string) string {
	msg := fmt.Sprintf("exit %d", exitCode)
	switch {
	case exitCode == 126:
		msg += " (not executable)"
	case exitCode == 127:
		msg += " (command not found)"
	case exitCode > 128:
		msg += fmt.Sprintf(" (killed by signal %d)", exitCode-128)
	}
	if first := firstLine(stderrText); first != "" {
		msg += ": " + first
	}
	return msg
}

// isJSONObject reports whether s is a JSON object, the only output a hook
// gives as an envelope.
func isJSONObject(s string) bool {
	return strings.HasPrefix(s, "{") && json.Valid([]byte(s))
}
