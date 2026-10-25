// Package agent is the core orchestration layer for Crush AI agents.
//
// It provides session-based AI agent functionality for managing
// conversations, tool execution, and message handling. It coordinates
// interactions between language models, messages, sessions, and tools while
// handling features like automatic summarization, queuing, and token
// management.
package agent

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"charm.land/fantasy/providers/bedrock"
	"charm.land/fantasy/providers/google"
	"charm.land/fantasy/providers/openai"
	"charm.land/fantasy/providers/openrouter"
	"charm.land/fantasy/providers/vercel"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent/hyper"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/env"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/stringext"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/charmtone"
)

const (
	DefaultSessionName = "Untitled Session"

	// Constants for auto-summarization thresholds
	largeContextWindowThreshold = 200_000
	largeContextWindowBuffer    = 20_000
	smallContextWindowRatio     = 0.2

	// unknownContextWindowFallback bounds a session when the model's real
	// context window is unknown (discovery failed or the provider does not
	// report one). It is a conservative floor: modern local coding models are
	// at least this large, so compacting at this budget avoids the hard
	// "exceeds context" rejection for any of them, at the cost of compacting a
	// genuinely larger model earlier than necessary.
	unknownContextWindowFallback = 32_768
)

// autoSummarize reports whether a session whose running token total is tokens
// should be compacted before the next request, given the model's context
// window cw and the auto_compact_window option window (see compactAt,
// which reads the variables and the option when overrides is set). It
// returns false when auto-summarize is disabled.
//
// When cw is unknown (<= 0, e.g. a local or custom model whose window could
// not be discovered) it falls back to unknownContextWindowFallback instead of
// giving up: without a limit the session grows until the provider hard-rejects
// the request ("request exceeds the available context size"), which dead-ends
// the session. Over-summarizing a model that is in fact larger is wasteful but
// recoverable; crashing is not. A discovered real window is always preferred.
func autoSummarize(cw, window, tokens int64, disabled, overrides bool) bool {
	return !disabled && tokens >= compactAt(cw, window, overrides)
}

var userAgent = fmt.Sprintf("Charm-Crush/%s (https://charm.land/crush)", version.Version)

//go:embed templates/title.md
var titlePrompt []byte

//go:embed templates/summary.md
var summaryPrompt []byte

// Used to remove <think> tags from generated titles.
var (
	thinkTagRegex       = regexp.MustCompile(`(?s)<think>.*?</think>`)
	orphanThinkTagRegex = regexp.MustCompile(`</?think>`)
)

type SessionAgentCall struct {
	SessionID string
	// RunID, when non-empty, is the caller-supplied correlator that
	// gets echoed back on the notify.RunComplete event emitted for
	// this turn. It is preserved when the call is enqueued behind a
	// busy session so the queued turn's terminal event is still
	// recognisable to the original caller. Callers that need a
	// reliable completion contract (e.g. `crush run` against a
	// session that may be busy) MUST set it; SessionID alone is
	// ambiguous when concurrent turns share the same session.
	RunID             string
	Channel           string
	HiddenUserMessage bool
	// NoticeUserMessage makes a hidden prompt a notice the chat shows
	// (message.TextContent.Notice).
	NoticeUserMessage bool
	Prompt            string
	ProviderOptions   fantasy.ProviderOptions
	Attachments       []message.Attachment
	MaxOutputTokens   int64
	Temperature       *float64
	TopP              *float64
	TopK              *int64
	FrequencyPenalty  *float64
	PresencePenalty   *float64
	NonInteractive    bool
	// OnComplete, when non-nil, replaces the default RunComplete
	// publish path: the inner Run hands the terminal payload to this
	// callback instead of emitting it on the RunComplete broker. The
	// coordinator uses this hook to coalesce the unauthorized →
	// re-auth → retry chain into a single user-visible terminal
	// event, so non-interactive clients (e.g. `crush run`) don't
	// exit on a stale failed-attempt RunComplete before the
	// successful retry. It is intentionally stripped when queueing
	// a busy-session call (see Run): the originating
	// coordinator.Run has long returned by the time the queued
	// recursion drains, so falling back to the default broker
	// publish keeps the event visible to subscribers.
	OnComplete func(notify.RunComplete)
	// Accepted, when non-nil, is the accept reservation taken by
	// BeginAccepted before the call was dispatched onto a goroutine
	// (the client/server fire-and-forget path). Run consumes it under
	// dispatchMu[SessionID] once the accepted -> (cancel-on-entry |
	// queued | active) transition has been chosen. When nil
	// (in-process / local callers like AppWorkspace), behavior is
	// unchanged and no accept tracking applies.
	Accepted *AcceptedRun
	// acceptSeq carries the accept sequence of the handle that produced
	// this call after it has been enqueued and its Accepted handle
	// stripped. The queue-drain paths compare it against a session's
	// cancel mark so a follow-up queued before a cancel is dropped while
	// one queued after the cancel survives. 0 means untracked (an
	// in-process enqueue with no accept reservation), which the drain
	// paths treat as covered by any present mark, preserving the
	// pre-sequence behavior.
	acceptSeq uint64
	// stopHookDepth counts how many times in a row a Stop hook has
	// refused to let this turn end. It rides along on the queued
	// continuation so the cap in stopHookContinuation is enforced
	// across the recursive run path; a prompt the user actually typed
	// always starts at 0.
	stopHookDepth int
	// stopHookReason is the objection the last Stop hook raised, carried
	// alongside stopHookDepth so that if the agent later judges it should
	// stop anyway, the user can be told what was still unresolved.
	stopHookReason string
	// stopHookReflecting marks a continuation sent at a reflection
	// checkpoint, where the agent was asked whether there is a genuine
	// reason to stop. Only on such a turn does StopMarker in its reply
	// end the turn; elsewhere the Stop hook stays in charge.
	stopHookReflecting bool
	// goalChecks counts how many checks in a row found the session's goal
	// not met, riding along on the queued continuation as stopHookDepth
	// does, and goalReflecting marks a continuation sent at a reflection
	// checkpoint (see goalContinuation).
	goalChecks     int
	goalReflecting bool
	// OnAuthRefresh, when non-nil, is called by fantasy when a stream
	// fails with an authentication error (HTTP 401). The callback should
	// refresh credentials and return nil on success, in which case
	// fantasy retries the stream transparently. Returning an error
	// surfaces the original auth error without retry.
	OnAuthRefresh func(ctx context.Context, err *fantasy.ProviderError) error
	// promptHooksRan marks a prompt the user sent that has been past its
	// UserPromptSubmit hooks, which run once, when it is sent, so a
	// queued prompt that runs later does not run them again.
	// promptHookContext is what they added for the model to read with
	// the prompt. carriesSessionStart marks a prompt that carries what the
	// SessionStart hooks printed, for it or for an earlier prompt that was
	// refused: that goes to the model with it whether or not it is
	// hidden, and a continuation copied from it (a Stop hook's, a loop's)
	// carries none of it a second time.
	promptHooksRan      bool
	promptHookContext   string
	carriesSessionStart bool
	// channelMeta holds the attributes of the <channel> element a
	// channel-originated turn was started with, parsed once at Run
	// entry. It survives the auto-summarize continuation, whose Prompt
	// is rewritten and no longer carries the element, so the reply
	// target is not lost when a long channel turn is summarized.
	channelMeta map[string]string
}

// filterToolsForChannel scopes the tool list for a turn. A channel-originated
// turn (channel != "") sees only the originating channel server's tools plus
// all non-channel tools — the model's reach is restricted to the channel it
// is replying through, so it cannot accidentally send via a different
// messaging backend. A local turn (channel == "") keeps every tool,
// including channel server tools, so a user in the TUI can still ask the
// agent to send a message through Signal or any other enabled channel.
func filterToolsForChannel(agentTools []fantasy.AgentTool, channel string, states map[string]mcp.ClientInfo) []fantasy.AgentTool {
	if channel == "" {
		return agentTools
	}
	filtered := make([]fantasy.AgentTool, 0, len(agentTools))
	for _, agentTool := range agentTools {
		mcpTool, ok := innermostTool(agentTool).(interface{ MCP() string })
		if !ok {
			filtered = append(filtered, agentTool)
			continue
		}
		state, found := states[mcpTool.MCP()]
		if !found || !state.Channel || channel == mcpTool.MCP() {
			filtered = append(filtered, agentTool)
		}
	}
	return filtered
}

type SessionAgent interface {
	Run(context.Context, SessionAgentCall) (*fantasy.AgentResult, error)
	BeginAccepted(sessionID string) *AcceptedRun
	SetModels(large Model, small Model)
	SetTools(tools []fantasy.AgentTool)
	// ToolNames names the tools the agent offers the model.
	ToolNames() []string
	SetSystemPrompt(systemPrompt string)
	Cancel(sessionID string)
	CancelAll()
	IsSessionBusy(sessionID string) bool
	IsBusy() bool
	ListSettledMessages(ctx context.Context, sessionID string) ([]message.Message, error)
	QueuedPrompts(sessionID string) int
	QueuedPromptsList(sessionID string) []string
	PopQueuedPrompts(sessionID string) []message.PoppedPrompt
	ClearQueue(sessionID string)
	// Summarize summarizes the session, with instructions the text given
	// after /compact ("" for none), as Claude Code's /compact takes them.
	Summarize(ctx context.Context, sessionID, instructions string, opts fantasy.ProviderOptions, onAuthRefresh func(context.Context, *fantasy.ProviderError) error, trigger preCompactTrigger) error
	Model() Model
	GenerateTitle(ctx context.Context, sessionID, userPrompt string)
	GenerateSessionName(ctx context.Context, sessionID string) (string, error)
}

type Model struct {
	Model      fantasy.LanguageModel
	CatwalkCfg catwalk.Model
	ModelCfg   config.SelectedModel
	FlatRate   bool
}

// activeCancel wraps a context.CancelFunc with a unique pointer identity.
// The pointer is used for compare-and-delete in the dispatch completion path:
// when a finishing run's deferred cleanup fires, it must only remove its own
// entry — not a newer run's entry that was installed in the window between
// the explicit Del and the function return.
type activeCancel struct {
	cancel context.CancelFunc
}

type sessionAgent struct {
	largeModel         *csync.Value[Model]
	smallModel         *csync.Value[Model]
	systemPromptPrefix *csync.Value[string]
	systemPrompt       *csync.Value[string]
	tools              *csync.Slice[fantasy.AgentTool]

	isSubAgent bool
	agentType  string // see SessionAgentOptions.AgentType
	sessions   session.Service
	messages   message.Service
	// cfg backs channel reply routing (config lookup + MCP tool
	// invocation). Nil in tests and sub-agents that never see channel
	// turns; sendChannelReply treats nil as "routing disabled".
	cfg                  *config.ConfigStore
	disableAutoSummarize bool
	// goals are the sessions' goals, shared with the coordinator that sets
	// them; nil, there are none (see goalContinuation).
	goals *csync.Map[string, Goal]
	// idleCompaction holds each session's idle-compaction timer, armed
	// when a turn ends and stopped when the next starts; idleCompactAfter
	// is how long it waits (see armIdleCompaction).
	idleCompaction        *csync.Map[string, *time.Timer]
	idleCompactAfter      time.Duration
	disableIdleCompaction bool
	isYolo                bool
	permissionModeOf      func(sessionID string) string // see SessionAgentOptions.PermissionMode
	notify                pubsub.Publisher[notify.Notification]
	runComplete           pubsub.Publisher[notify.RunComplete]

	messageQueue   *csync.Map[string, []SessionAgentCall]
	activeRequests *csync.Map[string, *activeCancel]

	// dispatchMu holds a per-session mutex that serializes the
	// accepted -> (cancel-on-entry | queued | active) transition in
	// Run against a concurrent Cancel. The lock is held only during
	// the brief handoff (no DB or LLM I/O under the lock).
	dispatchMu *csync.Map[string, *sync.Mutex]
	// submitMu holds a per-session mutex that keeps prompts in the order
	// they were sent while their UserPromptSubmit hooks run; see
	// sessionSubmitMu.
	submitMu *csync.Map[string, *sync.Mutex]
	// submitting holds, per session, the cancel of each prompt whose
	// UserPromptSubmit hooks are running; see beginSubmitting. Changed
	// only under the session's dispatch lock.
	submitting *csync.Map[string, []*activeCancel]
	// acceptedRuns counts dispatched-but-not-yet-active runs per
	// session. A counter > 0 means a dispatched prompt is in flight
	// and has not yet completed the dispatch handoff in Run. Only
	// BeginAccepted increments it; only AcceptedRun.Close decrements
	// it.
	acceptedRuns *csync.Map[string, int]
	// cancelMark records, per session, a high-water accept sequence: an
	// accepted handle is canceled by it iff the handle's sequence is at
	// or below the mark. Cancel raises the mark to the latest sequence
	// assigned at cancel time, so a single Cancel covers every prompt
	// accepted-but-not-yet-active then, while a prompt accepted later
	// (higher sequence) is never poisoned. Absent or 0 means no pending
	// cancel. It is only raised by Cancel when acceptedRuns > 0, so an
	// idle Escape never records a mark.
	cancelMark *csync.Map[string, uint64]
	// justSummarized marks the sessions auto-summarized since their last
	// step, and compactLevels how far each session's auto-compaction
	// still goes: see compacts.
	justSummarized *csync.Map[string, bool]
	compactLevels  *csync.Map[string, int]
	// dispatchMuCreate guards lazy creation of per-session entries in
	// dispatchMu so two goroutines can't race to lock different mutex
	// instances for the same session.
	dispatchMuCreate sync.Mutex
	// acceptedMu serializes increments/decrements of acceptedRuns and
	// the assignment of accept sequence numbers from acceptSeqGen. It
	// is separate from dispatchMu so AcceptedRun.Close (which may run
	// while Run holds dispatchMu for the same session) does not
	// deadlock by re-entering the dispatch lock.
	acceptedMu sync.Mutex
	// acceptSeqGen is the monotonic source of accept sequence numbers.
	// Each BeginAccepted increments it under acceptedMu and stamps the
	// returned handle, so sequences strictly increase in accept order
	// across the agent. Cancel uses its current value as the per-session
	// high-water mark.
	acceptSeqGen uint64
	// sessionStarts, see SessionAgentOptions.SessionStarts.
	sessionStarts *sessionStartState
	// instructionsLoaded, see SessionAgentOptions.InstructionsLoaded.
	instructionsLoaded *instructionsLoadedState
}

