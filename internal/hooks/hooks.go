// Package hooks runs user-defined shell commands that fire on hook events
// (e.g. PreToolUse), returning decisions that control agent behavior.
package hooks

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/tidwall/sjson"
)

// Hook event name constants. The canonical list lives in the config
// package (which owns validation and cannot import this one); these are
// aliases so agent code can keep referring to hooks.EventX.
const (
	// EventPreToolUse fires immediately before a tool call runs.
	EventPreToolUse = config.EventPreToolUse
	// EventPostToolUse fires right after a tool call succeeds.
	EventPostToolUse = config.EventPostToolUse
	// EventPostToolUseFailure fires right after a tool call fails.
	EventPostToolUseFailure = config.EventPostToolUseFailure
	// EventUserPromptSubmit fires when the user sends a prompt.
	EventUserPromptSubmit = config.EventUserPromptSubmit
	// EventSessionStart fires before the first prompt a process sends to a
	// session; its matcher is tested against the source.
	EventSessionStart = config.EventSessionStart
	// EventStop fires when the agent is about to finish its turn.
	EventStop = config.EventStop
	// EventSubagentStop fires when a sub-agent is about to finish, and
	// works as EventStop does for the main agent.
	EventSubagentStop = config.EventSubagentStop
	// EventSubagentStart fires when a sub-agent starts; what its hooks
	// add as context goes to the sub-agent with its first prompt.
	EventSubagentStart = config.EventSubagentStart
	// EventStopFailure fires instead of EventStop when a turn ends on an
	// error from the model's API; its matcher is tested against the
	// error's kind.
	EventStopFailure = config.EventStopFailure
	// EventPreCompact fires just before a session's conversation is
	// summarized, manual or automatic.
	EventPreCompact = config.EventPreCompact
	// EventPostCompact fires once a session's conversation has been
	// summarized, with the summary.
	EventPostCompact = config.EventPostCompact
	// EventSessionEnd fires when a session ends; its matcher is tested
	// against the reason.
	EventSessionEnd = config.EventSessionEnd
)

// HaltExitCode is the exit code that halts the whole turn. 2 blocks the
// current tool call; 49 sits in the no-man's-land between the
// generic-error range (1-30), the sysexits range (64-78), and the
// killed-by-signal range (128+) so it can't be hit by accident.
const HaltExitCode = 49

// CouldNotEvaluateExitCode is the exit code a hook gives when it could
// not check what it was asked to: the tool it runs is missing, say, or
// what it reads is not there. Klash fails closed on it, as on a hook that
// could not run at all: it blocks as exit 2 does, so a check that did not
// run never passes for one that did. Stderr says why. 3 is the UNKNOWN
// of monitoring checks, beside their 0 for OK and 2 for CRITICAL.
const CouldNotEvaluateExitCode = 3

// HookMetadata is embedded in tool response metadata so the UI can
// display a hook indicator.
type HookMetadata struct {
	HookCount    int        `json:"hook_count"`
	Decision     string     `json:"decision"`
	Halt         bool       `json:"halt,omitempty"`
	Reason       string     `json:"reason,omitempty"`
	InputRewrite bool       `json:"input_rewrite,omitempty"`
	Hooks        []HookInfo `json:"hooks,omitempty"`
}

// HookInfo identifies a single hook that ran and its individual result.
type HookInfo struct {
	Name string `json:"name"`
	// Event is the event the hook ran for. A tool call's metadata lists
	// its PreToolUse and PostToolUse hooks together, and a deny means
	// something different for each: a blocked call, or feedback on a
	// call that already ran.
	Event        string `json:"event,omitempty"`
	Matcher      string `json:"matcher,omitempty"`
	Decision     string `json:"decision"`
	Halt         bool   `json:"halt,omitempty"`
	Reason       string `json:"reason,omitempty"`
	InputRewrite bool   `json:"input_rewrite,omitempty"`
	// Error is set when the hook did not run to a verdict: it timed out,
	// or exited non-zero with a code that is not a decision. See
	// HookResult.Error.
	Error string `json:"error,omitempty"`
	// CouldNotEvaluate is set when the hook could not check what it was
	// asked to, so it blocked. See HookResult.CouldNotEvaluate.
	CouldNotEvaluate bool `json:"could_not_evaluate,omitempty"`
	// Message is what the hook asked to show the user, its
	// systemMessage. See HookResult.SystemMessage.
	Message string `json:"message,omitempty"`
}

// Decision represents the outcome of a single hook execution.
type Decision int

const (
	// DecisionNone means the hook expressed no opinion.
	DecisionNone Decision = iota
	// DecisionAllow means the hook explicitly allowed the action.
	DecisionAllow
	// DecisionDeny means the hook blocked the action.
	DecisionDeny
	// DecisionAsk means the hook wants the user to confirm the tool call
	// (PreToolUse's permissionDecision "ask"), even one that would run
	// without asking otherwise. It counts as no opinion for any other
	// event.
	DecisionAsk
)

func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "allow"
	case DecisionDeny:
		return "deny"
	case DecisionAsk:
		return "ask"
	default:
		return "none"
	}
}

