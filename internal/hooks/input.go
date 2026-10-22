package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/charmbracelet/crush/internal/shell"
	"github.com/tidwall/gjson"
)

// SupportedOutputVersion is the highest envelope version this build
// understands. Hooks may omit `version` entirely (treated as 1) or pin
// an older version. Unknown higher versions are still parsed but logged.
const SupportedOutputVersion = 1

// Payload is the JSON structure piped to hook commands via stdin.
// ToolInput is emitted as a parsed JSON object for compatibility with
// Claude Code hooks (which expect tool_input to be an object, not a
// string).
type Payload struct {
	Event string `json:"event"`
	// HookEventName repeats Event under Claude Code's name for it, which
	// Claude Code hooks read to tell events apart.
	HookEventName string          `json:"hook_event_name"`
	SessionID     string          `json:"session_id"`
	CWD           string          `json:"cwd"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	// ToolUseID is the ID of the tool call a PreToolUse or PostToolUse
	// hook runs for, the same in both, as in Claude Code.
	ToolUseID string `json:"tool_use_id,omitempty"`

	// StopHookActive is set on an EventStop payload when the turn is
	// only still running because an earlier Stop hook blocked it. A
	// hook that wants to fire once and then let the turn end should
	// return success while this is true. Named for parity with Claude
	// Code's Stop hook input.
	StopHookActive bool `json:"stop_hook_active,omitempty"`
	// LastAssistantMessage carries the text of the assistant message
	// the turn is about to end on, so a Stop hook can judge the answer
	// without reading the session back out of the database.
	LastAssistantMessage string `json:"last_assistant_message,omitempty"`
	// ToolResponse is set on an EventPostToolUse payload: what the tool
	// returned, as a ToolResponse object. Named for parity with Claude
	// Code's PostToolUse hook input.
	ToolResponse *ToolResponse `json:"tool_response,omitempty"`
	// Error is set on an EventPostToolUseFailure payload: the error the
	// tool call returned, as the agent reads it. IsInterrupt says the call
	// failed because the turn was cancelled.
	Error       string `json:"error,omitempty"`
	IsInterrupt bool   `json:"is_interrupt,omitempty"`
	// DurationMS is how long a tool call that has run took, on
	// PostToolUse and PostToolUseFailure payloads.
	DurationMS int64 `json:"duration_ms,omitempty"`
	// Prompt is set on an EventUserPromptSubmit payload: the prompt the
	// user sent, as Claude Code's UserPromptSubmit input carries it.
	// Attachments names the files sent with it, if any.
	Prompt      string   `json:"prompt,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
}