type SessionAgentOptions struct {
	LargeModel           Model
	SmallModel           Model
	SystemPromptPrefix   string
	SystemPrompt         string
	IsSubAgent           bool
	DisableAutoSummarize bool
	// DisableIdleCompaction turns off compacting a long conversation left
	// idle (see armIdleCompaction).
	DisableIdleCompaction bool
	IsYolo                bool
	Sessions              session.Service
	Messages              message.Service
	Cfg                   *config.ConfigStore
	Tools                 []fantasy.AgentTool
	Notify                pubsub.Publisher[notify.Notification]
	RunComplete           pubsub.Publisher[notify.RunComplete]

	// AgentType is the agent's name for hooks, the config ID of the agent
	// it runs as (a sub-agent's is task): SubagentStop's agent_type.
	AgentType string
	// PermissionMode names a session's permission mode for its hooks,
	// unless the agent is the plan agent (see coordinator.permissionMode).
	// Nil reads as default.
	PermissionMode func(sessionID string) string
	// SessionStarts is what the agent's SessionStart hooks know of the
	// sessions the process started. A coordinator's main agents share
	// one, so a session started in code mode counts as started in plan
	// mode; nil gives the agent its own.
	SessionStarts *sessionStartState
	// InstructionsLoaded is what the agent's InstructionsLoaded hooks
	// know of the sessions the process has loaded context files for. A
	// coordinator's main agents share one, as SessionStarts; nil gives
	// the agent its own.
	InstructionsLoaded *instructionsLoadedState
	// Goals are the sessions' goals (see Goal), shared by a coordinator's
	// agents, which set them; nil, there are none.
	Goals *csync.Map[string, Goal]
}

func NewSessionAgent(
	opts SessionAgentOptions,
) SessionAgent {
	sessionStarts := opts.SessionStarts
	if sessionStarts == nil {
		sessionStarts = newSessionStartState()
	}
	instructionsLoaded := opts.InstructionsLoaded
	if instructionsLoaded == nil {
		instructionsLoaded = newInstructionsLoadedState()
	}
	return &sessionAgent{
		largeModel:            csync.NewValue(opts.LargeModel),
		smallModel:            csync.NewValue(opts.SmallModel),
		systemPromptPrefix:    csync.NewValue(opts.SystemPromptPrefix),
		systemPrompt:          csync.NewValue(opts.SystemPrompt),
		isSubAgent:            opts.IsSubAgent,
		agentType:             opts.AgentType,
		permissionModeOf:      opts.PermissionMode,
		sessions:              opts.Sessions,
		messages:              opts.Messages,
		cfg:                   opts.Cfg,
		disableAutoSummarize:  opts.DisableAutoSummarize,
		disableIdleCompaction: opts.DisableIdleCompaction,
		idleCompactAfter:      idleCompactionAfter,
		idleCompaction:        csync.NewMap[string, *time.Timer](),
		tools:                 csync.NewSliceFrom(opts.Tools),
		isYolo:                opts.IsYolo,
		notify:                opts.Notify,
		runComplete:           opts.RunComplete,
		messageQueue:          csync.NewMap[string, []SessionAgentCall](),
		activeRequests:        csync.NewMap[string, *activeCancel](),
		dispatchMu:            csync.NewMap[string, *sync.Mutex](),
		submitMu:              csync.NewMap[string, *sync.Mutex](),
		submitting:            csync.NewMap[string, []*activeCancel](),
		acceptedRuns:          csync.NewMap[string, int](),
		cancelMark:            csync.NewMap[string, uint64](),
		justSummarized:        csync.NewMap[string, bool](),
		compactLevels:         csync.NewMap[string, int](),
		sessionStarts:         sessionStarts,
		instructionsLoaded:    instructionsLoaded,
		goals:                 opts.Goals,
	}
}

// AcceptedRun owns exactly one accept reservation taken by
// BeginAccepted. It is the only carrier of accept-state across the
// backend.runAgent / Coordinator.Run / sessionAgent.Run layers: a
// counter > 0 means a dispatched prompt is in flight and has not yet
// completed the dispatch handoff in Run. Close is the only way to
// release the reservation and is idempotent.
type AcceptedRun struct {
	agent     *sessionAgent
	sessionID string
	// seq is the monotonic accept sequence stamped by BeginAccepted. A
	// cancel covers this handle iff seq is at or below the session's
	// cancel mark, so a handle accepted after a cancel (higher seq) is
	// never poisoned by it.
	seq  uint64
	done atomic.Bool
}

// Close decrements the accept counter for this reservation. It is safe
// to call multiple times; only the first call has effect.
func (r *AcceptedRun) Close() {
	if r == nil {
		return
	}
	if !r.done.CompareAndSwap(false, true) {
		return
	}
	r.agent.endAccepted(r.sessionID)
}

// SessionID exposes the session this reservation is for so the run path
// can use it without an extra parameter.
func (r *AcceptedRun) SessionID() string {
	if r == nil {
		return ""
	}
	return r.sessionID
}

// BeginAccepted increments the accept counter for sessionID and returns
// a handle whose Close is the only way to decrement it. It is the only
// entry point that mutates acceptedRuns.
func (a *sessionAgent) BeginAccepted(sessionID string) *AcceptedRun {
	a.acceptedMu.Lock()
	defer a.acceptedMu.Unlock()
	count, _ := a.acceptedRuns.Get(sessionID)
	a.acceptedRuns.Set(sessionID, count+1)
	a.acceptSeqGen++
	return &AcceptedRun{agent: a, sessionID: sessionID, seq: a.acceptSeqGen}
}

// endAccepted decrements the accept counter for sessionID. It is only
// called via AcceptedRun.Close. It uses a dedicated lock (not the
// per-session dispatch mutex) so it can run while Run holds dispatchMu
// for the same session without deadlocking.
//
// When the count reaches zero the session's cancel mark is dropped: no
// accepted handle remains for it to cover, and any handle accepted later
// gets a strictly higher sequence that the mark would not match anyway.
// Handles canceled on entry never reach RunComplete, so this is the only
// place that clears the mark for an all-canceled batch. Sibling handles
// covered by the same mark are serialized on the per-session dispatch
// mutex and read the mark before they Close, so this never clears it out
// from under a covered handle still waiting to enter Run.
func (a *sessionAgent) endAccepted(sessionID string) {
	a.acceptedMu.Lock()
	defer a.acceptedMu.Unlock()
	count, ok := a.acceptedRuns.Get(sessionID)
	if !ok || count <= 1 {
		a.acceptedRuns.Del(sessionID)
		a.cancelMark.Del(sessionID)
		return
	}
	a.acceptedRuns.Set(sessionID, count-1)
}

// sessionMu returns the per-session dispatch mutex, creating it on first
// use. Creation is guarded so concurrent callers always observe the same
// mutex instance for a given session.
func (a *sessionAgent) sessionMu(sessionID string) *sync.Mutex {
	if mu, ok := a.dispatchMu.Get(sessionID); ok {
		return mu
	}
	a.dispatchMuCreate.Lock()
	defer a.dispatchMuCreate.Unlock()
	if mu, ok := a.dispatchMu.Get(sessionID); ok {
		return mu
	}
	mu := &sync.Mutex{}
	a.dispatchMu.Set(sessionID, mu)
	return mu
}

// queueContinuation appends call to the session's message queue under
// the per-session dispatch mutex.
//
// The mutex is what makes the append safe: csync.Map synchronizes each
// Get and Set, but not a read-modify-write across the two, and
// enqueueCall does the same read-modify-write while holding this mutex.
// A continuation queued at the end of a turn races exactly that path —
// the session still looks busy at this point, so a prompt arriving now
// goes to enqueueCall — and an interleaving would drop one of the two
// appends, losing a prompt the user had already sent.
func (a *sessionAgent) queueContinuation(call SessionAgentCall) {
	// A continuation is the agent's own; what the user's hooks added to
	// the prompt it continues, SessionStart's included, went to the model
	// with that prompt.
	call.promptHookContext = ""
	mu := a.sessionMu(call.SessionID)
	mu.Lock()
	defer mu.Unlock()
	existing, ok := a.messageQueue.Get(call.SessionID)
	if !ok {
		existing = []SessionAgentCall{}
	}
	a.messageQueue.Set(call.SessionID, append(existing, call))
}

// enqueueCall appends call to the session's message queue.
//
// The caller must hold the session's dispatch mutex (see sessionMu).
// csync.Map synchronizes each Get and Set but not the read-modify-write
// across the two, so two unsynchronized appends drop one of the calls —
// a prompt the user already sent, lost without a trace. Run holds the
// mutex across the busy-session branch that calls this; a caller that
// does not already hold it wants queueContinuation instead.
//
// The OnComplete hook is stripped: the caller that supplied it (typically
// coordinator.Run) has its own retry/coalesce scope that ends when it
// returns, so by the time the queue drains nobody is left to consume the
// buffered terminal event. The recursive Run falls back to the default
// broker publish, which is what existing subscribers expect for queued
// turns.
func (a *sessionAgent) enqueueCall(call SessionAgentCall) {
	existing, ok := a.messageQueue.Get(call.SessionID)
	if !ok {
		existing = []SessionAgentCall{}
	}
	queued := call
	if call.Accepted != nil {
		// Preserve the accept sequence after the handle is stripped so
		// the queue-drain paths can tell a follow-up queued before a
		// cancel (covered by the mark) from one queued after it.
		queued.acceptSeq = call.Accepted.seq
	}
	queued.OnComplete = nil
	queued.Accepted = nil
	existing = append(existing, queued)
	a.messageQueue.Set(call.SessionID, existing)
}

// drainQueueForStep partitions the session's queued calls for the current
// streaming step under the per-session dispatch mutex so the filtering is
// atomic against a concurrent Cancel: canceledBySeq requires the caller to
// hold that mutex, and evaluating it here (rather than after unlocking)
// prevents a cancel recorded between the drain and the check from being
// observed inconsistently.
//
// Calls covered by a pending cancel are dropped; the dropped ones that
// carry a RunID are returned in canceledWithRunID so the caller can
// publish their terminal cancelled RunComplete (a caller waiting on that
// RunID, e.g. `crush run`, would otherwise hang). Uncanceled calls without
// a RunID are returned in fold to be folded into the active turn,
// preserving the existing follow-up behavior. Uncanceled calls that carry
// a RunID are left in the queue so each runs as its own turn via the
// recursive run path and publishes its own RunComplete, giving every
// RunID-bearing prompt an explicit lifecycle instead of being silently
// absorbed into another turn. fold is processed by the caller without the
// lock held.
func (a *sessionAgent) drainQueueForStep(sessionID string) (fold, canceledWithRunID []SessionAgentCall) {
	dispatchLock := a.sessionMu(sessionID)
	dispatchLock.Lock()
	defer dispatchLock.Unlock()
	queuedCalls, _ := a.messageQueue.Get(sessionID)
	var keep []SessionAgentCall
	for _, queued := range queuedCalls {
		if a.canceledBySeq(sessionID, queued.acceptSeq) {
			if queued.RunID != "" {
				canceledWithRunID = append(canceledWithRunID, queued)
			}
			continue
		}
		if queued.RunID != "" {
			keep = append(keep, queued)
			continue
		}
		fold = append(fold, queued)
	}
	if len(keep) == 0 {
		a.messageQueue.Del(sessionID)
	} else {
		a.messageQueue.Set(sessionID, keep)
	}
	return fold, canceledWithRunID
}

