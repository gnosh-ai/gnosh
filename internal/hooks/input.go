package hooks

import (
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
	Event     string          `json:"event"`
	SessionID string          `json:"session_id"`
	CWD       string          `json:"cwd"`
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`

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
}

// BuildPayload constructs the JSON stdin payload for a hook command.
func BuildPayload(eventName, sessionID, cwd, toolName, toolInputJSON string) []byte {
	toolInput := json.RawMessage(toolInputJSON)
	if !json.Valid(toolInput) {
		toolInput = json.RawMessage("{}")
	}
	p := Payload{
		Event:     eventName,
		SessionID: sessionID,
		CWD:       cwd,
		ToolName:  toolName,
		ToolInput: toolInput,
	}
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
		SessionID:            sessionID,
		CWD:                  cwd,
		ToolInput:            json.RawMessage("{}"),
		StopHookActive:       stopHookActive,
		LastAssistantMessage: lastAssistantMessage,
	}
	data, err := json.Marshal(p)
	if err != nil {
		return []byte("{}")
	}
	return data
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
func parseStdout(stdout string) HookResult {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return HookResult{Decision: DecisionNone}
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return HookResult{Decision: DecisionNone}
	}

	// Claude Code compat: if hookSpecificOutput is present, parse that.
	if hso, ok := raw["hookSpecificOutput"]; ok {
		return parseClaudeCodeOutput(hso)
	}

	var parsed struct {
		Version      int             `json:"version"`
		Decision     string          `json:"decision"`
		Halt         bool            `json:"halt"`
		Reason       string          `json:"reason"`
		Context      json.RawMessage `json:"context"`
		UpdatedInput json.RawMessage `json:"updated_input"`
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
		Halt:    parsed.Halt,
		Reason:  parsed.Reason,
		Context: parseContext(parsed.Context),
	}
	decision, err := parseDecision(parsed.Decision)
	result.Decision = decision
	if err != nil {
		result.Error = err.Error()
	}
	if updated, patchErr := parseUpdatedInput(parsed.UpdatedInput); patchErr != nil {
		setParseError(&result, patchErr)
	} else {
		result.UpdatedInput = updated
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

// parseClaudeCodeOutput handles the Claude Code hook output format:
// {"hookSpecificOutput": {"permissionDecision": "allow", ...}}
func parseClaudeCodeOutput(data json.RawMessage) HookResult {
	var hso struct {
		PermissionDecision       string          `json:"permissionDecision"`
		PermissionDecisionReason string          `json:"permissionDecisionReason"`
		UpdatedInput             json.RawMessage `json:"updatedInput"`
		AdditionalContext        string          `json:"additionalContext"`
	}
	if err := json.Unmarshal(data, &hso); err != nil {
		return HookResult{Decision: DecisionNone}
	}

	decision, err := parseDecision(hso.PermissionDecision)
	result := HookResult{
		Decision: decision,
		Reason:   hso.PermissionDecisionReason,
		Context:  hso.AdditionalContext,
	}
	if err != nil {
		result.Error = err.Error()
	}

	// Marshal updatedInput back to a string for our opaque format.
	if len(hso.UpdatedInput) > 0 && string(hso.UpdatedInput) != "null" {
		updated, patchErr := parseUpdatedInput(hso.UpdatedInput)
		if patchErr != nil {
			setParseError(&result, patchErr)
		} else {
			result.UpdatedInput = updated
		}
	}

	return result
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