// ToolResponse is what a tool call returned, as a PostToolUse hook sees
// it: the text the agent reads, and the tool's metadata (for bash, its
// exit code among other things) when it has any.
type ToolResponse struct {
	Output   string          `json:"output"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// BuildPayload constructs the JSON stdin payload for a hook command.
func BuildPayload(eventName, sessionID, cwd, toolName, toolInputJSON string) []byte {
	return marshalPayload(toolPayload(eventName, sessionID, cwd, toolName, toolInputJSON))
}

// BuildPostPayload constructs the JSON stdin payload for an
// EventPostToolUse hook: the tool call, plus what it returned. Metadata
// that is not a JSON object is left out rather than passed on as
// something a hook would have to guess at.
func BuildPostPayload(sessionID, cwd, toolName, toolInputJSON, output, metadata string) []byte {
	return marshalPayload(postPayload(sessionID, cwd, toolName, toolInputJSON, output, metadata))
}

func postPayload(sessionID, cwd, toolName, toolInputJSON, output, metadata string) Payload {
	p := toolPayload(EventPostToolUse, sessionID, cwd, toolName, toolInputJSON)
	p.ToolResponse = &ToolResponse{Output: output}
	if meta := json.RawMessage(metadata); json.Valid(meta) && strings.HasPrefix(strings.TrimSpace(metadata), "{") {
		p.ToolResponse.Metadata = meta
	}
	return p
}

// toolUseIDKey is the context key WithToolUseID stores a call's ID under.
type toolUseIDKey struct{}

// WithToolUseID returns a context naming the tool call that hooks run
// for. They get it as tool_use_id, and in CRASH_TOOL_USE_ID, so that a
// PreToolUse and a PostToolUse hook can tell they saw the same call.
func WithToolUseID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolUseIDKey{}, id)
}

func toolUseID(ctx context.Context) string {
	id, _ := ctx.Value(toolUseIDKey{}).(string)
	return id
}

// toolPayload is the payload common to the events that fire around a
// tool call.
func toolPayload(eventName, sessionID, cwd, toolName, toolInputJSON string) Payload {
	toolInput := json.RawMessage(toolInputJSON)
	if !json.Valid(toolInput) {
		toolInput = json.RawMessage("{}")
	}
	return Payload{
		Event:         eventName,
		HookEventName: eventName,
		SessionID:     sessionID,
		CWD:           cwd,
		ToolName:      toolName,
		ToolInput:     toolInput,
	}
}

func marshalPayload(p Payload) []byte {
	data, err := json.Marshal(p)
	if err != nil {
		return []byte("{}")
	}
	return data
}

// BuildStopPayload constructs the JSON stdin payload for an EventStop
// hook. There is no tool in play, so tool_name is empty and tool_input
// is an empty object.
func BuildStopPayload(sessionID, cwd, lastAssistantMessage string, stopHookActive bool) []byte {
	p := Payload{
		Event:                EventStop,
		HookEventName:        EventStop,
		SessionID:            sessionID,
		CWD:                  cwd,
		ToolInput:            json.RawMessage("{}"),
		StopHookActive:       stopHookActive,
		LastAssistantMessage: lastAssistantMessage,
	}
	return marshalPayload(p)
}

// BuildUserPromptSubmitPayload constructs the JSON stdin payload for an
// EventUserPromptSubmit hook. There is no tool in play, so tool_name is
// empty and tool_input is an empty object.
func BuildUserPromptSubmitPayload(sessionID, cwd, prompt string, attachments []string) []byte {
	return marshalPayload(Payload{
		Event:         EventUserPromptSubmit,
		HookEventName: EventUserPromptSubmit,
		SessionID:     sessionID,
		CWD:           cwd,
		ToolInput:     json.RawMessage("{}"),
		Prompt:        prompt,
		Attachments:   attachments,
	})
}

// BuildStopEnv constructs the environment variable slice for an
// EventStop hook command.
func BuildStopEnv(sessionID, cwd, projectDir string, stopHookActive bool) []string {
	env := BuildEnv(EventStop, "", sessionID, cwd, projectDir, "")
	active := "0"
	if stopHookActive {
		active = "1"
	}
	return append(env, hookVar("STOP_HOOK_ACTIVE", active)...)
}

// BuildEnv constructs the environment variable slice for a hook command.
// It includes all current process env vars plus hook-specific ones.
func BuildEnv(eventName, toolName, sessionID, cwd, projectDir, toolInputJSON string) []string {
	env := os.Environ()
	env = append(env, shell.CrushEnvMarkers()...)
	env = append(env, hookVar("EVENT", eventName)...)
	env = append(env, hookVar("TOOL_NAME", toolName)...)
	env = append(env, hookVar("SESSION_ID", sessionID)...)
	env = append(env, hookVar("CWD", cwd)...)
	env = append(env, hookVar("PROJECT_DIR", projectDir)...)

	// Extract tool-specific env vars from the JSON input.
	if toolInputJSON != "" {
		if cmd := gjson.Get(toolInputJSON, "command"); cmd.Exists() {
			env = append(env, hookVar("TOOL_INPUT_COMMAND", cmd.String())...)
		}
		if fp := gjson.Get(toolInputJSON, "file_path"); fp.Exists() {
			env = append(env, hookVar("TOOL_INPUT_FILE_PATH", fp.String())...)
		}
	}

	return env
}

// hookVar sets a hook's environment variable under its CRASH_ name and
// under the CRUSH_ name that hooks written before the rename read.
func hookVar(name, value string) []string {
	return []string{"CRASH_" + name + "=" + value, "CRUSH_" + name + "=" + value}
}

// parseStdout parses the JSON output from a hook command's stdout.
// Supports both Crush format and Claude Code format (hookSpecificOutput).
//
// The top-level fields are read first, then hookSpecificOutput on top of
// them, because Claude Code hooks use both at once: a PostToolUse or Stop
// hook blocks with a top-level "decision": "block" and sends its
// additionalContext in hookSpecificOutput. Reading only hookSpecificOutput
// when it was present dropped that block. Claude Code's "continue": false
// stops everything, which is a halt here.
func parseStdout(stdout string) HookResult {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return HookResult{Decision: DecisionNone}
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return HookResult{Decision: DecisionNone}
	}

	var parsed struct {
		Version      int             `json:"version"`
		Decision     string          `json:"decision"`
		Halt         bool            `json:"halt"`
		Reason       string          `json:"reason"`
		Context      json.RawMessage `json:"context"`
		UpdatedInput json.RawMessage `json:"updated_input"`
		// Claude Code stops with "continue": false, giving the reason
		// in stopReason.
		Continue   *bool  `json:"continue"`
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		return HookResult{Decision: DecisionNone}
	}

	if parsed.Version > SupportedOutputVersion {
		slog.Debug(
			"Hook output declared a newer envelope version than this build supports",
			"version", parsed.Version,
			"supported", SupportedOutputVersion,
		)
	}

	result := HookResult{
		Halt:    parsed.Halt || (parsed.Continue != nil && !*parsed.Continue),
		Reason:  parsed.Reason,
		Context: parseContext(parsed.Context),
	}
	if result.Halt && result.Reason == "" {
		result.Reason = parsed.StopReason
	}
	decision, err := parseDecision(parsed.Decision)
	result.Decision = decision
	if err != nil {
		result.Error = err.Error()
	}
	if updated, patchErr := parseUpdatedInput(parsed.UpdatedInput); patchErr != nil {
		rejectRewrite(&result, patchErr)
	} else {
		result.UpdatedInput = updated
	}

	if hso, ok := raw["hookSpecificOutput"]; ok {
		applyClaudeCodeOutput(&result, hso)
	}
	return result
}

// parseUpdatedInput accepts a native updated_input that is a JSON object
// (nested, or stringified as a backward-compat fallback) and returns it
// as the opaque patch string. Anything else that is actually present is
// an error: it would be silently dropped at aggregation time, so a hook
// that set out to rewrite the tool input would look like one that did
// not.
func parseUpdatedInput(raw json.RawMessage) (string, error) {
	s := rawToString(raw)
	if s == "" {
		return "", nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return "", fmt.Errorf(
			"updated_input must be a JSON object, got %s", jsonTypeName(raw),
		)
	}
	return s, nil
}

// jsonTypeName reports which kind of JSON value a raw message holds, for
// error messages.
func jsonTypeName(raw json.RawMessage) string {
	switch {
	case len(raw) == 0 || string(raw) == "null":
		return "null"
	case raw[0] == '"':
		return "a string"
	case raw[0] == '[':
		return "an array"
	case raw[0] == '{':
		return "an object"
	default:
		return "a number or boolean"
	}
}

// rejectRewrite records that a hook's tool-input rewrite is malformed. The
// hook's "allow" goes with it: a hook that approves a call as it rewrote it
// must not approve the call as it stands, so the normal permission check
// runs instead. A deny or a halt still holds.
func rejectRewrite(result *HookResult, err error) {
	setParseError(result, err)
	result.rewriteRejected = true
	if result.Decision == DecisionAllow {
		result.Decision = DecisionNone
	}
}

// setParseError records a parse error on the result without clobbering
// one already set (an unknown decision and a bad updated_input should
// both surface).
func setParseError(result *HookResult, err error) {
	if result.Error == "" {
		result.Error = err.Error()
	} else {
		result.Error += "; " + err.Error()
	}
}

// parseContext accepts either a single string or an array of strings and
// returns a newline-joined value with empty entries dropped.
func parseContext(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// String form.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		return ""
	}
	// Array form.
	if raw[0] == '[' {
		var items []string
		if err := json.Unmarshal(raw, &items); err != nil {
			return ""
		}
		out := items[:0]
		for _, s := range items {
			if s != "" {
				out = append(out, s)
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// applyClaudeCodeOutput applies Claude Code's hookSpecificOutput,
// {"permissionDecision": "allow", ...}, on top of the top-level fields
// already in result. A permissionDecision overrides a top-level decision,
// as it does in Claude Code, where the top-level one is deprecated for
// PreToolUse; its reason goes with it. additionalContext is added to any
// top-level context, and updatedInput replaces a top-level updated_input.
//
// Each field is read on its own, so that one of the wrong type is
// reported by name without losing the rest: a deny must hold even when
// the context beside it is malformed. Decoding them all at once dropped
// the whole output, deny included, and blamed the object itself
// ("must be a JSON object, got an object").
func applyClaudeCodeOutput(result *HookResult, data json.RawMessage) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		setParseError(result, fmt.Errorf("hookSpecificOutput must be a JSON object, got %s", jsonTypeName(data)))
		return
	}

	if raw, ok := fields["permissionDecision"]; ok && string(raw) != "null" {
		word, err := hookSpecificString(fields, "permissionDecision")
		if err != nil {
			setParseError(result, err)
		}
		decision, err := parseDecision(word)
		if err != nil {
			setParseError(result, err)
		}
		if decision == DecisionAllow && result.rewriteRejected {
			// The top-level rewrite this allow goes with was malformed.
			decision = DecisionNone
		}
		result.Decision = decision
		reason, err := hookSpecificString(fields, "permissionDecisionReason")
		if err != nil {
			setParseError(result, err)
		}
		if reason != "" {
			result.Reason = reason
		}
	} else if _, err := hookSpecificString(fields, "permissionDecisionReason"); err != nil {
		setParseError(result, err)
	}

	// Claude Code's additionalContext is a string; a list of them is taken
	// too, as Crush's own context field takes one.
	if raw, ok := fields["additionalContext"]; ok && string(raw) != "null" {
		if raw[0] != '"' && raw[0] != '[' {
			setParseError(result, fmt.Errorf(
				"hookSpecificOutput.additionalContext must be a string, got %s", jsonTypeName(raw),
			))
		} else if extra := parseContext(raw); extra != "" {
			if result.Context != "" {
				result.Context += "\n"
			}
			result.Context += extra
		}
	}

	// Claude Code's updatedInput is an object and nothing else: unlike
	// Crush's own updated_input, a string holding one is not accepted.
	if raw := fields["updatedInput"]; len(raw) > 0 && string(raw) != "null" {
		if raw[0] == '{' {
			result.UpdatedInput = string(raw)
		} else {
			rejectRewrite(result, fmt.Errorf(
				"hookSpecificOutput.updatedInput must be a JSON object, got %s", jsonTypeName(raw),
			))
		}
	}
}

// hookSpecificString reads a string field of a hookSpecificOutput. A
// missing or null field is empty.
func hookSpecificString(fields map[string]json.RawMessage, name string) (string, error) {
	raw, ok := fields[name]
	if !ok || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("hookSpecificOutput.%s must be a string, got %s", name, jsonTypeName(raw))
	}
	return s, nil
}

// rawToString converts a json.RawMessage to a string suitable for use
// as opaque tool input. It accepts both a JSON object (nested) and a
// JSON string (stringified, for backward compatibility).
func rawToString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// If it's a JSON string, unwrap it.
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	// Otherwise it's an object/array — use as-is.
	return string(raw)
}

// decisionWords is every decision value a hook may return, and what it
// means. Two vocabularies are accepted because the docs promise both
// Crush's own and Claude Code compatibility:
//
//   - Crush's top-level "decision": "allow", "deny".
//   - Claude Code's top-level "decision": "approve", "block". Claude Code
//     Stop hooks keep the agent working with {"decision": "block"}.
//   - Claude Code's hookSpecificOutput.permissionDecision: "allow",
//     "deny", "ask". "ask" means show the normal permission prompt, which
//     is exactly what DecisionNone already does here.
//
// Matching is exact, as it is for event names.
var decisionWords = map[string]Decision{
	"allow":   DecisionAllow,
	"approve": DecisionAllow,
	"deny":    DecisionDeny,
	"block":   DecisionDeny,
	"ask":     DecisionNone,
}

// parseDecision maps a hook's decision value to a Decision. An empty
// value means the hook expressed no opinion.
//
// Anything else is an error, not "no opinion". Reading an unrecognised
// word as no opinion is how Claude Code's own {"decision": "block"} was
// silently ignored — a hook that set out to stop a tool call let it run,
// and a Stop hook that set out to keep the agent working let the turn
// end. The hook stays non-blocking, as every failed hook is, but the
// error surfaces as "Failed" on the tool line, or as a warning for a
// Stop hook.
func parseDecision(s string) (Decision, error) {
	if s == "" {
		return DecisionNone, nil
	}
	if d, ok := decisionWords[s]; ok {
		return d, nil
	}
	return DecisionNone, fmt.Errorf(
		"unknown decision %q; expected allow, deny, approve, block or ask", s,
	)
}