// publishCanceledQueueDrops emits a terminal cancelled RunComplete for
// every dropped queued call that carries a RunID. A queued prompt removed
// from the queue without ever running — covered by a pending cancel, or
// cleared by Cancel/ClearQueue — would otherwise leave a caller blocked on
// that RunID: `crush run` ignores live message events and exits only on a
// RunComplete whose RunID matches. Calls without a RunID had no such waiter
// and are dropped silently as before. A detached, bounded context keeps the
// must-deliver publish alive even when the run context that triggered the
// drop is already canceled.
func (a *sessionAgent) publishCanceledQueueDrops(drops []SessionAgentCall) {
	var hasRunID bool
	for _, d := range drops {
		if d.RunID != "" {
			hasRunID = true
			break
		}
	}
	if !hasRunID {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, d := range drops {
		if d.RunID == "" {
			continue
		}
		a.publishRunComplete(ctx, d, notify.RunComplete{
			SessionID: d.SessionID,
			RunID:     d.RunID,
			Cancelled: true,
		})
	}
}

// clearQueueAndNotify removes all queued prompts for the session and
// publishes a terminal cancelled RunComplete for any that carried a RunID,
// so callers waiting on those RunIDs (e.g. `crush run`) are not left
// hanging when their queued prompt is discarded without running.
func (a *sessionAgent) clearQueueAndNotify(sessionID string) {
	queued, ok := a.messageQueue.Get(sessionID)
	a.messageQueue.Del(sessionID)
	if !ok {
		return
	}
	a.publishCanceledQueueDrops(queued)
}

// clearPendingCancel removes any pending-cancel mark for sessionID. It
// takes the per-session dispatch lock so it is ordered against Cancel
// and the dispatch handoff.
func (a *sessionAgent) clearPendingCancel(sessionID string) {
	mu := a.sessionMu(sessionID)
	mu.Lock()
	defer mu.Unlock()
	a.cancelMark.Del(sessionID)
}

// canceledBySeq reports whether an accepted handle or queued call with
// the given accept sequence is covered by a pending cancel for the
// session. Callers must hold the session's dispatch mutex. A tracked
// sequence (seq > 0) is covered only when it is at or below the cancel
// high-water mark, so a prompt accepted after the cancel (higher seq) is
// never poisoned. An untracked sequence (seq == 0, an in-process enqueue
// with no accept reservation) is covered whenever any mark is present,
// preserving the pre-sequence behavior. The mark is not consumed: it
// stays so every sibling handle it covers observes the same cancel, and
// a later handle (higher seq) ignores it regardless.
func (a *sessionAgent) canceledBySeq(sessionID string, seq uint64) bool {
	mark, ok := a.cancelMark.Get(sessionID)
	if !ok || mark == 0 {
		return false
	}
	return seq == 0 || seq <= mark
}

// persistCanceledTurn writes the user/assistant records for a turn that
// was canceled before (or just as) streaming would have produced them.
// It creates the user message only when it was not already created by an
// earlier createUserMessage call (userMsgCreated), then writes an
// assistant message with FinishReasonCanceled. Both writes use
// context.WithoutCancel(ctx) so workspace shutdown (which cancels the run
// context) can't drop them.
func (a *sessionAgent) persistCanceledTurn(ctx context.Context, call SessionAgentCall, userMsgCreated bool) error {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if !userMsgCreated {
		if _, err := a.createUserMessage(writeCtx, call); err != nil {
			return err
		}
	}
	largeModel := a.largeModel.Get()
	assistant, err := a.messages.Create(writeCtx, call.SessionID, message.CreateMessageParams{
		Role:     message.Assistant,
		Parts:    []message.ContentPart{},
		Model:    largeModel.ModelCfg.Model,
		Provider: largeModel.ModelCfg.Provider,
	})
	if err != nil {
		return err
	}
	assistant.AddFinish(message.FinishReasonCanceled, "User canceled request", "")
	return a.messages.Update(writeCtx, assistant)
}

// publishRunComplete emits the authoritative terminal event for a turn.
// It honors the per-call OnComplete hook when set (so the coordinator can
// coalesce retries) and otherwise falls back to the RunComplete broker.
// ctx is used only for the bounded-blocking must-deliver publish; the
// terminal payload is supplied by the caller. This is the single emit path
// shared by the streaming defer and the cancel-on-entry early return so a
// caller waiting on RunComplete (e.g. `crush run` with a RunID) always
// observes exactly one terminal event regardless of which Run branch ends
// the turn.
func (a *sessionAgent) publishRunComplete(ctx context.Context, call SessionAgentCall, complete notify.RunComplete) {
	if call.OnComplete != nil {
		call.OnComplete(complete)
		return
	}
	if a.runComplete == nil {
		return
	}
	a.runComplete.PublishMustDeliver(ctx, pubsub.UpdatedEvent, complete)
}

// ValidateCall performs the cheap structural validation that
// sessionAgent.Run requires before a call can be dispatched: a call must
// carry either a non-empty prompt or a text attachment, and it must name a
// session. It is exported so callers that accept a run before dispatching it
// (e.g. backend.SendMessage) can apply the same checks and keep the error
// contract consistent.
func ValidateCall(call SessionAgentCall) error {
	if call.Prompt == "" && !message.ContainsTextAttachment(call.Attachments) {
		return ErrEmptyPrompt
	}
	if call.SessionID == "" {
		return ErrSessionMissing
	}
	return nil
}

func (a *sessionAgent) Run(ctx context.Context, call SessionAgentCall) (result *fantasy.AgentResult, retErr error) {
	// The model reads the prompt trimmed whenever the conversation is
	// rebuilt (ToAIMessage), so it is sent trimmed now too. Sent as typed,
	// a prompt with a trailing newline, as `klash run` makes from piped
	// stdin, differed from its rebuilt copy, and the local server's prompt
	// cache missed from that message on.
	call.Prompt = strings.TrimSpace(call.Prompt)
	if err := ValidateCall(call); err != nil {
		return nil, err
	}
	// The turn's hooks are told the session's permission mode, as Claude
	// Code tells them; a sub-agent's turn keeps its parent's.
	ctx = hooks.WithPermissionMode(ctx, func() string { return a.permissionMode(call.SessionID) })
	// The turn's bash commands run what the session's SessionStart hooks
	// wrote to its env file, as Claude Code's do; a sub-agent's run its
	// parent session's.
	ctx = tools.WithSessionEnvFile(ctx, config.SessionEnvFile(call.SessionID))

	if call.Channel != "" && call.channelMeta == nil {
		call.channelMeta, _ = parseChannelMeta(call.Prompt)
	}

	// genCtx/cancel are the run context and its cancel func, created under
	// the per-session dispatch mutex below so a concurrent Cancel can observe
	// the activeRequests entry before the assistant message exists.
	var (
		genCtx         context.Context
		cancel         context.CancelFunc
		userMsgCreated bool
	)

	// A prompt the user sent goes past their UserPromptSubmit hooks
	// first, once, whether it starts a turn or waits in the queue. The
	// submit lock, held until the dispatch lock below is taken, keeps
	// prompts sent one after another in that order while hooks run.
	// While they run the prompt counts as submitting: the session reads
	// as busy and a Cancel stops the hooks, and the prompt is then not
	// sent; see beginSubmitting.
	var (
		submitMu *sync.Mutex
		submit   *activeCancel
		hookCtx  context.Context
	)
	// The first prompt this process sends to a session goes past its
	// SessionStart hooks, then, as every prompt the user sends, past
	// UserPromptSubmit's, in that order, as in Claude Code. Both run while
	// the prompt is submitting: the session reads as busy, and a Cancel
	// stops them and the prompt is not sent (cancel-on-entry below, which
	// reports the cancelled turn). SessionStart ran after the dispatch
	// before: an Esc in its hooks ended the turn without the RunComplete
	// a waiting client blocks on, and what its hooks printed replaced what
	// UserPromptSubmit's had.
	//
	// The check below is repeated once the submit lock is held: two
	// prompts sent at once would each pass it otherwise, the second would
	// wait on the lock and then run the hooks a second time, as "clear" or
	// "compact", and its output would reach the model twice.
	sessionStart := a.wantsSessionStartHooks(call)
	promptHooks := a.wantsPromptHooks(call)
	if sessionStart || promptHooks {
		submitMu = a.sessionSubmitMu(call.SessionID)
		submitMu.Lock()
		if sessionStart {
			sessionStart = a.wantsSessionStartHooks(call)
		}
		hookCtx, submit = a.beginSubmitting(ctx, call.SessionID)
	}
	if sessionStart && a.runSessionStartHooks(hookCtx, &call) && hookCtx.Err() == nil {
		a.sessionStarts.markStarted(call.SessionID)
	}
	// Fire the InstructionsLoaded hooks for the context files in the
	// system prompt, once per session start (or again after a summary).
	// They run asynchronously: the turn does not wait for them.
	a.fireInstructionsLoadedForSession(call.SessionID)
	// What the SessionStart hooks printed for an earlier prompt that a
	// UserPromptSubmit hook refused goes out with this one, hidden or not.
	if hookCtx == nil || hookCtx.Err() == nil {
		if pending := a.sessionStarts.takePending(call.SessionID); pending != "" {
			call.promptHookContext = joinHookContext(pending, call.promptHookContext)
			call.carriesSessionStart = true
		}
	}
	if promptHooks && hookCtx.Err() == nil {
		if err := a.runPromptHooks(hookCtx, &call); err != nil {
			// Refused before it reached the model: what SessionStart
			// printed for it waits for the next prompt to the session,
			// since the session counts as started and the hooks do not
			// run again.
			if call.carriesSessionStart {
				a.sessionStarts.setPending(call.SessionID, call.promptHookContext)
			}
			a.endSubmitting(call.SessionID, submit, true)
			submit.cancel()
			submitMu.Unlock()
			call.Accepted.Close()
			return nil, err
		}
	}
	// Sent, with or without hooks: a queued copy of this call, or a
	// continuation made from it, does not run them again, even if some
	// are configured by the time it runs.
	call.promptHooksRan = true

	// Serialize the dispatch decision (cancel-on-entry | queued | active)
	// against a concurrent Cancel. Cancel takes the same per-session lock, so
	// every cancel observes at least one of: a cancel mark, an activeRequests
	// entry, or a messageQueue entry it then clears. Holding the lock across
	// the busy check and the active registration also makes them atomic, so
	// two concurrent in-process callers — a burst of channel events, or a
	// channel event racing a typed prompt — cannot both pass the busy check
	// and start two runs on the same session.
	sessMu := a.sessionMu(call.SessionID)
	sessMu.Lock()
	// A Cancel while the hooks ran ended hookCtx. The prompt stops being
	// submitting only now, under the dispatch lock, so a Cancel after the
	// hooks finished either saw it submitting or sees it queued or active.
	cancelledInHooks := false
	if submit != nil {
		a.endSubmitting(call.SessionID, submit, false)
		submitMu.Unlock()
		cancelledInHooks = hookCtx.Err() != nil
		submit.cancel()
	}

	if cancelledInHooks || (call.Accepted != nil && a.canceledBySeq(call.SessionID, call.Accepted.seq)) {
		// Cancel-on-entry: a cancel arrived while this accepted run was
		// dispatched but not yet active, and this handle's accept sequence
		// is at or below the session's cancel mark. The mark is left in
		// place so sibling handles it also covers observe the same cancel;
		// release the accept reservation, drop the lock, and persist a
		// canceled turn without entering Stream.
		//
		// This path returns before the streaming defer that publishes
		// RunComplete is installed, so emit the terminal event explicitly.
		// Without it, a caller waiting on RunComplete for this RunID (e.g.
		// `crush run`, which ignores message events and blocks on
		// RunComplete) would hang on an immediately-canceled accepted run.
		call.Accepted.Close()
		sessMu.Unlock()
		complete := notify.RunComplete{
			SessionID: call.SessionID,
			RunID:     call.RunID,
			Cancelled: true,
		}
		if err := a.persistCanceledTurn(ctx, call, false); err != nil {
			complete.Error = err.Error()
			a.publishRunComplete(ctx, call, complete)
			return nil, err
		}
		a.publishRunComplete(ctx, call, complete)
		return nil, nil
	}

	if a.hasActiveRun(call.SessionID) {
		// Busy: an earlier prompt is active. Queue this call so it is
		// folded into (or sequenced after) the active turn, and release any
		// accept reservation. A Cancel arriving after this point sees the
		// active entry and clears the queue.
		//
		// enqueueCall strips OnComplete: the caller that supplied the hook
		// (typically coordinator.Run) has its own retry/coalesce scope that
		// ends when it returns, so by the time the queue drains nobody is
		// left to consume the buffered terminal event. The queued turn falls
		// back to the default broker publish, which is what existing
		// subscribers expect.
		a.enqueueCall(call)
		if call.Accepted != nil {
			call.Accepted.Close()
		}
		sessMu.Unlock()
		return nil, nil
	}

	// Idle: become the active run. Register the cancel func before dropping
	// the lock so a Cancel that arrives between here and assistant creation
	// is not lost.
	runCtx := context.WithValue(ctx, tools.SessionIDContextKey, call.SessionID)
	genCtx, cancel = context.WithCancel(runCtx)
	ac := &activeCancel{cancel: cancel}
	a.activeRequests.Set(call.SessionID, ac)
	if call.Accepted != nil {
		call.Accepted.Close()
	}
	sessMu.Unlock()

	defer cancel()
	// Conditional cleanup: only remove our entry if it hasn't been replaced
	// by a newer run. Without this guard, the deferred Del fires after a
	// concurrent run registers in the completion window, silently wiping
	// the new run's cancel and breaking cancellation.
	defer a.activeRequests.CompareAndDelete(call.SessionID, ac)

	// skipRunComplete is set just before the queued-recursion path so
	// the outer Run doesn't publish a RunComplete that would race
	// with — and be superseded by — the recursive call's own
	// RunComplete (each queued user prompt is its own turn and
	// publishes exactly one terminal event).
	var skipRunComplete bool
	// currentAssistant is declared here so the deferred RunComplete
	// publish below can capture the pointer that PrepareStep will
	// later (re)assign for each streaming step. The final assistant
	// message of the turn is the value reachable through this
	// pointer when the defer runs.
	var currentAssistant *message.Message
	// haltedByTool records that a tool result ended the turn (a denied
	// permission, a cancelled question, a hook's halt), for the terminal
	// RunComplete's Halted.
	var haltedByTool bool
	// Drain any debounced message updates before returning. message.Service
	// already flushes synchronously on terminal updates, but a defer here
	// guarantees the contract at every Run exit (success, error, panic
	// recovery upstream) without callers needing to know.
	//
	// After the flush completes — meaning all per-message
	// Publish(UpdatedEvent) calls have fired and been buffered into
	// every subscriber's channel — publish the authoritative
	// RunComplete event for this turn. The flush-then-publish order
	// gives well-behaved clients the best chance of seeing the final
	// message event before RunComplete; the embedded Text field
	// reconciles for clients that observe the events out of order
	// (the pubsub broker fan-in does not serialize publishes from
	// different upstream brokers).
	defer func() {
		// Use a context detached from the run context: workspace
		// shutdown cancels ctx before this goroutine returns, but the
		// buffered streaming deltas must still land before the DB is
		// closed. A short timeout bounds the flush.
		flushCtx, flushCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer flushCancel()
		if flushErr := a.messages.FlushAll(flushCtx); flushErr != nil {
			slog.Error("Failed to flush pending message updates after run", "error", flushErr)
		}
		if skipRunComplete {
			return
		}
		complete := notify.RunComplete{SessionID: call.SessionID, RunID: call.RunID, Halted: haltedByTool}
		if currentAssistant != nil {
			complete.MessageID = currentAssistant.ID
			complete.Text = currentAssistant.Content().String()
		}
		if retErr != nil {
			complete.Error = retErr.Error()
			complete.Cancelled = errors.Is(retErr, context.Canceled)
		} else if ctx.Err() != nil {
			complete.Cancelled = true
		}
		// Prefer the per-call hook when supplied so the coordinator
		// can coalesce retries (e.g. unauthorized → re-auth → retry)
		// into a single user-visible terminal event. The fallback
		// must-deliver publish applies bounded-blocking semantics to
		// the authoritative terminal event so a momentarily-full
		// subscriber channel can't silently drop it and hang
		// non-interactive clients waiting on RunComplete.
		a.publishRunComplete(ctx, call, complete)
	}()

	// Copy mutable fields under lock to avoid races with SetTools/SetModels.
	agentTools := a.requestTools(ctx, call.Channel)
	largeModel := a.largeModel.Get()
	systemPrompt := a.requestSystemPrompt()
	promptPrefix := a.systemPromptPrefix.Get()

	agent := fantasy.NewAgent(
		largeModel.Model,
		fantasy.WithSystemPrompt(systemPrompt),
		fantasy.WithTools(agentTools...),
		fantasy.WithUserAgent(userAgent),
	)

	sessionLock := sync.Mutex{}
	currentSession, err := a.sessions.Get(ctx, call.SessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to get session: %w", err)
	}

	msgs, err := a.getSessionMessages(ctx, currentSession)
	if err != nil {
		return nil, fmt.Errorf("failed to get session messages: %w", err)
	}

	// Repair ghosts first: an assistant message left without a Finish part
	// by a crash or hard kill would otherwise render as a second, endless
	// "working" spinner next to this turn's. No other run can be active for
	// the session here, so every unfinished reply is an orphan.
	if finalizeOrphanedAssistants(ctx, a.messages, call.SessionID) > 0 {
		if msgs, err = a.getSessionMessages(ctx, currentSession); err != nil {
			return nil, fmt.Errorf("failed to reload session messages: %w", err)
		}
	}

	// Generate title from the first real (non-shell) user prompt.
	// can take tens of seconds. Blocking Run on it delays the
	// response to the caller. Use a detached context so the title
	// goroutine survives Run's cancel.
	//
	// A sub-agent's session gets none: nothing lists or shows it, and the
	// request would run beside the sub-agent's first one. On a local
	// server with few slots that took the slot holding the parent's
	// prompt cache, and the parent then spent minutes re-reading its
	// whole conversation when it resumed.
	if !hasUserTextMessage(msgs) && currentSession.ParentSessionID == "" {
		titleCtx := context.WithoutCancel(ctx)
		go a.GenerateTitle(titleCtx, call.SessionID, call.Prompt)
	}

	// Add the user message to the session.
	_, err = a.createUserMessage(ctx, call)
	if err != nil {
		return nil, err
	}
	userMsgCreated = true

	// Add the session to the context. The run context (genCtx) and its
	// cancel func were already created and registered under the dispatch
	// mutex above for both the accepted and in-process paths.
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, call.SessionID)

	history, files := a.preparePrompt(msgs, largeModel.CatwalkCfg.SupportsImages, call.Attachments...)

	startTime := time.Now()
	a.eventPromptSent(call.SessionID)

	var stepMessages []fantasy.Message
	var shouldSummarize bool
	// The tool calls the agent was caught repeating, when that is what
	// stopped the run; see loopContinuation.
	var loopCalls []fantasy.ToolCallContent
	sanitizedToolCalls := make(map[string]bool)
	// Full names of tool calls that completed without error this turn.
	// Written only from the streaming callbacks (which run sequentially)
	// and read after Stream returns, where sendChannelReply uses it to
	// tell whether the model already replied on the originating channel.
	completedToolCalls := make(map[string]struct{})
	// Calls that got a reply this turn; the rest are settled when it ends.
	answeredToolCalls := csync.NewMap[string, struct{}]()
	// Don't send MaxOutputTokens if 0 — some providers (e.g. LM Studio) reject it
	var maxOutputTokens *int64
	if call.MaxOutputTokens > 0 {
		maxOutputTokens = &call.MaxOutputTokens
	}
	result, err = agent.Stream(genCtx, fantasy.AgentStreamCall{
		Prompt:           message.AppendPromptHookContext(message.PromptWithTextAttachments(call.Prompt, call.Attachments), promptHookBlock(call)),
		Files:            files,
		Messages:         history,
		Headers:          sessionHeaders(call.SessionID),
		ProviderOptions:  call.ProviderOptions,
		MaxOutputTokens:  maxOutputTokens,
		TopP:             call.TopP,
		Temperature:      call.Temperature,
		PresencePenalty:  call.PresencePenalty,
		TopK:             call.TopK,
		FrequencyPenalty: call.FrequencyPenalty,
		PrepareStep: func(callContext context.Context, options fantasy.PrepareStepFunctionOptions) (_ context.Context, prepared fantasy.PrepareStepResult, err error) {
			prepared.Messages = options.Messages
			for i := range prepared.Messages {
				prepared.Messages[i].ProviderOptions = nil
			}

			// Use latest tools (updated by SetTools when MCP tools
			// change).
			prepared.Tools = a.requestTools(callContext, call.Channel)

			// Drain queued follow-up prompts for this step. Calls covered
			// by a cancel recorded while they sat in the queue are dropped:
			// a cancel that arrived after a prompt was queued must not let
			// it run as part of this step. Coverage is per-call by accept
			// sequence so a follow-up queued after the cancel (higher seq)
			// is not dropped. A dropped prompt carrying a RunID still gets
			// its terminal cancelled RunComplete so a caller waiting on it
			// does not hang. Uncanceled prompts without a RunID are folded
			// into this turn; uncanceled prompts with a RunID are left
			// queued so each runs as its own turn (with its own
			// RunComplete) via the recursive run path below.
			fold, canceledRunIDs := a.drainQueueForStep(call.SessionID)
			a.publishCanceledQueueDrops(canceledRunIDs)
			for _, queued := range fold {
				userMessage, createErr := a.createUserMessage(callContext, queued)
				if createErr != nil {
					return callContext, prepared, createErr
				}
				prepared.Messages = append(prepared.Messages, userMessage.ToAIMessage()...)
			}

			prepared.Messages = a.prepareRequestMessages(prepared.Messages, largeModel, promptPrefix)

			sessionLock.Lock()
			stepMessages = cloneFantasyMessages(prepared.Messages)
			sessionLock.Unlock()

			var assistantMsg message.Message
			assistantMsg, err = a.messages.Create(callContext, call.SessionID, message.CreateMessageParams{
				Role:     message.Assistant,
				Parts:    []message.ContentPart{},
				Model:    largeModel.ModelCfg.Model,
				Provider: largeModel.ModelCfg.Provider,
			})
			if err != nil {
				return callContext, prepared, err
			}
			callContext = context.WithValue(callContext, tools.MessageIDContextKey, assistantMsg.ID)
			callContext = context.WithValue(callContext, tools.ChannelContextKey, call.Channel)
			callContext = context.WithValue(callContext, tools.SupportsImagesContextKey, largeModel.CatwalkCfg.SupportsImages)
			callContext = context.WithValue(callContext, tools.ModelNameContextKey, largeModel.CatwalkCfg.Name)
			currentAssistant = &assistantMsg
			return callContext, prepared, err
		},
		OnReasoningStart: func(id string, reasoning fantasy.ReasoningContent) error {
			currentAssistant.AppendReasoningContent(reasoning.Text)
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnReasoningDelta: func(id string, text string) error {
			currentAssistant.AppendReasoningContent(text)
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnReasoningEnd: func(id string, reasoning fantasy.ReasoningContent) error {
			// handle anthropic signature
			if anthropicData, ok := reasoning.ProviderMetadata[anthropic.Name]; ok {
				if reasoning, ok := anthropicData.(*anthropic.ReasoningOptionMetadata); ok {
					currentAssistant.AppendReasoningSignature(reasoning.Signature)
				}
			}
			if googleData, ok := reasoning.ProviderMetadata[google.Name]; ok {
				if reasoning, ok := googleData.(*google.ReasoningMetadata); ok {
					currentAssistant.AppendThoughtSignature(reasoning.Signature, reasoning.ToolID)
				}
			}
			if openaiData, ok := reasoning.ProviderMetadata[openai.Name]; ok {
				if reasoning, ok := openaiData.(*openai.ResponsesReasoningMetadata); ok {
					currentAssistant.SetReasoningResponsesData(reasoning)
				}
			}
			currentAssistant.FinishThinking()
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnTextDelta: func(id string, text string) error {
			// Strip leading newline from initial text content. This is is
			// particularly important in non-interactive mode where leading
			// newlines are very visible.
			if len(currentAssistant.Parts) == 0 {
				text = strings.TrimPrefix(text, "\n")
			}

			currentAssistant.AppendContent(text)
			return a.messages.Update(genCtx, *currentAssistant)
		},
		OnToolInputStart: func(id string, toolName string) error {
			toolCall := message.ToolCall{
				ID:               id,
				Name:             toolName,
				ProviderExecuted: false,
				Finished:         false,
			}
			currentAssistant.AddToolCall(toolCall)
			// Use parent ctx instead of genCtx to ensure the update succeeds
			// even if the request is canceled mid-stream
			return a.messages.Update(ctx, *currentAssistant)
		},
		OnRetry: func(err *fantasy.ProviderError, delay time.Duration) {
			slog.Warn("Provider request failed, retrying", providerRetryLogFields(err, delay)...)
			// Reset streamed content so the retried response doesn't
			// concatenate with partial content from the failed attempt.
			// On the final attempt (no more retries), any partial content
			// stays in the message as useful context beneath the error.
			currentAssistant.ResetStreamedContent()
			if updateErr := a.messages.Update(genCtx, *currentAssistant); updateErr != nil {
				slog.Error("Failed to reset message on retry", "error", updateErr)
			}
		},
		OnAuthRefresh: call.OnAuthRefresh,
		ModelProvider: func() fantasy.LanguageModel {
			m := a.largeModel.Get()
			slog.Info("ModelProvider called",
				"provider", m.ModelCfg.Provider,
				"model", m.ModelCfg.Model)
			return m.Model
		},
		OnToolCall: func(tc fantasy.ToolCallContent) error {
			input, wasSanitized := sanitizeToolInput(tc.ToolName, tc.ToolCallID, tc.Input)
			if wasSanitized {
				sanitizedToolCalls[tc.ToolCallID] = true
			}
			toolCall := message.ToolCall{
				ID:               tc.ToolCallID,
				Name:             tc.ToolName,
				Input:            input,
				ProviderExecuted: false,
				Finished:         true,
			}
			currentAssistant.AddToolCall(toolCall)
			// Use parent ctx instead of genCtx to ensure the update succeeds
			// even if the request is canceled mid-stream
			return a.messages.Update(ctx, *currentAssistant)
		},
		OnToolResult: func(result fantasy.ToolResultContent) error {
			toolResult := a.convertToToolResult(result)
			if sanitizedToolCalls[result.ToolCallID] {
				toolResult.Content = "Tool call failed: arguments were not valid JSON. Please check your tool call format and try again."
				toolResult.IsError = true
			}
			if !toolResult.IsError && toolResult.Name != "" {
				completedToolCalls[toolResult.Name] = struct{}{}
			}
			// Use parent ctx instead of genCtx to ensure the message is created
			// even if the request is canceled mid-stream
			_, createMsgErr := a.messages.Create(ctx, currentAssistant.SessionID, message.CreateMessageParams{
				Role: message.Tool,
				Parts: []message.ContentPart{
					toolResult,
				},
			})
			if createMsgErr == nil {
				answeredToolCalls.Set(result.ToolCallID, struct{}{})
			}
			return createMsgErr
		},
		OnStepFinish: func(stepResult fantasy.StepResult) error {
			for _, w := range stepResult.Warnings {
				slog.Warn("Provider warning", "type", w.Type, "message", w.Message)
			}
			finishReason := message.FinishReasonUnknown
			switch stepResult.FinishReason {
			case fantasy.FinishReasonLength:
				finishReason = message.FinishReasonMaxTokens
			case fantasy.FinishReasonStop:
				finishReason = message.FinishReasonEndTurn
			case fantasy.FinishReasonToolCalls:
				finishReason = message.FinishReasonToolUse
			case fantasy.FinishReasonContentFilter:
				// Provider safety classifier stopped the model
				// (Anthropic stop_reason=refusal, OpenAI content_filter).
				// The TUI owns the display copy; we only persist the
				// reason so the UI can show a REFUSED banner.
				finishReason = message.FinishReasonContentFilter
				slog.Warn(
					"Provider content filter stopped the model",
					"session_id", call.SessionID,
					"finish_reason", string(stepResult.FinishReason),
				)
			}
			// If a tool result halted the turn (e.g. a hook halt or a
			// permission denial), the step ends on FinishReasonToolCalls but
			// the model will not be called again. Treat it as the end of the
			// turn so the UI can render the assistant footer.
			if finishReason == message.FinishReasonToolUse {
				for _, tr := range stepResult.Content.ToolResults() {
					if tr.StopTurn {
						finishReason = message.FinishReasonEndTurn
						haltedByTool = true
						break
					}
				}
			}
			currentAssistant.AddFinish(finishReason, "", "")
			currentAssistant.PrismModelID, currentAssistant.PrismModelName = extractPrismModel(stepResult.ProviderMetadata)
			currentAssistant.PrismHypercreditSavings, currentAssistant.PrismDollarSavings = extractPrismSavings(stepResult.ProviderMetadata)
			sessionLock.Lock()
			defer sessionLock.Unlock()

			updatedSession, getSessionErr := a.sessions.Get(ctx, call.SessionID)
			if getSessionErr != nil {
				return getSessionErr
			}
			usage, estimated := fallbackStepUsage(stepMessages, stepResult)
			costBefore := updatedSession.Cost
			a.updateSessionUsage(largeModel, &updatedSession, usage, a.openrouterCost(stepResult.ProviderMetadata), estimated)
			// Only the usage is written: the title model's title, or a
			// /rename, written since the Get is kept.
			updatedSession, sessionErr := a.sessions.UpdateUsage(ctx, call.SessionID, updatedSession.PromptTokens, updatedSession.CompletionTokens, updatedSession.Cost-costBefore, updatedSession.EstimatedUsage)
			if sessionErr != nil {
				return sessionErr
			}
			currentSession = updatedSession
			return a.messages.Update(genCtx, *currentAssistant)
		},
		StopWhen: []fantasy.StopCondition{
			func(_ []fantasy.StepResult) bool {
				tokens := currentSession.CompletionTokens + currentSession.PromptTokens
				if a.compacts(call.SessionID, int64(largeModel.CatwalkCfg.ContextWindow), tokens) {
					shouldSummarize = true
					return true
				}
				return false
			},
			loopStopCondition(func(repeated []fantasy.ToolCallContent) {
				loopCalls = repeated
			}),
		},
	})

	a.eventPromptResponded(call.SessionID, time.Since(startTime).Truncate(time.Second))

	// Final writes use a context detached from the run: workspace shutdown
	// cancels ctx before agent goroutines finish, and the turn's ending
	// must still land.
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cleanupCancel()
	if currentAssistant != nil {
		answered := func(id string) bool {
			_, ok := answeredToolCalls.Get(id)
			return ok
		}
		if settleErr := a.settleTurn(cleanupCtx, currentAssistant, answered, err); settleErr != nil {
			slog.Error("Failed to settle unanswered tool calls",
				"session_id", call.SessionID, "error", settleErr)
		}
	}

	if err != nil {
		isHyper := largeModel.ModelCfg.Provider == hyper.Name
		isCancelErr := errors.Is(err, context.Canceled)
		slog.Info("Agent stream returned error",
			"error", err.Error(),
			"error_type", fmt.Sprintf("%T", err),
			"is_hyper", isHyper,
			"is_cancel", isCancelErr)
		if currentAssistant == nil {
			// Cancel-before-assistant-creation window: the run was
			// canceled after activeRequests.Set but before PrepareStep
			// created the assistant message. Without this, the turn
			// would return with no FinishReasonCanceled marker and no
			// user-visible record. The user message was already created
			// above, so persistCanceledTurn only writes the assistant
			// record.
			if isCancelErr {
				if persistErr := a.persistCanceledTurn(ctx, call, userMsgCreated); persistErr != nil {
					return nil, persistErr
				}
			}
			return result, err
		}
		// Ensure we finish thinking on error to close the reasoning state.
		currentAssistant.FinishThinking()
		var fantasyErr *fantasy.Error
		var providerErr *fantasy.ProviderError
		var requestTimedOutErr *requestTimeoutError
		const defaultTitle = "Provider Error"
		linkStyle := lipgloss.NewStyle().Foreground(charmtone.Guac).Underline(true)
		if isCancelErr {
			currentAssistant.AddFinish(message.FinishReasonCanceled, "User canceled request", "")
		} else if errors.As(err, &requestTimedOutErr) {
			// Checked before the provider branches so a deadline our own
			// request timeout imposed is never reported as a provider error.
			currentAssistant.AddFinish(message.FinishReasonError, "Request timed out", requestTimedOutErr.userMessage())
		} else if isHyper && errors.As(err, &providerErr) && providerErr.StatusCode == http.StatusUnauthorized {
			currentAssistant.AddFinish(message.FinishReasonError, "Unauthorized", `Please re-authenticate with Hyper. You can also run "`+version.Command+` login hyper" to re-authenticate.`)
		} else if errors.As(err, &providerErr) {
			if providerErr.Message == "The requested model is not supported." {
				url := "https://github.com/settings/copilot/features"
				link := linkStyle.Hyperlink(url, "id=copilot").Render(url)
				currentAssistant.AddFinish(
					message.FinishReasonError,
					"Copilot model not enabled",
					fmt.Sprintf("%q is not enabled in Copilot. Go to the following page to enable it. Then, wait 5 minutes before trying again. %s", largeModel.CatwalkCfg.Name, link),
				)
			} else {
				currentAssistant.AddFinish(message.FinishReasonError, cmp.Or(stringext.Capitalize(providerErr.Title), defaultTitle), providerErr.Message)
			}
		} else if errors.As(err, &fantasyErr) {
			currentAssistant.AddFinish(message.FinishReasonError, cmp.Or(stringext.Capitalize(fantasyErr.Title), defaultTitle), fantasyErr.Message)
		} else if fantasy.IsTransportError(err) {
			wrapped := fantasy.NewTransportError(err)
			currentAssistant.AddFinish(message.FinishReasonError, stringext.Capitalize(wrapped.Title), wrapped.Message)
		} else {
			currentAssistant.AddFinish(message.FinishReasonError, defaultTitle, err.Error())
		}
		updateErr := a.messages.Update(cleanupCtx, *currentAssistant)
		if updateErr != nil {
			return nil, updateErr
		}
		// A turn that ended on an error from the model's API runs the
		// StopFailure hooks in place of the Stop hooks, as in Claude Code.
		if !isCancelErr {
			a.runStopFailureHooks(context.WithoutCancel(ctx), call.SessionID, err, shownError(currentAssistant))
		}
		// A channel-originated turn has no caller watching the error, so
		// tell the channel side something went wrong instead of leaving
		// the sender hanging. sendChannelReply detaches from the run
		// context so the notice survives a provider error that tore it down.
		if channelErrorReplyWanted(call.Channel, err) {
			a.sendChannelReply(ctx, call,
				"Something went wrong while handling your message. Please try again.",
				completedToolCalls)
		}
		return nil, err
	}

	if shouldSummarize {
		a.activeRequests.Del(call.SessionID)
		if summarizeErr := a.summarize(genCtx, call.SessionID, call.Channel, "", call.ProviderOptions, call.OnAuthRefresh, preCompactTriggerAuto); summarizeErr != nil {
			return nil, summarizeErr
		}
		a.justSummarized.Set(call.SessionID, true)
		// If the agent wasn't done...
		if len(currentAssistant.ToolCalls()) > 0 {
			call.Prompt = fmt.Sprintf("The previous session was interrupted because it got too long, the initial user request was: `%s`", call.Prompt)
			a.queueContinuation(call)
		}
	}

	// A turn stopped because the agent kept repeating itself is not over:
	// it re-queues this same call (same RunID), telling the agent what it
	// was doing, so the queue handoff below puts it back to work. Stop
	// hooks get the last word on whether a turn that did finish is really
	// over; one that denies re-queues the call the same way, with the
	// hook's reason as the prompt. Neither applies when summarization
	// already queued a continuation of its own.
	continued := false
	// A turn the user or a hook halted (a denied permission, a cancelled
	// question, a hook's halt) is over: that is the last word, as in
	// Claude Code, where a Stop hook does not run when the user stops the
	// agent. Asking the Stop hooks could send the agent straight back to
	// what was just refused.
	if !shouldSummarize && currentAssistant != nil && !haltedByTool {
		if loopCalls != nil {
			slog.Warn("Agent repeated the same tool calls; telling it to change approach",
				"session_id", call.SessionID,
				"tool", loopCalls[0].ToolName,
			)
			a.queueContinuation(loopContinuation(call, loopCalls))
			continued = true
		} else if next, unmet := a.goalContinuation(ctx, call, currentAssistant.Content().String()); unmet {
			a.queueContinuation(next)
			continued = true
		} else if next, blocked := a.stopHookContinuation(ctx, call, currentAssistant.Content().String()); blocked {
			a.queueContinuation(next)
			continued = true
		}
	}

	// Route the finished turn's response back to the channel it came
	// from, unless the turn was cut short for summarization with work
	// still pending — the queued continuation carries the channel and
	// replies when it actually finishes. A turn continued for repeating
	// itself, or that a Stop hook refused to end, is likewise not
	// finished, so its continuation replies instead. Runs before the busy
	// state is released so a follow-up push queued behind this turn
	// cannot overtake its reply.
	if currentAssistant != nil && !continued && (!shouldSummarize || len(currentAssistant.ToolCalls()) == 0) {
		a.sendChannelReply(ctx, call, currentAssistant.Content().String(), completedToolCalls)
	}

	// Release active request before publishing the notification.
	// TUI handlers poll IsSessionBusy() and only re-evaluate when a
	// tea.Msg arrives, so the cleanup must precede the notify or
	// subscribers see stale busy state at the moment of receipt.
	a.activeRequests.Del(call.SessionID)
	cancel()

	// Send notification that agent has finished its turn (skip for
	// nested/non-interactive sessions).
	if !call.NonInteractive && !continued && a.notify != nil {
		a.notify.Publish(pubsub.CreatedEvent, notify.Notification{
			SessionID:    call.SessionID,
			SessionTitle: currentSession.Title,
			Type:         notify.TypeAgentFinished,
		})
	}
	if !call.NonInteractive && !continued {
		a.armIdleCompaction(call.SessionID, call.ProviderOptions)
	}

	// Hand off to the next queued prompt (if any) under dispatchMu so
	// the transition from this finished run to the queued run is atomic
	// against a concurrent Cancel. activeRequests for this session was
	// just deleted above, so without the lock there is a window in
	// which the session looks idle and a cancel becomes a no-op that
	// fails to stop the queued prompt. Holding the lock lets us observe
	// a pending cancel recorded against the session and drop the queue
	// instead of running it, and (for the recursion) hand a fresh
	// accept reservation to the dequeued call so acceptedRuns stays > 0
	// across the recursive Run's own dispatch handoff — keeping the
	// session observable to Cancel for the entire transition and
	// closing the dequeue -> re-register window.
	mu := a.sessionMu(call.SessionID)
	mu.Lock()
	queuedMessages, _ := a.messageQueue.Get(call.SessionID)
	if mark, ok := a.cancelMark.Get(call.SessionID); ok && mark > 0 && len(queuedMessages) > 0 {
		// A cancel was recorded for this session (e.g. it arrived while
		// this run was active and follow-ups had been queued). Drop the
		// queued prompts it covers (accept sequence at or below the
		// mark, or untracked); keep any queued after the cancel (higher
		// sequence) so they still run.
		var kept []SessionAgentCall
		var canceledRunIDDrops []SessionAgentCall
		for _, q := range queuedMessages {
			if q.acceptSeq == 0 || q.acceptSeq <= mark {
				if q.RunID != "" {
					canceledRunIDDrops = append(canceledRunIDDrops, q)
				}
				continue
			}
			kept = append(kept, q)
		}
		queuedMessages = kept
		a.messageQueue.Set(call.SessionID, kept)
		// A dropped prompt carrying a RunID must still publish its
		// terminal cancelled RunComplete so a caller waiting on that
		// RunID does not hang.
		a.publishCanceledQueueDrops(canceledRunIDDrops)
	}
	if len(queuedMessages) == 0 {
		// No queued work. Clear the cancel mark only when no accepted
		// run remains in flight that it might still cover; otherwise a
		// sibling prompt (sequence at or below the mark) waiting to
		// enter Run would lose its cancellation. When accepted runs are
		// gone, this also clears a stale mark so it can't catch a
		// future run.
		a.messageQueue.Del(call.SessionID)
		a.acceptedMu.Lock()
		inFlight, _ := a.acceptedRuns.Get(call.SessionID)
		a.acceptedMu.Unlock()
		if inFlight == 0 {
			a.cancelMark.Del(call.SessionID)
		}
		mu.Unlock()
		return result, err
	}
	// There are queued messages, restart the loop. Suppress the outer
	// defer's emit: it would otherwise observe the recursive Run's retErr
	// (named-return clobbering through the return below) against this
	// turn's MessageID/Text and publish a mixed, racing event.
	skipRunComplete = true
	// Decide whether this turn still owes its own terminal RunComplete.
	// Each submitted prompt with a RunID has its own lifecycle, so a turn
	// that is finished and handing off to a *different* queued prompt must
	// publish its own RunComplete here — leaving it to the recursive turn
	// (which carries a different RunID) would hang a caller waiting on
	// this turn's RunID. The exception is the summarize-continuation path,
	// which re-queues this same call (same RunID) to resume after a
	// summary; in that case the eventual terminal turn for this RunID
	// publishes, so publishing now would double-emit.
	outerOwesRunComplete := call.RunID != ""
	if outerOwesRunComplete {
		for _, q := range queuedMessages {
			if q.RunID == call.RunID {
				outerOwesRunComplete = false
				break
			}
		}
	}
	firstQueuedMessage := queuedMessages[0]
	a.messageQueue.Set(call.SessionID, queuedMessages[1:])
	// Reserve a fresh accept for the dequeued prompt before dropping the
	// lock so acceptedRuns > 0 across the handoff into the recursive
	// Run. This closes the window between this dequeue and the recursive
	// Run registering its activeRequests entry: a cancel arriving in
	// that window now records a pending cancel (acceptedRuns > 0) that
	// the recursive Run's accepted path observes as cancel-on-entry.
	firstQueuedMessage.Accepted = a.BeginAccepted(call.SessionID)
	mu.Unlock()
	if outerOwesRunComplete {
		complete := notify.RunComplete{SessionID: call.SessionID, RunID: call.RunID, Halted: haltedByTool}
		if currentAssistant != nil {
			complete.MessageID = currentAssistant.ID
			complete.Text = currentAssistant.Content().String()
		}
		if ctx.Err() != nil {
			complete.Cancelled = true
		}
		a.publishRunComplete(ctx, call, complete)
	}
	return a.Run(ctx, firstQueuedMessage)
}

func (a *sessionAgent) Summarize(ctx context.Context, sessionID, instructions string, opts fantasy.ProviderOptions, onAuthRefresh func(context.Context, *fantasy.ProviderError) error, trigger preCompactTrigger) error {
	// A summary asked for by hand starts the session's auto-compaction
	// afresh (see compacts).
	a.compactLevels.Del(sessionID)
	return a.summarize(ctx, sessionID, "", instructions, opts, onAuthRefresh, trigger)
}

// summarize replaces the session's conversation with a summary of it.
// The request asking for it is the session's next turn as the provider
// sees it: the same system prompt and tools, scoped to the channel of
// the turn being cut short ("" when the user asked), the conversation
// prepared as a turn prepares it, and the summary instructions as the
// last user message. So the provider's prompt cache covers the whole
// conversation. It was sent with a system prompt of its own and no
// tools: on a local server that differed from the cached conversation
// within its first tokens, and a full session waited twenty minutes
// while the server read all of it again. instructions, the text given
// after /compact, go at the end of the summary instructions.
func (a *sessionAgent) summarize(ctx context.Context, sessionID, channel, instructions string, opts fantasy.ProviderOptions, onAuthRefresh func(context.Context, *fantasy.ProviderError) error, trigger preCompactTrigger) error {
	// A turn running in the session stops a summary. A prompt that is
	// still in its UserPromptSubmit hooks does not: IsSessionBusy counts
	// it, and the summary at the end of a turn failed as busy when the
	// user had just sent another prompt. That prompt queues behind the
	// summary as it would behind the turn.
	if a.hasActiveRun(sessionID) {
		return ErrSessionBusy
	}

	// The user's PreCompact hooks get the first word, just before the
	// conversation is summarized. They cannot stop it: whatever they
	// say, the summary goes ahead.
	a.runPreCompactHooks(ctx, sessionID, trigger, instructions)

	// Copy mutable fields under lock to avoid races with SetModels.
	largeModel := a.largeModel.Get()
	systemPromptPrefix := a.systemPromptPrefix.Get()

	currentSession, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}
	msgs, err := a.getSessionMessages(ctx, currentSession)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		// Nothing to summarize.
		return nil
	}

	aiMsgs, _ := a.preparePrompt(msgs, largeModel.CatwalkCfg.SupportsImages)

	genCtx, cancel := context.WithCancel(ctx)
	ac := &activeCancel{cancel: cancel}
	a.activeRequests.Set(sessionID, ac)
	defer a.activeRequests.CompareAndDelete(sessionID, ac)
	defer cancel()
	defer func() {
		if flushErr := a.messages.FlushAll(ctx); flushErr != nil {
			slog.Error("Failed to flush pending message updates after summarize", "error", flushErr)
		}
	}()

	summaryMessage, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:             message.Assistant,
		Model:            largeModel.ModelCfg.Model,
		Provider:         largeModel.ModelCfg.Provider,
		IsSummaryMessage: true,
	})
	if err != nil {
		return err
	}

	noTools := fantasy.ToolChoiceNone
	ask := func(systemPrompt string, tools []fantasy.AgentTool) (*fantasy.AgentResult, error) {
		agent := fantasy.NewAgent(
			largeModel.Model,
			fantasy.WithSystemPrompt(systemPrompt),
			fantasy.WithTools(tools...),
			fantasy.WithUserAgent(userAgent),
		)
		return agent.Stream(genCtx, fantasy.AgentStreamCall{
			Prompt:          buildSummaryPrompt(currentSession.Todos, instructions),
			Messages:        aiMsgs,
			Headers:         sessionHeaders(sessionID),
			ProviderOptions: opts,
			OnAuthRefresh:   onAuthRefresh,
			ToolChoice:      &noTools,
			StopWhen:        []fantasy.StopCondition{fantasy.StepCountIs(1)},
			ModelProvider: func() fantasy.LanguageModel {
				return a.largeModel.Get().Model
			},
			PrepareStep: func(callContext context.Context, options fantasy.PrepareStepFunctionOptions) (_ context.Context, prepared fantasy.PrepareStepResult, err error) {
				prepared.Messages = options.Messages
				for i := range prepared.Messages {
					prepared.Messages[i].ProviderOptions = nil
				}
				prepared.Messages = a.prepareRequestMessages(prepared.Messages, largeModel, systemPromptPrefix)
				return callContext, prepared, nil
			},
			OnReasoningDelta: func(id string, text string) error {
				summaryMessage.AppendReasoningContent(text)
				return a.messages.Update(genCtx, summaryMessage)
			},
			OnReasoningEnd: func(id string, reasoning fantasy.ReasoningContent) error {
				// Handle anthropic signature.
				if anthropicData, ok := reasoning.ProviderMetadata["anthropic"]; ok {
					if signature, ok := anthropicData.(*anthropic.ReasoningOptionMetadata); ok && signature.Signature != "" {
						summaryMessage.AppendReasoningSignature(signature.Signature)
					}
				}
				summaryMessage.FinishThinking()
				return a.messages.Update(genCtx, summaryMessage)
			},
			OnTextDelta: func(id, text string) error {
				summaryMessage.AppendContent(text)
				return a.messages.Update(genCtx, summaryMessage)
			},
		})
	}
	// The tools are listed but cannot run: the model is told not to call
	// them, and one it calls anyway does nothing.
	agentTools := a.requestTools(ctx, channel)
	listedTools := make([]fantasy.AgentTool, len(agentTools))
	for i, t := range agentTools {
		listedTools[i] = unrunnableTool{t}
	}
	resp, err := ask(a.requestSystemPrompt(), listedTools)
	if tooLongForContext(err) {
		// The system prompt and tools take room that a conversation filled
		// up by its last step may not have left. Without them, as it was
		// asked for before, the summary reads the whole conversation again
		// but fits.
		resp, err = ask("", nil)
	}
	if err != nil {
		isCancelErr := errors.Is(err, context.Canceled)
		if isCancelErr {
			// User cancelled summarize we need to remove the summary message.
			deleteErr := a.messages.Delete(ctx, summaryMessage.ID)
			return deleteErr
		}
		// Mark the summary message as finished with an error so the UI
		// stops spinning.
		summaryMessage.AddFinish(message.FinishReasonError, "Summarization Error", err.Error())
		if updateErr := a.messages.Update(ctx, summaryMessage); updateErr != nil {
			return updateErr
		}
		return err
	}
	// A reply with no summary in it, a tool call or thinking that used up
	// the output, would leave the session nothing to go on. The
	// conversation is kept as it is instead.
	if strings.TrimSpace(summaryMessage.Content().Text) == "" {
		err := errors.New("the model wrote no summary")
		summaryMessage.AddFinish(message.FinishReasonError, "Summarization Error", "The model wrote no summary; the conversation is kept as it is.")
		if updateErr := a.messages.Update(ctx, summaryMessage); updateErr != nil {
			return updateErr
		}
		return err
	}

	summaryMessage.AddFinish(message.FinishReasonEndTurn, "", "")
	err = a.messages.Update(genCtx, summaryMessage)
	if err != nil {
		return err
	}

	var openrouterCost *float64
	for _, step := range resp.Steps {
		stepCost := a.openrouterCost(step.ProviderMetadata)
		if stepCost != nil {
			newCost := *stepCost
			if openrouterCost != nil {
				newCost += *openrouterCost
			}
			openrouterCost = &newCost
		}
	}

	// Only what the summary changes is written, so a name the session was
	// given while it ran (/rename, the sessions dialog) is kept. Usage
	// added to an empty session is what the summary cost.
	spent := session.Session{ID: sessionID}
	a.updateSessionUsage(largeModel, &spent, resp.TotalUsage, openrouterCost, false)

	// Just in case, get just the last usage info.
	usage := resp.Response.Usage
	_, err = a.sessions.UpdateSummary(genCtx, sessionID, summaryMessage.ID, summaryCompletionTokens(usage, summaryMessage), spent.Cost, usageIsZero(usage))
	if err != nil {
		return err
	}

	// The next prompt to this session runs its SessionStart hooks again,
	// with source "compact", as in Claude Code.
	a.sessionStarts.markSummarized(sessionID)

	// The next prompt to this session re-fires its InstructionsLoaded
	// hooks, with load_reason "compact".
	a.markInstructionsLoadedSummarized(sessionID)

	// The user's PostCompact hooks get the summary, now that it is saved
	// and before any prompt queued meanwhile runs on it.
	a.runPostCompactHooks(ctx, sessionID, trigger, summaryMessage.Content().Text)

	// Release the active request before processing queued messages so that
	// Run() does not see the session as busy.
	a.activeRequests.Del(sessionID)
	cancel()

	// Process any messages that were queued while summarizing. The
	// read-modify-write holds the session's dispatch mutex, as every other
	// queue writer does (enqueueCall, PopQueuedPrompts, the turn-end
	// handoff), so a prompt queued or popped meanwhile is neither lost nor
	// put back. Run takes the same mutex, so it is released first.
	mu := a.sessionMu(sessionID)
	mu.Lock()
	queuedMessages, ok := a.messageQueue.Get(sessionID)
	if !ok || len(queuedMessages) == 0 {
		mu.Unlock()
		return nil
	}
	firstQueuedMessage := queuedMessages[0]
	a.messageQueue.Set(sessionID, queuedMessages[1:])
	mu.Unlock()
	_, qErr := a.Run(ctx, firstQueuedMessage)
	return qErr
}