// HookResult holds the parsed output of a single hook execution.
type HookResult struct {
	Decision     Decision
	Halt         bool   // If true, halt the whole turn.
	Reason       string // Deny or halt reason (same field, different audience).
	Context      string
	UpdatedInput string // Shallow-merge patch against tool_input (opaque JSON).

	// Error is set when the hook did not run to a verdict (see
	// CouldNotEvaluate), so that it is not mistaken for a hook that ran
	// and simply had no opinion: that is how a safety hook pointed at the
	// wrong path came to be shown as a green "OK" on every call while
	// checking nothing.
	Error string

	// CouldNotEvaluate is set when the hook did not check what it was
	// asked to: it said so (CouldNotEvaluateExitCode), or it could not run
	// at all (never started, not found, not executable, timed out). Such a
	// result is a DecisionDeny, so it blocks wherever a hook can, and its
	// Reason says why it could not check: a check that did not run must
	// not pass. "ffmpeg exited 0" once passed for "the audio is fine".
	// An exit with another code counts when the hook says why on stderr,
	// as a check that crashed does; a silent one is a test that came out
	// false, no opinion.
	CouldNotEvaluate bool

	// SystemMessage is what the hook asked to show the user, as Claude
	// Code's "systemMessage" is: a warning that blocks nothing. It is the
	// way for a hook to warn, now that an error on stderr blocks.
	SystemMessage string

	// rewriteRejected records that the hook's tool-input rewrite was
	// malformed, so that no allow it gives, wherever in its output, is
	// taken (see rejectRewrite).
	rewriteRejected bool
}

// AggregateResult holds the combined outcome of all hooks for an event.
type AggregateResult struct {
	Decision     Decision
	Halt         bool       // Any hook requested halt.
	HookCount    int        // Number of hooks that ran.
	Hooks        []HookInfo // Info about each hook that ran (config order).
	Reason       string     // Concatenated deny/halt reasons (newline-separated).
	AskReason    string     // Concatenated reasons of the hooks that asked (newline-separated).
	Context      string     // Concatenated context from all hooks.
	UpdatedInput string     // Merged tool_input JSON (empty if no patches).

	// Objected is set when a hook halted or refused with a verdict of its
	// own. A hook that could not evaluate makes Decision a deny too, so
	// that it blocks wherever a hook can; where none can, as for
	// SessionStart, it is a failure to report, not an objection.
	Objected bool
}

// aggregate merges multiple HookResults into a single AggregateResult.
// Results are processed in config order (the order of the slice). Deny
// wins over ask, ask over allow, and allow over none, as in Claude Code,
// so one hook's ask is not overruled by another's allow. Halt is sticky. Reasons and
// context concatenate in order. updated_input patches shallow-merge in
// order against the original tool input; later patches override earlier
// ones on colliding keys.
func aggregate(results []HookResult, origToolInput string) AggregateResult {
	var (
		decision Decision
		halt     bool
		objected bool
		reasons  []string
		asks     []string
		contexts []string
		merged   = origToolInput
		anyPatch = false
	)
	for _, r := range results {
		switch r.Decision {
		case DecisionDeny:
			decision = DecisionDeny
			if r.Reason != "" {
				reasons = append(reasons, r.Reason)
			}
		case DecisionAsk:
			if decision != DecisionDeny {
				decision = DecisionAsk
			}
			if r.Reason != "" {
				asks = append(asks, r.Reason)
			}
		case DecisionAllow:
			if decision == DecisionNone {
				decision = DecisionAllow
			}
		case DecisionNone:
			// No change.
		}
		if r.Halt || r.Decision == DecisionDeny && !r.CouldNotEvaluate {
			objected = true
		}
		if r.Halt {
			halt = true
			if r.Reason != "" && r.Decision != DecisionDeny {
				// A halting hook that didn't also deny still contributes
				// its reason so the user sees it.
				reasons = append(reasons, r.Reason)
			}
		}
		if r.Context != "" {
			contexts = append(contexts, r.Context)
		}
		if r.UpdatedInput != "" {
			next, err := shallowMerge(merged, r.UpdatedInput)
			if err != nil {
				slog.Warn(
					"Hook updated_input patch rejected; ignoring",
					"error", err,
					"patch", r.UpdatedInput,
				)
				continue
			}
			merged = next
			anyPatch = true
		}
	}

	agg := AggregateResult{
		Decision:  decision,
		Halt:      halt,
		Objected:  objected,
		HookCount: len(results),
	}
	if anyPatch {
		agg.UpdatedInput = merged
	}
	if len(reasons) > 0 {
		agg.Reason = strings.Join(reasons, "\n")
	}
	if len(asks) > 0 {
		agg.AskReason = strings.Join(asks, "\n")
	}
	if len(contexts) > 0 {
		agg.Context = strings.Join(contexts, "\n")
	}
	return agg
}

// shallowMerge applies a top-level-keys patch to base (both JSON
// objects). Keys in patch overwrite keys in base; keys absent from the
// patch are preserved. Returns an error if either value is not a valid
// JSON object.
func shallowMerge(base, patch string) (string, error) {
	if base == "" {
		base = "{}"
	}
	// Ensure base is an object so sjson has somewhere to write.
	var baseAny any
	if err := json.Unmarshal([]byte(base), &baseAny); err != nil {
		return "", err
	}
	if _, ok := baseAny.(map[string]any); !ok {
		return "", errNotObject("tool_input")
	}
	var patchMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(patch), &patchMap); err != nil {
		return "", errNotObject("updated_input")
	}
	out := base
	for k, v := range patchMap {
		next, err := sjson.SetRawBytes([]byte(out), k, v)
		if err != nil {
			return "", err
		}
		out = string(next)
	}
	return out, nil
}

type errNotObject string

func (e errNotObject) Error() string { return string(e) + " is not a JSON object" }
