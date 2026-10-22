package agent

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/filepathext"
	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/tidwall/sjson"
)

// toolHooks holds what hookedTool needs to run the hooks configured
// around tool calls.
type toolHooks struct {
	pre         *hooks.Runner // PreToolUse hooks; nil when none are configured.
	post        *hooks.Runner // PostToolUse hooks; nil when none are configured.
	postFailure *hooks.Runner // PostToolUseFailure hooks; nil when none are configured.
	// files and workingDir let a PostToolUse hook rewrite the file a call
	// just wrote (a formatter, most likely) without the agent's next edit
	// of it being refused as stale. files may be nil, as in tests.
	files      filetracker.Service
	workingDir string
}

// hookedTool wraps a fantasy.AgentTool to run PreToolUse hooks before
// delegating to the inner tool, and PostToolUse hooks once it succeeds.
type hookedTool struct {
	inner fantasy.AgentTool
	hooks toolHooks
}

func newHookedTool(inner fantasy.AgentTool, th toolHooks) *hookedTool {
	return &hookedTool{inner: inner, hooks: th}
}

// wrapToolsWithHooks returns a tool slice with each entry wrapped in a
// hookedTool. Returns the original slice unchanged when no hooks fire
// around tool calls or when isSubAgent is true — sub-agents never fire
// hooks, the top-level invocation of the sub-agent tool itself is wrapped
// on the caller's side.
func wrapToolsWithHooks(tools []fantasy.AgentTool, th toolHooks, isSubAgent bool) []fantasy.AgentTool {
	if (th.pre == nil && th.post == nil && th.postFailure == nil) || isSubAgent {
		return tools
	}
	out := make([]fantasy.AgentTool, len(tools))
	for i, tool := range tools {
		out[i] = newHookedTool(tool, th)
	}
	return out
}

func (h *hookedTool) Info() fantasy.ToolInfo {
	return h.inner.Info()
}

func (h *hookedTool) ProviderOptions() fantasy.ProviderOptions {
	return h.inner.ProviderOptions()
}

func (h *hookedTool) SetProviderOptions(opts fantasy.ProviderOptions) {
	h.inner.SetProviderOptions(opts)
}

func (h *hookedTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	sessionID := tools.GetSessionFromContext(ctx)
	ctx = hooks.WithToolUseID(ctx, call.ID)
	var result hooks.AggregateResult
	if h.hooks.pre != nil {
		var err error
		result, err = h.hooks.pre.Run(ctx, hooks.EventPreToolUse, sessionID, call.Name, call.Input)
		if err != nil {
			slog.Warn("Hook execution error, proceeding with tool call",
				"tool", call.Name, "error", err)
		}
	}

	if result.Decision == hooks.DecisionDeny || result.Halt {
		reason := fmt.Sprintf("Tool call blocked by hook. Reason: %s", result.Reason)
		if result.Halt {
			reason = fmt.Sprintf("Turn halted by hook. Reason: %s", result.Reason)
		}
		resp := fantasy.NewTextErrorResponse(reason)
		// Halt ends the whole turn; a plain deny only blocks this tool
		// call so the model can see the error and try something else.
		resp.StopTurn = result.Halt
		resp.Metadata = hookMetadataJSON(result)
		return resp, nil
	}

	if result.UpdatedInput != "" {
		call.Input = result.UpdatedInput
	}

	// An explicit allow from a hook pre-approves the permission prompt for
	// this tool call. Deny is already handled above; silence falls through
	// to the normal permission flow.
	if result.Decision == hooks.DecisionAllow {
		ctx = permission.WithHookApproval(ctx, call.ID)
	}

	start := time.Now()
	resp, err := h.inner.Run(ctx, call)
	if err != nil {
		return resp, err
	}

	// The PostToolUse or PostToolUseFailure hooks see what the tool
	// returned, before anything is added to it.
	post, notes := h.runPostToolUse(ctx, sessionID, call, resp, time.Since(start))
	if result.Context != "" {
		notes = append([]string{result.Context}, notes...)
	}
	for _, note := range notes {
		if resp.Content != "" {
			resp.Content += "\n"
		}
		resp.Content += note
	}
	if post.Halt {
		resp.StopTurn = true
	}

	resp.Metadata = mergeHookMetadata(resp.Metadata, combineHookResults(result, post))
	return resp, nil
}