// permissionMode is a session's permission mode as its hooks are told
// it. The plan agent's turns are plan, whatever else is set, as the TUI
// shows them.
func (a *sessionAgent) permissionMode(sessionID string) string {
	if a.agentType == config.AgentPlan {
		return config.PermissionModePlan
	}
	if a.permissionModeOf == nil {
		return config.PermissionModeDefault
	}
	return a.permissionModeOf(sessionID)
}

// requestSystemPrompt is the system prompt a session's requests send:
// the agent's, then the instructions of the MCP servers connected now.
func (a *sessionAgent) requestSystemPrompt() string {
	return a.systemPrompt.Get() + mcpInstructions()
}

// mcpInstructions is what a request's system prompt ends with for the
// connected MCP servers that give instructions, "" when none do.
func mcpInstructions() string {
	var instructions strings.Builder
	for _, server := range mcp.GetStates() {
		if server.State != mcp.StateConnected {
			continue
		}
		if s := server.Client.InitializeResult().Instructions; s != "" {
			instructions.WriteString(s)
			instructions.WriteString("\n\n")
		}
	}
	if s := instructions.String(); s != "" {
		return "\n\n<mcp-instructions>\n" + s + "\n</mcp-instructions>"
	}
	return ""
}

// requestTools is the tools a request of a turn in channel lists: the
// agent's latest, filtered for the channel and minus MCP servers disabled
// for this repository, the last one marked for prompt caching.
//
// The mark is the request's own. It used to be set on the agent's tool,
// which every request in every session shares, and stayed there: after
// a turn whose last tool was another (a channel's turn, say), a request
// carried two tool marks, and with the system prompt's and the last two
// messages' that is five, one more than Anthropic takes.
func (a *sessionAgent) requestTools(ctx context.Context, channel string) []fantasy.AgentTool {
	agentTools := a.filterDisabledMCPTools(ctx, filterToolsForChannel(a.tools.Copy(), channel, mcp.GetStates()))
	if len(agentTools) > 0 {
		last := len(agentTools) - 1
		agentTools[last] = cacheMarkedTool{AgentTool: agentTools[last], options: a.getCacheControlOptions()}
	}
	return agentTools
}

