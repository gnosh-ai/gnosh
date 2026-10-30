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
	"strings"
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
	// permissionRequest answers a call's permission dialog in the user's
	// place; nil when no PermissionRequest hooks are configured.
	permissionRequest *hooks.Runner
	// files and workingDir let a PostToolUse hook rewrite the file a call
	// just wrote (a formatter, most likely) without the agent's next edit
	// of it being refused as stale. files may be nil, as in tests.
	files      filetracker.Service
	workingDir string
	// permissions puts a call a PreToolUse hook answered "ask" for to the
	// user, when there is one: interactive is false where no one can
	// answer, as in gnosh run. Without them, as in tests, such a call is
	// refused.
	permissions permission.Service
	interactive bool
	// dontAsk is Claude Code's dontAsk mode, where the permission service
	// denies what it would ask about: the model is told why the call did
	// not run, and the turn goes on.
	dontAsk bool
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
// hookedTool, or the original slice when no hooks fire around tool calls.
// A sub-agent's tools are wrapped too, as in Claude Code.
func wrapToolsWithHooks(tools []fantasy.AgentTool, th toolHooks) []fantasy.AgentTool {
	if th.pre == nil && th.post == nil && th.postFailure == nil && th.permissionRequest == nil && !th.dontAsk {
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

func (h *hookedTool) Unwrap() fantasy.AgentTool { return h.inner }

// innermostTool is tool without the wrappers Gnosh puts around tools
// (hooks, user descriptions, the fetch agent's confinement), each of which
// says what it wraps with Unwrap: the tool itself, with the methods a
// wrapper does not pass on, such as an MCP tool's MCP. With hooks or user
// descriptions configured, every tool is wrapped, and a channel turn's
// tools were no longer kept to its own channel: filterToolsForChannel
// could not tell an MCP tool from a built-in one.
func innermostTool(tool fantasy.AgentTool) fantasy.AgentTool {
	for {
		w, ok := tool.(interface{ Unwrap() fantasy.AgentTool })
		if !ok {
			return tool
		}
		tool = w.Unwrap()
	}
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
	// An MCP tool's matchers also see its Claude Code name.
	if m, ok := innermostTool(h.inner).(interface {
		MCP() string
		MCPToolName() string
	}); ok {
		ctx = hooks.WithToolAliases(ctx, "mcp__"+m.MCP()+"__"+m.MCPToolName())
	}
	var notAsked *permission.NotAsked
	if h.hooks.dontAsk {
		ctx, notAsked = permission.WithNotAsked(ctx, call.ID)
	}
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
	// to the normal permission flow. An ask puts the call to the user
	// first, and once they allow it, it needs no other approval.
	switch result.Decision {
	case hooks.DecisionAllow:
		ctx = permission.WithHookApproval(ctx, call.ID)
	case hooks.DecisionAsk:
		if resp, ok, err := h.confirm(ctx, sessionID, call, result); !ok {
			if err == nil && notAsked.Denied() {
				return notAskedResponse(result), nil
			}
			return resp, err
		}
		ctx = permission.WithHookApproval(ctx, call.ID)
	}

	// If the call's permission dialog comes, its PermissionRequest hooks
	// answer it first.
	var asked hooks.AggregateResult
	var answer permission.HookAnswer
	if h.hooks.permissionRequest != nil {
		ctx = permission.WithHookAnswer(ctx, call.ID, func(ctx context.Context) permission.HookAnswer {
			answer, asked = h.answerPermission(ctx, sessionID, call)
			return answer
		})
	}

	start := time.Now()
	resp, err := h.inner.Run(ctx, call)
	if err != nil {
		return resp, err
	}
	result = combineHookResults(result, asked)
	if answer.Deny {
		// The tool took the denial as a user's, which ends the turn. A
		// hook's deny tells the model why and lets it go on, as in Claude
		// Code, unless the hook halted.
		resp = fantasy.NewTextErrorResponse("Tool call denied by a PermissionRequest hook. Reason: " + cmp.Or(deniedReason(asked), "none given"))
		resp.StopTurn = asked.Halt
		resp.Metadata = hookMetadataJSON(result)
		return resp, nil
	}
	if notAsked.Denied() {
		return notAskedResponse(result), nil
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

// confirm puts a call a PreToolUse hook answered "ask" for to the user, as
// Claude Code does: even a call that would run without asking (yolo,
// --allowedTools, acceptEdits, a tool that never asks) waits for their
// answer, with the hook's reason. Where no one can answer, in gnosh run,
// the call is refused with that reason, as Claude Code refuses one in a
// -p run. A sub-agent's call is asked about as the main agent's is, even
// agentic_fetch's, whose session grants its own requests. A
// hook's "ask" used to count as no opinion here, so the calls it asked
// about ran unasked wherever nothing else asked.
func (h *hookedTool) confirm(ctx context.Context, sessionID string, call fantasy.ToolCall, result hooks.AggregateResult) (fantasy.ToolResponse, bool, error) {
	reason := cmp.Or(result.AskReason, "no reason given")
	refuse := func(resp fantasy.ToolResponse) (fantasy.ToolResponse, bool, error) {
		resp.Metadata = hookMetadataJSON(result)
		return resp, false, nil
	}
	if h.hooks.permissions == nil || !h.hooks.interactive {
		// A PermissionRequest hook can answer where no one else can, as in
		// Claude Code, which denies the call when none allows it.
		if answer, _ := h.answerPermission(ctx, sessionID, call); answer.Allow {
			return fantasy.ToolResponse{}, true, nil
		}
		return refuse(fantasy.NewTextErrorResponse("Tool call blocked: a hook asks the user to confirm it, and no one is here to answer. Reason: " + reason))
	}
	// The question and the approval that answers it are the call's, by
	// its ID: without one neither can be told from another call's.
	if call.ID == "" {
		return refuse(fantasy.NewTextErrorResponse("Tool call blocked: a hook asks the user to confirm it, and the call has no ID to ask about. Reason: " + reason))
	}
	granted, err := h.hooks.permissions.Request(permission.WithHookAsk(ctx, call.ID), permission.CreatePermissionRequest{
		SessionID:   sessionID,
		ToolCallID:  call.ID,
		ToolName:    call.Name,
		Action:      permission.HookAskAction,
		Description: "A PreToolUse hook asks you to confirm this call: " + reason,
		Params:      call.Input,
		Path:        h.hooks.workingDir,
	})
	if err != nil {
		return fantasy.ToolResponse{}, false, err
	}
	if !granted {
		return refuse(tools.NewPermissionDeniedResponse())
	}
	return fantasy.ToolResponse{}, true, nil
}

// notAskedResponse is what the model reads of a call whose permission
// request was denied unasked under dontAsk. The tool took the denial as a
// user's, which ends the turn; here the turn goes on, as in Claude Code,
// where dontAsk is for runs no one watches.
func notAskedResponse(result hooks.AggregateResult) fantasy.ToolResponse {
	resp := fantasy.NewTextErrorResponse("Permission denied: Gnosh runs with --permission-mode dontAsk, which denies a call it would ask about instead of asking. Only read-only commands and calls allowed with --allowedTools or permissions.allow run.")
	if result.HookCount > 0 {
		resp.Metadata = hookMetadataJSON(result)
	}
	return resp
}

// answerPermission runs the PermissionRequest hooks of call, whose
// permission dialog is due, and turns what they said into the answer for
// the dialog: a deny from any hook denies (a halt too), else an allow from
// one allows; a hook that could not check leaves the user to answer, with
// a note on why. An allow with changed input cannot be honored here, after
// the tool read its input, so the user is asked about the input as it is.
func (h *hookedTool) answerPermission(ctx context.Context, sessionID string, call fantasy.ToolCall) (permission.HookAnswer, hooks.AggregateResult) {
	if h.hooks.permissionRequest == nil {
		return permission.HookAnswer{}, hooks.AggregateResult{}
	}
	result, err := h.hooks.permissionRequest.Run(ctx, hooks.EventPermissionRequest, sessionID, call.Name, call.Input)
	if err != nil {
		slog.Warn("PermissionRequest hook execution error; asking the user", "tool", call.Name, "error", err)
		return permission.HookAnswer{}, result
	}
	var couldNot []string
	for _, hook := range result.Hooks {
		if hook.CouldNotEvaluate {
			couldNot = append(couldNot, hook.Reason)
		}
	}
	switch {
	case result.Objected:
		return permission.HookAnswer{Deny: true}, result
	case len(couldNot) > 0:
		return permission.HookAnswer{Note: "A PermissionRequest hook could not answer this, so you are asked: " + strings.Join(couldNot, "; ")}, result
	case result.Decision == hooks.DecisionAllow && result.UpdatedInput != "":
		return permission.HookAnswer{Note: "A PermissionRequest hook allowed this with changed input, which Gnosh cannot run, so you are asked about the input as it is."}, result
	case result.Decision == hooks.DecisionAllow:
		return permission.HookAnswer{Allow: true}, result
	}
	return permission.HookAnswer{}, result
}

// deniedReason is the reasons of the hooks in result that denied the call,
// not counting those that could not check.
func deniedReason(result hooks.AggregateResult) string {
	var reasons []string
	for _, hook := range result.Hooks {
		if hook.Decision == hooks.DecisionDeny.String() && !hook.CouldNotEvaluate && hook.Reason != "" {
			reasons = append(reasons, hook.Reason)
		}
	}
	return strings.Join(reasons, "\n")
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
	case tools.EditToolName, tools.MultiEditToolName, tools.WriteToolName, tools.ReplaceSymbolToolName:
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