// runPostToolUse runs the hooks that follow a call: PostToolUse for one
// that succeeded, handed what the tool returned, or PostToolUseFailure for
// one that failed, handed its error, as in Claude Code. It returns their
// result and what the agent should read after the tool's output: a
// block's reason, any context, and a note when a hook changed the file
// the call just wrote.
func (h *hookedTool) runPostToolUse(ctx context.Context, sessionID string, call fantasy.ToolCall, resp fantasy.ToolResponse, took time.Duration) (hooks.AggregateResult, []string) {
	var (
		event   string
		result  hooks.AggregateResult
		err     error
		written string
		before  fileState
	)
	switch {
	case !resp.IsError && h.hooks.post != nil:
		event = hooks.EventPostToolUse
		written = h.writtenFile(call)
		before = statFile(written)
		result, err = h.hooks.post.RunPostToolUse(ctx, sessionID, call.Name, call.Input, resp.Content, resp.Metadata, took)
	case resp.IsError && h.hooks.postFailure != nil:
		event = hooks.EventPostToolUseFailure
		result, err = h.hooks.postFailure.RunPostToolUseFailure(ctx, sessionID, call.Name, call.Input, resp.Content, ctx.Err() != nil, took)
	default:
		return hooks.AggregateResult{}, nil
	}
	if err != nil {
		slog.Warn("Hook execution error after a tool call", "event", event, "tool", call.Name, "error", err)
	}

	var notes []string
	reason := cmp.Or(result.Reason, "no reason given")
	switch {
	case result.Halt:
		notes = append(notes, "Turn halted by a "+event+" hook. Reason: "+reason)
	case result.Decision == hooks.DecisionDeny:
		// The call has run; a block is the hook's feedback on it.
		notes = append(notes, event+" hook feedback: "+reason)
	}
	if result.Context != "" {
		notes = append(notes, result.Context)
	}
	if note := h.resyncWrittenFile(ctx, sessionID, written, before); note != "" {
		notes = append(notes, note)
	}
	return result, notes
}

// writtenFile is the file a file-writing call just wrote, resolved as the
// tool resolves it, or "" for any other call.
func (h *hookedTool) writtenFile(call fantasy.ToolCall) string {
	if h.hooks.files == nil {
		return ""
	}
	switch call.Name {
	case tools.EditToolName, tools.MultiEditToolName, tools.WriteToolName:
	default:
		return ""
	}
	var input struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal([]byte(call.Input), &input); err != nil || input.FilePath == "" {
		return ""
	}
	return filepathext.SmartJoin(h.hooks.workingDir, input.FilePath)
}

// fileState is what resyncWrittenFile compares to tell whether a hook
// touched a file: its modification time, and a hash of its content.
type fileState struct {
	exists  bool
	modTime time.Time
	sum     [sha256.Size]byte
}

func statFile(path string) fileState {
	if path == "" {
		return fileState{}
	}
	info, err := os.Stat(path)
	if err != nil {
		return fileState{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileState{}
	}
	return fileState{exists: true, modTime: info.ModTime(), sum: sha256.Sum256(data)}
}

// resyncWrittenFile handles a PostToolUse hook that touched the file the
// call wrote, most often a formatter run over it. Touched at all, the
// file would count as modified since the agent last read it, and its
// next edit would be refused, so a fresh read is recorded. Changed, the
// agent is told, since its idea of the file is now out of date where the
// hook changed it. Claude Code does the same.
func (h *hookedTool) resyncWrittenFile(ctx context.Context, sessionID, path string, before fileState) string {
	if path == "" || !before.exists {
		return ""
	}
	after := statFile(path)
	if after == before {
		return ""
	}
	if !after.exists {
		return fmt.Sprintf("A PostToolUse hook removed %s after this call.", path)
	}
	h.hooks.files.RecordRead(ctx, sessionID, path)
	if after.sum == before.sum {
		return ""
	}
	return fmt.Sprintf("A PostToolUse hook changed %s after this call (a formatter, most likely). "+
		"Your next edit of it will not be refused as stale, but view it again first if you edit near what the hook changed.", path)
}

// combineHookResults lists a call's PreToolUse and PostToolUse hooks
// together for its metadata, so its tool line shows both. The call's
// decision is the PreToolUse one: a PostToolUse block is feedback on a
// call that already ran, and its hooks say so through their event.
func combineHookResults(pre, post hooks.AggregateResult) hooks.AggregateResult {
	if post.HookCount == 0 {
		return pre
	}
	combined := pre
	combined.HookCount += post.HookCount
	combined.Hooks = append(slices.Clone(pre.Hooks), post.Hooks...)
	combined.Halt = pre.Halt || post.Halt
	return combined
}

// buildHookMetadata creates a HookMetadata from an AggregateResult.
func buildHookMetadata(result hooks.AggregateResult) hooks.HookMetadata {
	return hooks.HookMetadata{
		HookCount:    result.HookCount,
		Decision:     result.Decision.String(),
		Halt:         result.Halt,
		Reason:       result.Reason,
		InputRewrite: result.UpdatedInput != "",
		Hooks:        result.Hooks,
	}
}

// hookMetadataJSON builds a JSON string containing only the hook metadata.
func hookMetadataJSON(result hooks.AggregateResult) string {
	meta := buildHookMetadata(result)
	data, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return `{"hook":` + string(data) + `}`
}

// mergeHookMetadata injects hook metadata into existing tool metadata.
func mergeHookMetadata(existing string, result hooks.AggregateResult) string {
	if result.HookCount == 0 {
		return existing
	}
	meta := buildHookMetadata(result)
	data, err := json.Marshal(meta)
	if err != nil {
		return existing
	}
	if existing == "" {
		existing = "{}"
	}
	merged, err := sjson.SetRaw(existing, "hook", string(data))
	if err != nil {
		return existing
	}
	return merged
}