// cacheMarkedTool is a tool marked for prompt caching in one request,
// leaving the tool itself as it was.
type cacheMarkedTool struct {
	fantasy.AgentTool
	options fantasy.ProviderOptions
}

func (t cacheMarkedTool) ProviderOptions() fantasy.ProviderOptions { return t.options }

func (t cacheMarkedTool) Unwrap() fantasy.AgentTool { return t.AgentTool }

// prepareRequestMessages readies a request's messages as every request
// in a session sends them, a turn's steps and a summary alike, so that
// each starts as the one before it did.
func (a *sessionAgent) prepareRequestMessages(msgs []fantasy.Message, largeModel Model, promptPrefix string) []fantasy.Message {
	msgs = dropEmptyToolMedia(msgs)
	// Neutralize model control tokens that untrusted tool output or pasted text
	// may contain, so a vision-enabled server cannot read them as image/turn markers.
	msgs = sanitizeControlTokens(msgs)
	msgs = a.workaroundProviderMediaLimitations(msgs, largeModel)

	lastSystemRoleInx := 0
	systemMessageUpdated := false
	for i, msg := range msgs {
		// Only add cache control to the last message.
		if msg.Role == fantasy.MessageRoleSystem {
			lastSystemRoleInx = i
		} else if !systemMessageUpdated {
			msgs[lastSystemRoleInx].ProviderOptions = a.getCacheControlOptions()
			systemMessageUpdated = true
		}
		// Than add cache control to the last 2 messages.
		if i > len(msgs)-3 {
			msgs[i].ProviderOptions = a.getCacheControlOptions()
		}
	}

	if promptPrefix != "" {
		msgs = append([]fantasy.Message{fantasy.NewSystemMessage(promptPrefix)}, msgs...)
	}
	return msgs
}

// tooLongForContext reports whether err is the provider refusing a
// request that does not fit the model's context. Fantasy recognizes the
// hosted providers' refusals; a llama.cpp server's is its own.
func tooLongForContext(err error) bool {
	var providerErr *fantasy.ProviderError
	if !errors.As(err, &providerErr) {
		return false
	}
	return providerErr.IsContextTooLarge() || strings.Contains(providerErr.Message, "exceeds the available context size")
}

// unrunnableTool lists a tool in a request without letting it run.
type unrunnableTool struct{ fantasy.AgentTool }

func (unrunnableTool) Run(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
	return fantasy.NewTextErrorResponse("Tools do not run while the conversation is summarized."), nil
}

func (a *sessionAgent) getCacheControlOptions() fantasy.ProviderOptions {
	if t, _ := strconv.ParseBool(env.Getenv("KLASH_DISABLE_ANTHROPIC_CACHE")); t {
		return fantasy.ProviderOptions{}
	}
	return fantasy.ProviderOptions{
		anthropic.Name: &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		},
		bedrock.Name: &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		},
		vercel.Name: &anthropic.ProviderCacheControlOptions{
			CacheControl: anthropic.CacheControl{Type: "ephemeral"},
		},
	}
}

// sessionHeaders returns the HTTP headers we use for cache affinity on
// every LLM request for a given session.
//
// We use the session hash is used instead of the raw UUID so the header
// value is deterministic and opaque.
func sessionHeaders(sessionID string) map[string]string {
	hash := session.HashID(sessionID)
	return map[string]string{
		"x-session-id":       hash,
		"x-session-affinity": hash,
	}
}

func (a *sessionAgent) createUserMessage(ctx context.Context, call SessionAgentCall) (message.Message, error) {
	parts := []message.ContentPart{message.TextContent{Text: call.Prompt, Hidden: call.HiddenUserMessage, Notice: call.NoticeUserMessage}}
	var attachmentParts []message.ContentPart
	for _, attachment := range call.Attachments {
		attachmentParts = append(attachmentParts, message.BinaryContent{Path: attachment.FilePath, MIMEType: attachment.MimeType, Data: attachment.Content})
	}
	parts = append(parts, attachmentParts...)
	// What the user's hooks added is kept with the prompt as a hidden
	// part: the chat shows only what was typed, and the model reads it
	// after the prompt whenever the conversation is rebuilt.
	if block := promptHookBlock(call); block != "" {
		parts = append(parts, message.TextContent{Text: block, Hidden: true})
	}
	msg, err := a.messages.Create(ctx, call.SessionID, message.CreateMessageParams{
		Role:  message.User,
		Parts: parts,
	})
	if err != nil {
		return message.Message{}, fmt.Errorf("failed to create user message: %w", err)
	}
	return msg, nil
}

func (a *sessionAgent) preparePrompt(msgs []message.Message, supportsImages bool, attachments ...message.Attachment) ([]fantasy.Message, []fantasy.FilePart) {
	var history []fantasy.Message
	if !a.isSubAgent {
		history = append(history, fantasy.NewUserMessage(
			fmt.Sprintf(
				"<system_reminder>%s</system_reminder>",
				`This is a reminder that your todo list is currently empty. DO NOT mention this to the user explicitly because they are already aware.
If you are working on tasks that would benefit from a todo list please use the "todos" tool to create one.
If not, please feel free to ignore. Again do not mention this message to the user.`,
			),
		))
	}
	// Collect all tool call IDs present in assistant messages, then index
	// every tool result by its call ID. Tool results are re-emitted right
	// after the assistant message that requested them instead of at their
	// stored position: messages can be written to a session concurrently
	// (e.g. resuming while a tool is still running), which interleaves
	// user messages between a tool call and its result. LLM APIs require
	// every tool call to be followed by its results before any other
	// message, and strict-adjacency providers (e.g. Kimi, DeepSeek) reject
	// the request otherwise, permanently locking the session.
	knownToolCallIDs := make(map[string]struct{})
	for _, m := range msgs {
		if m.Role != message.Assistant {
			continue
		}
		for _, tc := range m.ToolCalls() {
			knownToolCallIDs[tc.ID] = struct{}{}
		}
	}
	toolResultsByCall := make(map[string][]fantasy.MessagePart)
	for _, m := range msgs {
		if m.Role != message.Tool {
			continue
		}
		for _, aiMsg := range m.ToAIMessage() {
			for _, part := range aiMsg.Content {
				tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
				if !ok {
					// Tool-role ToAIMessage only emits ToolResultParts today;
					// log so unexpected parts do not vanish silently.
					slog.Warn(
						"Dropping unexpected non-tool-result part from tool message",
						"part_type", fmt.Sprintf("%T", part),
					)
					continue
				}
				if _, known := knownToolCallIDs[tr.ToolCallID]; !known {
					slog.Warn(
						"Dropping orphaned tool result with no matching tool call",
						"tool_call_id", tr.ToolCallID,
					)
					continue
				}
				toolResultsByCall[tr.ToolCallID] = append(toolResultsByCall[tr.ToolCallID], part)
			}
		}
	}

	for _, m := range msgs {
		if len(m.Parts) == 0 {
			continue
		}
		// Assistant message without content or tool calls (cancelled before it returned anything).
		if m.Role == message.Assistant && len(m.ToolCalls()) == 0 && m.Content().Text == "" && m.ReasoningContent().String() == "" {
			continue
		}
		// Tool results are emitted right after their assistant message.
		if m.Role == message.Tool {
			continue
		}
		aiMsgs := m.ToAIMessage()
		if !supportsImages {
			for i := range aiMsgs {
				if aiMsgs[i].Role == fantasy.MessageRoleUser {
					aiMsgs[i].Content = filterFileParts(aiMsgs[i].Content)
				}
			}
		}
		history = append(history, aiMsgs...)

		if m.Role == message.Assistant && len(m.ToolCalls()) > 0 {
			history = append(history, toolResultsForCalls(m, toolResultsByCall))
		}
	}

	var files []fantasy.FilePart
	for _, attachment := range attachments {
		if attachment.IsText() {
			continue
		}
		if !supportsImages {
			continue
		}
		files = append(files, fantasy.FilePart{
			Filename:  attachment.FileName,
			Data:      attachment.Content,
			MediaType: attachment.MimeType,
		})
	}

	return history, files
}

// filterDisabledMCPTools removes tools from MCP servers disabled via the
// "Toggle MCPs" dialog. The override set is repository-scoped and shared
// by every session in the repository, including sub-agent sessions.
// Connections are process-global and left untouched; only the tool list
// changes.
func (a *sessionAgent) filterDisabledMCPTools(ctx context.Context, toolList []fantasy.AgentTool) []fantasy.AgentTool {
	disabledServers, err := a.sessions.MCPDisabledServers(ctx)
	if err != nil {
		slog.Error("Failed to list disabled MCP servers", "error", err)
		return toolList
	}
	if len(disabledServers) == 0 {
		return toolList
	}
	disabled := make(map[string]struct{}, len(disabledServers))
	for _, name := range disabledServers {
		disabled[name] = struct{}{}
	}
	filtered := make([]fantasy.AgentTool, 0, len(toolList))
	for _, t := range toolList {
		if mcpTool, ok := innermostTool(t).(interface{ MCP() string }); ok {
			if _, off := disabled[mcpTool.MCP()]; off {
				continue
			}
		}
		filtered = append(filtered, t)
	}
	return filtered
}

// filterFileParts removes fantasy.FilePart entries from a slice of message
// parts. Used to strip image attachments from historical user messages when
// the current model does not support them.
func filterFileParts(parts []fantasy.MessagePart) []fantasy.MessagePart {
	filtered := make([]fantasy.MessagePart, 0, len(parts))
	for _, part := range parts {
		if _, ok := fantasy.AsMessagePart[fantasy.FilePart](part); ok {
			continue
		}
		filtered = append(filtered, part)
	}
	return filtered
}

func (a *sessionAgent) getSessionMessages(ctx context.Context, session session.Session) ([]message.Message, error) {
	// Read only the tail a compacted session actually sends. The full
	// transcript can be tens of megabytes on the single shared connection.
	msgs, err := a.messages.ListFromSummary(ctx, session.ID, session.SummaryMessageID)
	if err != nil {
		return nil, fmt.Errorf("failed to list messages: %w", err)
	}

	if session.SummaryMessageID != "" {
		summaryMsgIndex := -1
		for i, msg := range msgs {
			if msg.ID == session.SummaryMessageID {
				summaryMsgIndex = i
				break
			}
		}
		if summaryMsgIndex != -1 {
			msgs = msgs[summaryMsgIndex:]
			msgs[0].Role = message.User
		}
	}
	return msgs, nil
}

// hasUserTextMessage reports whether any user message in msgs contains
// text content (as opposed to only shell commands or other non-text parts).
func hasUserTextMessage(msgs []message.Message) bool {
	for _, msg := range msgs {
		if msg.Role != message.User {
			continue
		}
		for _, part := range msg.Parts {
			if tc, ok := part.(message.TextContent); ok && tc.Text != "" {
				return true
			}
		}
	}
	return false
}

// GenerateTitle generates a session title based on the initial prompt.
// titleFromPrompt is the title a session gets when the model does not
// produce one: the start of the user's prompt, truncated to 50 columns. It
// says far more than DefaultSessionName when looking for the session later;
// the generic name is kept for an empty prompt.
func titleFromPrompt(userPrompt string) string {
	fallback := strings.TrimSpace(strings.ReplaceAll(userPrompt, "\n", " "))
	if len(fallback) > 50 {
		fallback = ansi.Truncate(fallback, 50, "…")
	}
	return cmp.Or(fallback, DefaultSessionName)
}

// titleProviderOptions returns the request options for asking model for a
// title: thinking off, where its provider supports the switch.
//
// The title prompt also carries a /no_think hint and an empty think block,
// but newer Qwen models ignore both; only llama.cpp's chat-template switch
// reaches them. Without it the model reasoned through its 40-token budget,
// was cut off before answering, and every session on the AI box came out
// "Untitled Session".
func (a *sessionAgent) titleProviderOptions(model Model) fantasy.ProviderOptions {
	opts := fantasy.ProviderOptions{}
	if a.cfg == nil || a.cfg.Config().Providers == nil {
		return opts
	}
	if providerCfg, ok := a.cfg.Config().Providers.Get(model.ModelCfg.Provider); ok {
		disableThinking(opts, providerCfg)
	}
	return opts
}

func (a *sessionAgent) GenerateTitle(ctx context.Context, sessionID string, userPrompt string) {
	if userPrompt == "" {
		return
	}

	// The title the session has now. A name it is given while this runs
	// (/rename, the sessions dialog) is kept instead of the one made here.
	sess, getErr := a.sessions.Get(context.WithoutCancel(ctx), sessionID)
	if getErr != nil {
		slog.Error("Failed to read the session to title", "error", getErr)
		return
	}
	untitled := sess.Title

	// Ensure the session always gets a title even if every path below
	// fails or the context is cancelled before we finish.
	var titleSaved bool
	defer func() {
		if !titleSaved {
			fallbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if _, renamed := a.renamedFrom(fallbackCtx, sessionID, untitled); renamed {
				return
			}
			if err := a.sessions.Rename(fallbackCtx, sessionID, titleFromPrompt(userPrompt)); err != nil {
				slog.Error("Failed to save fallback session title", "error", err)
			}
		}
	}()

	prompt := fmt.Sprintf("Generate a concise title for the following content:\n\n%s\n <think>\n\n</think>", userPrompt)
	resp, model, err := a.askForName(ctx, sessionID, "title", titlePrompt, prompt, 40)
	if err != nil {
		// The deferred fallback names the session after the prompt.
		return
	}

	// Clean up title.
	var title string
	title = strings.ReplaceAll(resp.Response.Content.Text(), "\n", " ")

	// Remove thinking tags if present.
	title = thinkTagRegex.ReplaceAllString(title, "")
	title = orphanThinkTagRegex.ReplaceAllString(title, "")

	title = strings.TrimSpace(title)
	if title == "" {
		// The model returned empty content.
		title = titleFromPrompt(userPrompt)
	}

	cost := a.answerCost(model, resp)
	promptTokens := contextTokens(resp.TotalUsage)
	completionTokens := resp.TotalUsage.OutputTokens

	if name, renamed := a.renamedFrom(ctx, sessionID, untitled); renamed {
		title = name
	}

	// Atomically update only title and usage fields to avoid overriding other
	// concurrent session updates.
	saveErr := a.sessions.UpdateTitleAndUsage(ctx, sessionID, title, promptTokens, completionTokens, cost)
	if saveErr != nil {
		slog.Error("Failed to save session title and usage", "error", saveErr)
		return
	}
	titleSaved = true
}

// askForName asks for a title or a name: the small model first, then the
// large one if that fails or is cut off, with no tools, thinking off where
// the provider has a switch, and maxTokens for the answer (a reasoning
// model's whole default instead, as it may think anyway). what names it
// in the log.
func (a *sessionAgent) askForName(ctx context.Context, sessionID, what string, systemPrompt []byte, prompt string, maxTokens int64) (*fantasy.AgentResult, Model, error) {
	systemPromptPrefix := a.systemPromptPrefix.Get()
	call := fantasy.AgentStreamCall{
		Prompt:  prompt,
		Headers: sessionHeaders(sessionID),
		PrepareStep: func(callCtx context.Context, opts fantasy.PrepareStepFunctionOptions) (_ context.Context, prepared fantasy.PrepareStepResult, err error) {
			prepared.Messages = opts.Messages
			if systemPromptPrefix != "" {
				prepared.Messages = append([]fantasy.Message{
					fantasy.NewSystemMessage(systemPromptPrefix),
				}, prepared.Messages...)
			}
			return callCtx, prepared, nil
		},
	}
	var lastErr error
	for _, attempt := range []struct {
		name  string
		model Model
	}{
		{"small", a.smallModel.Get()},
		{"large", a.largeModel.Get()},
	} {
		tok := maxTokens
		if attempt.model.CatwalkCfg.CanReason {
			tok = attempt.model.CatwalkCfg.DefaultMaxTokens
		}
		agent := fantasy.NewAgent(
			attempt.model.Model,
			fantasy.WithSystemPrompt(string(systemPrompt)+"\n /no_think"),
			fantasy.WithMaxOutputTokens(tok),
			fantasy.WithUserAgent(userAgent),
		)
		call.ProviderOptions = a.titleProviderOptions(attempt.model)
		resp, err := agent.Stream(ctx, call)
		if err == nil && resp.Response.FinishReason != fantasy.FinishReasonLength {
			slog.Debug("Generated " + what + " with " + attempt.name + " model")
			return resp, attempt.model, nil
		}
		if err == nil {
			err = errors.New("the answer was cut off at its token limit")
		}
		// A warning, not an error: the next model may answer, and the
		// caller has a way on without one.
		slog.Warn("Could not generate a "+what+" with the "+attempt.name+" model; trying the next", "err", err)
		lastErr = err
	}
	return nil, Model{}, lastErr
}

// answerCost is what resp cost on model: its provider's figure when it
// gives one (OpenRouter), nothing on a flat rate.
func (a *sessionAgent) answerCost(model Model, resp *fantasy.AgentResult) float64 {
	var openrouterCost *float64
	for _, step := range resp.Steps {
		stepCost := a.openrouterCost(step.ProviderMetadata)
		if stepCost != nil {
			newCost := *stepCost
			if openrouterCost != nil {
				newCost += *openrouterCost
			}
			openrouterCost = &newCost
		}
	}

	modelConfig := model.CatwalkCfg
	cost := modelConfig.CostPer1MInCached/1e6*float64(resp.TotalUsage.CacheCreationTokens) +
		modelConfig.CostPer1MOutCached/1e6*float64(resp.TotalUsage.CacheReadTokens) +
		modelConfig.CostPer1MIn/1e6*float64(resp.TotalUsage.InputTokens) +
		modelConfig.CostPer1MOut/1e6*float64(resp.TotalUsage.OutputTokens)

	// Use override cost if available (e.g., from OpenRouter).
	if openrouterCost != nil {
		cost = *openrouterCost
	}

	// Skip cost accumulation
	if model.FlatRate {
		cost = 0
	}
	return cost
}

// renamedFrom reports the title sessionID has now when it is no longer
// title: the session was named while its title was being made.
func (a *sessionAgent) renamedFrom(ctx context.Context, sessionID, title string) (string, bool) {
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil || sess.Title == title {
		return "", false
	}
	return sess.Title, true
}

func (a *sessionAgent) openrouterCost(metadata fantasy.ProviderMetadata) *float64 {
	openrouterMetadata, ok := metadata[openrouter.Name]
	if !ok {
		return nil
	}

	opts, ok := openrouterMetadata.(*openrouter.ProviderMetadata)
	if !ok {
		return nil
	}
	return &opts.Usage.Cost
}

// extractPrismModel returns the ID and name of the model that actually
// served the turn, as reported by the Hyper Prism model router headers,
// or empty strings when the turn was not routed through a Prism model.
func extractPrismModel(metadata fantasy.ProviderMetadata) (modelID, modelName string) {
	openaiMeta, ok := metadata[openai.Name]
	if !ok {
		return "", ""
	}
	pm, ok := openaiMeta.(*openai.ProviderMetadata)
	if !ok {
		return "", ""
	}
	_ = pm.ExtraField(hyper.PrismModelIDField, &modelID)
	_ = pm.ExtraField(hyper.PrismModelNameField, &modelName)
	return modelID, modelName
}

// extractPrismSavings returns the hypercredit and dollar savings from
// routing the turn through the Hyper Prism model router, as reported by
// its savings trailers, or nil when not reported or malformed.
func extractPrismSavings(metadata fantasy.ProviderMetadata) (hypercredits, dollars *float64) {
	openaiMeta, ok := metadata[openai.Name]
	if !ok {
		return nil, nil
	}
	pm, ok := openaiMeta.(*openai.ProviderMetadata)
	if !ok {
		return nil, nil
	}
	return extraFieldFloat(pm, hyper.PrismHypercreditSavingsField), extraFieldFloat(pm, hyper.PrismDollarSavingsField)
}

func extraFieldFloat(pm *openai.ProviderMetadata, key string) *float64 {
	var value string
	if !pm.ExtraField(key, &value) {
		return nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		slog.Warn("Could not parse Prism savings", "key", key, "value", value, "error", err)
		return nil
	}
	return &parsed
}

func (a *sessionAgent) updateSessionUsage(model Model, session *session.Session, usage fantasy.Usage, overrideCost *float64, estimated bool) {
	if !usageIsZero(usage) {
		session.EstimatedUsage = estimated
	}

	modelConfig := model.CatwalkCfg
	cost := modelConfig.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
		modelConfig.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
		modelConfig.CostPer1MIn/1e6*float64(usage.InputTokens) +
		modelConfig.CostPer1MOut/1e6*float64(usage.OutputTokens)

	if !estimated {
		a.eventTokensUsed(session.ID, model, usage, cost)
	}

	if estimated {
		cost = 0
	} else {
		// Use override cost if available (e.g., from OpenRouter).
		if overrideCost != nil {
			cost = *overrideCost
		}

		// Skip cost accumulation
		if model.FlatRate {
			cost = 0
		}
	}

	session.Cost += cost
	updateSessionTokenCounters(session, usage)
}

// contextTokens returns the size of the prompt the provider processed for
// a step. Providers with prompt caching (Anthropic, Bedrock, Vercel) report
// the prompt as three disjoint buckets: tokens served from cache, tokens
// newly written to the cache, and the uncached remainder. All three occupy
// the context window, so all three count. Providers without cache writes
// leave CacheCreationTokens at zero, and the OpenAI-style providers already
// subtract cached tokens from InputTokens, so nothing is counted twice.
func contextTokens(usage fantasy.Usage) int64 {
	return usage.InputTokens + usage.CacheReadTokens + usage.CacheCreationTokens
}

func updateSessionTokenCounters(session *session.Session, usage fantasy.Usage) {
	if usage.OutputTokens != 0 {
		session.CompletionTokens = usage.OutputTokens
	}
	if promptTokens := contextTokens(usage); promptTokens != 0 {
		session.PromptTokens = promptTokens
	}
}

func summaryCompletionTokens(usage fantasy.Usage, summaryMessage message.Message) int64 {
	if usage.OutputTokens != 0 {
		return usage.OutputTokens
	}
	return approxTokenCount(summaryMessage.Content().Text) + approxTokenCount(summaryMessage.ReasoningContent().String())
}

func (a *sessionAgent) Cancel(sessionID string) {
	// Serialize against the dispatch handoff in Run so the accepted ->
	// (cancel-on-entry | queued | active) transition is atomic against
	// this cancel. Every cancel observes at least one of: an active
	// request, an accepted run (recorded as a pending cancel), or a
	// queue entry it then clears. If none of those hold, an idle Escape
	// is a true no-op and must not poison the next prompt.
	mu := a.sessionMu(sessionID)
	mu.Lock()
	defer mu.Unlock()

	// Cancel regular requests. Don't use Take() here - we need the entry to
	// remain in activeRequests so IsBusy() returns true until the goroutine
	// fully completes (including error handling that may access the DB).
	// The defer in processRequest will clean up the entry.
	if ac, ok := a.activeRequests.Get(sessionID); ok && ac != nil {
		slog.Debug("Request cancellation initiated", "session_id", sessionID)
		ac.cancel()
	}

	// Also check for summarize requests.
	if ac, ok := a.activeRequests.Get(sessionID + "-summarize"); ok && ac != nil {
		slog.Debug("Summarize cancellation initiated", "session_id", sessionID)
		ac.cancel()
	}

	// Record a pending cancel only when a dispatched-but-not-yet-active
	// run exists. This catches runs still in the goroutine scheduler or
	// about to enter Run's busy-queue branch, while leaving an idle
	// session untouched. Active and accepted are not mutually exclusive:
	// when a run is active and a follow-up has been accepted, both the
	// cancel above and this pending record fire.
	//
	// Raise the session's cancel mark to the latest accept sequence
	// assigned so far. Every prompt currently accepted-but-not-yet-
	// active has a sequence at or below that value, so one cancel covers
	// all of them; a prompt accepted after this cancel gets a strictly
	// higher sequence and is never poisoned. Using max keeps repeated
	// cancels idempotent while the same prompts are in flight and lets a
	// later cancel extend coverage to prompts accepted since.
	a.acceptedMu.Lock()
	count, ok := a.acceptedRuns.Get(sessionID)
	mark := a.acceptSeqGen
	a.acceptedMu.Unlock()
	if ok && count > 0 {
		slog.Debug("Recording cancel mark for accepted runs", "session_id", sessionID, "count", count, "mark", mark)
		existing, _ := a.cancelMark.Get(sessionID)
		a.cancelMark.Set(sessionID, max(existing, mark))
	}

	if a.hasQueuedCalls(sessionID) {
		slog.Debug("Clearing queued prompts", "session_id", sessionID)
		a.clearQueueAndNotify(sessionID)
	}

	// A prompt still in its UserPromptSubmit hooks is not sent either.
	a.cancelSubmitting(sessionID)
}

func (a *sessionAgent) ClearQueue(sessionID string) {
	mu := a.sessionMu(sessionID)
	mu.Lock()
	if a.hasQueuedCalls(sessionID) {
		slog.Debug("Clearing queued prompts", "session_id", sessionID)
		a.clearQueueAndNotify(sessionID)
	}
	mu.Unlock()
}

// hasQueuedCalls reports whether anything at all is queued for the session,
// the agent's own hidden continuations included. Cancel and ClearQueue go by
// this rather than QueuedPrompts, which counts only what the user sent: an
// Esc must also drop a queued Stop-hook or loop-detector follow-up, or the
// agent would carry on after the user stopped it.
func (a *sessionAgent) hasQueuedCalls(sessionID string) bool {
	l, ok := a.messageQueue.Get(sessionID)
	return ok && len(l) > 0
}

func (a *sessionAgent) CancelAll() {
	if !a.IsBusy() {
		return
	}
	for key := range a.activeRequests.Seq2() {
		a.Cancel(key) // key is sessionID
	}

	timeout := time.After(5 * time.Second)
	for a.IsBusy() {
		select {
		case <-timeout:
			return
		default:
			time.Sleep(200 * time.Millisecond)
		}
	}
}

func (a *sessionAgent) IsBusy() bool {
	var busy bool
	for ac := range a.activeRequests.Seq() {
		if ac != nil {
			busy = true
			break
		}
	}
	return busy || a.submitting.Len() > 0
}

// IsSessionBusy reports whether a turn is running in the session, or a
// prompt sent to it is still in its UserPromptSubmit hooks.
func (a *sessionAgent) IsSessionBusy(sessionID string) bool {
	return a.hasActiveRun(sessionID) || a.isSubmitting(sessionID)
}

// hasActiveRun reports whether a turn is running in the session. Run's
// dispatch asks this, not IsSessionBusy: a prompt counts as submitting
// while its own hooks run, and must not then queue behind itself.
func (a *sessionAgent) hasActiveRun(sessionID string) bool {
	_, busy := a.activeRequests.Get(sessionID)
	return busy
}

// queuedUserCalls returns the session's queued calls minus the agent's own
// hidden continuations (loop detection and Stop-hook follow-ups), which the
// queue handoff re-queues at the end of a turn. User-sent prompts are what
// the queue pill and the editor's Up key present, so both count and list
// only those.
func (a *sessionAgent) queuedUserCalls(sessionID string) []SessionAgentCall {
	l, ok := a.messageQueue.Get(sessionID)
	if !ok {
		return nil
	}
	var user []SessionAgentCall
	for _, call := range l {
		if !call.HiddenUserMessage {
			user = append(user, call)
		}
	}
	return user
}

func (a *sessionAgent) QueuedPrompts(sessionID string) int {
	return len(a.queuedUserCalls(sessionID))
}

func (a *sessionAgent) QueuedPromptsList(sessionID string) []string {
	user := a.queuedUserCalls(sessionID)
	prompts := make([]string, 0, len(user))
	for _, call := range user {
		prompts = append(prompts, call.Prompt)
	}
	return prompts
}

// PopQueuedPrompts takes the prompts the user typed out of the session's
// queue, oldest first, along with the attachments that came with each, and
// leaves everything else queued:
//   - hidden continuations, for the turn-end handoff to pick up;
//   - prompts that carry a RunID: a caller such as `klash run` is waiting
//     for their RunComplete and would hang;
//   - channel messages (a Signal message, say): the user did not write
//     them, and in the editor they would lose the channel their reply
//     goes back to.
//
// What is popped is fire-and-forget (the TUI sends no RunID), so removing it
// publishes nothing.
func (a *sessionAgent) PopQueuedPrompts(sessionID string) []message.PoppedPrompt {
	mu := a.sessionMu(sessionID)
	mu.Lock()
	defer mu.Unlock()
	l, ok := a.messageQueue.Get(sessionID)
	if !ok {
		return nil
	}
	var popped []message.PoppedPrompt
	var keep []SessionAgentCall
	for _, call := range l {
		if call.HiddenUserMessage || call.RunID != "" || call.Channel != "" {
			keep = append(keep, call)
			continue
		}
		popped = append(popped, message.PoppedPrompt{
			Prompt:      call.Prompt,
			Attachments: call.Attachments,
		})
	}
	if len(keep) == 0 {
		a.messageQueue.Del(sessionID)
	} else {
		a.messageQueue.Set(sessionID, keep)
	}
	return popped
}

func (a *sessionAgent) SetModels(large Model, small Model) {
	a.largeModel.Set(large)
	a.smallModel.Set(small)
}

func (a *sessionAgent) SetTools(tools []fantasy.AgentTool) {
	a.tools.SetSlice(tools)
}

func (a *sessionAgent) ToolNames() []string {
	tools := a.tools.Copy()
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Info().Name
	}
	return names
}

func (a *sessionAgent) SetSystemPrompt(systemPrompt string) {
	a.systemPrompt.Set(systemPrompt)
}

func (a *sessionAgent) Model() Model {
	return a.largeModel.Get()
}

// convertToToolResult converts a fantasy tool result to a message tool result.
func (a *sessionAgent) convertToToolResult(result fantasy.ToolResultContent) message.ToolResult {
	baseResult := message.ToolResult{
		ToolCallID: result.ToolCallID,
		Name:       result.ToolName,
		Metadata:   result.ClientMetadata,
	}

	switch result.Result.GetType() {
	case fantasy.ToolResultContentTypeText:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentText](result.Result); ok {
			baseResult.Content = r.Text
		}
	case fantasy.ToolResultContentTypeError:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentError](result.Result); ok {
			baseResult.Content = r.Error.Error()
			baseResult.IsError = true
		}
	case fantasy.ToolResultContentTypeMedia:
		if r, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](result.Result); ok {
			switch {
			case r.Data == "":
				slog.Warn("Tool returned media with no data, storing a note instead",
					"tool", result.ToolName, "tool_call_id", result.ToolCallID)
				baseResult.Content = emptyMediaText(r.Text)
			case !stringext.IsValidBase64(r.Data):
				slog.Warn(
					"Tool returned media with invalid base64 data, discarding image",
					"tool", result.ToolName,
					"tool_call_id", result.ToolCallID,
				)
				baseResult.Content = "Tool returned image data with invalid encoding"
				baseResult.IsError = true
			default:
				content := r.Text
				if content == "" {
					content = fmt.Sprintf("Loaded %s content", r.MediaType)
				}
				baseResult.Content = content
				baseResult.Data = r.Data
				baseResult.MIMEType = r.MediaType
			}
		}
	}

	return baseResult
}

// workaroundProviderMediaLimitations converts media content in tool results to
// user messages for providers that don't natively support images in tool results.
//
// Problem: OpenAI, Google, OpenRouter, and other OpenAI-compatible providers
// don't support sending images/media in tool result messages - they only accept
// text in tool results. However, they DO support images in user messages.
//
// If we send media in tool results to these providers, the API returns an error.
//
// Solution: For these providers, we:
//  1. Replace the media in the tool result with a text placeholder
//  2. Inject a user message immediately after with the image as a file attachment
//  3. This maintains the tool execution flow while working around API limitations
//
// Anthropic and Bedrock support images natively in tool results, so we skip
// this workaround for them.
//
// Example transformation:
//
//	BEFORE: [tool result: image data]
//	AFTER:  [tool result: "Image loaded - see attached"], [user: image attachment]
func (a *sessionAgent) workaroundProviderMediaLimitations(messages []fantasy.Message, largeModel Model) []fantasy.Message {
	providerSupportsMedia := largeModel.ModelCfg.Provider == string(catwalk.InferenceProviderAnthropic) ||
		largeModel.ModelCfg.Provider == string(catwalk.InferenceProviderBedrock) ||
		largeModel.ModelCfg.Provider == string(catwalk.InferenceProviderBedrockEurope)

	if providerSupportsMedia {
		return messages
	}

	supportsImages := largeModel.CatwalkCfg.SupportsImages

	convertedMessages := make([]fantasy.Message, 0, len(messages))

	for _, msg := range messages {
		if msg.Role != fantasy.MessageRoleTool {
			convertedMessages = append(convertedMessages, msg)
			continue
		}

		textParts := make([]fantasy.MessagePart, 0, len(msg.Content))
		var mediaFiles []fantasy.FilePart

		for _, part := range msg.Content {
			toolResult, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
			if !ok {
				textParts = append(textParts, part)
				continue
			}

			if media, ok := fantasy.AsToolResultOutputType[fantasy.ToolResultOutputContentMedia](toolResult.Output); ok {
				if !supportsImages {
					// Model cannot process images. Replace with a text
					// placeholder and skip creating a synthetic user
					// message with FilePart, which would brick the
					// session on text-only models.
					textParts = append(textParts, fantasy.ToolResultPart{
						ToolCallID: toolResult.ToolCallID,
						Output: fantasy.ToolResultOutputContentText{
							Text: "[Image/media content not supported by this model]",
						},
						ProviderOptions: toolResult.ProviderOptions,
					})
					continue
				}

				decoded, err := base64.StdEncoding.DecodeString(media.Data)
				if err != nil {
					slog.Warn("Failed to decode media data", "error", err)
					textParts = append(textParts, part)
					continue
				}

				mediaFiles = append(mediaFiles, fantasy.FilePart{
					Data:      decoded,
					MediaType: media.MediaType,
					Filename:  fmt.Sprintf("tool-result-%s", toolResult.ToolCallID),
				})

				textParts = append(textParts, fantasy.ToolResultPart{
					ToolCallID: toolResult.ToolCallID,
					Output: fantasy.ToolResultOutputContentText{
						Text: "[Image/media content loaded - see attached file]",
					},
					ProviderOptions: toolResult.ProviderOptions,
				})
			} else {
				textParts = append(textParts, part)
			}
		}

		convertedMessages = append(convertedMessages, fantasy.Message{
			Role:    fantasy.MessageRoleTool,
			Content: textParts,
		})

		if len(mediaFiles) > 0 {
			convertedMessages = append(convertedMessages, fantasy.NewUserMessage(
				"Here is the media content from the tool result:",
				mediaFiles...,
			))
		}
	}

	return convertedMessages
}

// buildSummaryPrompt is the message asking for a session's summary: how
// to write it, then the session's todos to carry over.
// buildSummaryPrompt is the summary request's last user message: the
// summary instructions, the session's todos, and, as Claude Code adds
// them, the text given after /compact.
func buildSummaryPrompt(todos []session.Todo, instructions string) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(string(summaryPrompt)))
	if len(todos) > 0 {
		sb.WriteString("\n\n## Current Todo List\n\n")
		for _, t := range todos {
			fmt.Fprintf(&sb, "- [%s] %s\n", t.Status, t.Content)
		}
		sb.WriteString("\nInclude these tasks and their statuses in your summary. ")
		sb.WriteString("Instruct the resuming assistant to use the `todos` tool to continue tracking progress on these tasks.")
	}
	if instructions = strings.TrimSpace(instructions); instructions != "" {
		sb.WriteString("\n\nAdditional Instructions:\n")
		sb.WriteString(instructions)
	}
	return sb.String()
}

func providerRetryLogFields(err *fantasy.ProviderError, delay time.Duration) []any {
	fields := []any{
		"retry_delay", delay.String(),
	}
	if err == nil {
		return fields
	}
	fields = append(fields, "status_code", err.StatusCode)
	if err.Title != "" {
		fields = append(fields, "title", err.Title)
	}
	if err.Message != "" {
		fields = append(fields, "message", err.Message)
	}
	return fields
}

// sanitizeToolInput validates tool call JSON from the provider.
// Malformed input is replaced with an empty object to prevent
// stuck conversations from truncated or malformed model output.
// The second return value indicates whether sanitization occurred.
func sanitizeToolInput(toolName, toolCallID, input string) (string, bool) {
	if !json.Valid([]byte(input)) {
		slog.Warn(
			"Malformed tool call JSON from provider, replacing with empty object",
			"tool", toolName,
			"id", toolCallID,
			"input_len", len(input),
		)
		return "{}", true
	}
	return input, false
}
