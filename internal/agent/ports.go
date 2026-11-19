package agent

import (
	"context"
	"encoding/json"
	"iter"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// UnknownWindow is the context window the loop assumes for a model whose
// window nobody could learn. Treating "unknown" as "never too long" once
// wedged a session for good; treating it as small only costs an early
// summary.
const UnknownWindow = 32768

// Model is a model the loop sends requests to, with what the loop has to
// know about it.
type Model struct {
	// Provider is the service that runs the model.
	Provider core.Provider

	// Info names the model and gives its context window. A window of zero
	// means nobody could learn it, and the loop then uses UnknownWindow.
	Info core.ModelInfo

	// Address is where the provider lives, such as http://127.0.0.1:8080.
	// It is only used in messages, so that a failure names the server.
	Address string

	// Local says the provider is a model server on the person's own machine
	// or network. For such a server a reply that is cut off at the length
	// limit, when no limit was asked for, means the server's real window
	// is smaller than Info says, and the loop then learns the real one.
	Local bool

	// Levels are the model's thinking levels from the least thinking to the
	// most: "off" and "on" for a model with a switch, "low", "medium" and
	// "high" for one with an effort dial. Empty means the loop knows of no
	// switch for this model.
	Levels []string

	// Level is the thinking level main requests ask for. It must be one of
	// Levels, or empty to leave the choice to the model.
	Level string

	// MaxTokens caps each main reply. Zero sends no cap, which is right for
	// a local server.
	MaxTokens int

	// ThinkingBudget caps how many tokens a main reply may think, when the
	// model thinks; zero sends no cap. BudgetNote is put at the end of the
	// thinking when the cap is reached, where the provider can.
	ThinkingBudget int
	BudgetNote     string

	// Temperature and TopP are sent on main requests when not nil.
	Temperature *float64
	TopP        *float64

	// Extra is merged into every request to this model, main and side.
	Extra map[string]any

	// ResendsThinking says the provider sends the thinking of earlier
	// replies back to the model, so that the loop counts it when it
	// estimates how long a request is.
	ResendsThinking bool
}

// window returns the context window the loop works with for m.
func (m Model) window() int {
	if m.Info.ContextWindow > 0 {
		return m.Info.ContextWindow
	}
	return UnknownWindow
}

// label names m for a person: the provider, the model, and the address when
// one is known.
func (m Model) label() string {
	name := "the model"
	if m.Provider != nil && m.Provider.Name() != "" {
		name = m.Provider.Name()
	}
	if m.Info.ID != "" {
		name += " (" + m.Info.ID + ")"
	}
	if m.Address != "" {
		name += " at " + m.Address
	}
	return name
}

// thinkingFor turns the name of a thinking level into what a request
// carries.
func thinkingFor(level string) core.Thinking {
	switch level {
	case "":
		return core.Thinking{}
	case "off", "none":
		return core.Thinking{Mode: "off"}
	case "on":
		return core.Thinking{Mode: "on"}
	}
	return core.Thinking{Effort: level}
}

// lowestThinking is what a side request asks for: the least thinking the
// model has, and the plain off switch for a model with no known levels.
func (m Model) lowestThinking() core.Thinking {
	if len(m.Levels) > 0 {
		return thinkingFor(m.Levels[0])
	}
	return core.Thinking{Mode: "off"}
}

// Log is the session's transcript as the loop needs it: a place to append
// what happened, in the order it happened, and the view the next request is
// built from. The loop never changes what it has appended.
//
// The loop calls a Log from more than one goroutine (the session's own,
// the one that takes in prompts sent while a turn runs, and whoever has a
// notice to keep), but never from two at once. The log's owner may change
// what View returns between two calls (a popped frame flags entries); the
// loop asks again before every request.
type Log interface {
	// Append stores entries after everything stored before, in the order
	// given, and returns once they are stored. An error means none of them
	// may be relied on, and the loop ends the turn saying so.
	Append(ctx context.Context, entries ...Entry) error

	// View returns the conversation as the model is to read it.
	//
	// The rules a View follows, so that what the loop appended is what the
	// model reads, a request begins with the bytes of the one before, and
	// the messages are ones core.Request allows:
	//
	//   - EntryPrompt and EntryNote are user-side text; EntryResult is a
	//     user-side tool_result block; EntryAssistant is an assistant
	//     message, unless it is Silent, and then it is left out.
	//   - Entries that follow each other on the user's side are joined
	//     into one user message, blocks in the order stored. The loop
	//     stores a round's results before anything else that follows the
	//     reply, so the tool_result blocks come first.
	//   - The view starts at the newest EntrySummary, which is user-side
	//     text; nothing before it is in the view.
	//   - EntryNotice, EntryTurn, EntryQueued and EntryUnqueued are never
	//     in the view.
	//   - Whatever the owner leaves out (the entries of a popped frame),
	//     no tool call is left without its result.
	//   - The same entries always give the same messages.
	View(ctx context.Context) (View, error)

	// Recent yields the stored entries from the newest to the oldest, as
	// they were appended. The loop reads back as far as the end of the
	// turn before last, to settle what a killed process left open: calls
	// without results, a turn without an end, a prompt that was accepted
	// and never sent. What the log's owner stored for itself (a mark on
	// the entries of a popped frame) is left out, or given a kind of its
	// own, which the loop passes over.
	Recent(ctx context.Context) iter.Seq2[Entry, error]
}

// View is the conversation as the model is to read it.
type View struct {
	// Messages starts with a user message, roles alternate, and every tool
	// call is answered in the message after it. The one exception is the
	// very end of a log whose process was killed in the middle of a round;
	// the loop settles that before it sends anything.
	Messages []core.Message
}

// Roomer is a Log whose owner can take the oldest part of the conversation
// out of the view without a summary: the slide of a session that stands in
// frames. A Log that is not a Roomer has no room to give, and the loop goes
// straight to a summary.
type Roomer interface {
	// MakeRoom takes at least req.Need tokens out of the view if it can,
	// less if that is all there is, and says how much it took. Freed zero
	// means nothing could go. What it takes out stays out: the owner
	// records the cut in its own store before it returns, so that every
	// later View leaves the same entries out and the cut does not move
	// from one request to the next. The loop calls MakeRoom as it calls
	// Append and View, never beside them, and asks for the View again
	// afterwards.
	MakeRoom(ctx context.Context, req RoomRequest) (Room, error)
}

// RoomMoment says where in a turn room is being asked for, which decides
// how the model learns of it.
type RoomMoment string

const (
	// RoomBeforePrompt: a prompt a person typed is about to go out. The
	// prompt's own lines can tell the model, so Room.Reminder may be empty.
	RoomBeforePrompt RoomMoment = "before-prompt"
	// RoomBeforeOwnPrompt: a prompt of gnosh's own is about to go out. It
	// carries no lines, so the model is told by Room.Reminder.
	RoomBeforeOwnPrompt RoomMoment = "before-own-prompt"
	// RoomMidTurn: between two rounds of a running turn.
	RoomMidTurn RoomMoment = "mid-turn"
	// RoomBeforeSummary: a summary the person asked for is about to be
	// made.
	RoomBeforeSummary RoomMoment = "before-summary"
)

// RoomRequest asks a Roomer to take something out of the view.
type RoomRequest struct {
	// Need is how many tokens to free, by the loop's estimate: what the
	// next request is over the compaction point, and a tenth of the window
	// besides so that the rounds that follow have room.
	Need int
	// Moment says where in the turn this is.
	Moment RoomMoment
	// Protect is the id of the newest prompt of the running turn, which
	// must stay. It is empty before a new prompt goes out.
	Protect string
	// Estimate and EstimateMessage are the loop's token estimators, for a
	// text and for a whole message, so that both sides count the same
	// way. Neither is nil.
	Estimate        func(string) int
	EstimateMessage func(core.Message) int
}

// Room is what a Roomer did.
type Room struct {
	// Freed is about how many tokens left the view. Zero means none.
	Freed int
	// Reminder, when not empty, is a line the model must read before its
	// next reply. The loop stores it as a hidden note.
	Reminder string
	// Notice, when not empty, is a line for the person.
	Notice string
}

// EntryKind says what an Entry of the log holds.
type EntryKind string

const (
	// EntryPrompt is a prompt: one a person typed, or with Hidden set, one
	// gnosh sent for itself (a nudge, a check's objection, a continuation).
	EntryPrompt EntryKind = "prompt"
	// EntryNote is hidden text of the harness on the user's side: a part
	// that rides on a prompt (Prompt names it), or a reminder put between
	// two rounds (Prompt is empty).
	EntryNote EntryKind = "note"
	// EntryAssistant is one reply of the model.
	EntryAssistant EntryKind = "assistant"
	// EntryResult is the result of one tool call.
	EntryResult EntryKind = "result"
	// EntrySummary is a summary that stands for everything before it.
	EntrySummary EntryKind = "summary"
	// EntryNotice is a line for the person. The model never reads it.
	EntryNotice EntryKind = "notice"
	// EntryTurn marks the end of a turn and says how it ended.
	EntryTurn EntryKind = "turn"
	// EntryQueued records a prompt that was accepted but could not go out
	// at once. It is settled by a later EntryPrompt with the same ID (it
	// went out) or by an EntryUnqueued that names it (it came back).
	EntryQueued EntryKind = "queued"
	// EntryUnqueued records that the queued prompt named by Prompt will
	// not go out: the person took it back, or it was dropped.
	EntryUnqueued EntryKind = "unqueued"
)

// Entry is one thing the loop appends to the log. Which fields are set
// depends on Kind.
type Entry struct {
	// Kind says what the entry holds.
	Kind EntryKind
	// ID names the entry. For an EntryPrompt and an EntryQueued it is the
	// prompt's id.
	ID string
	// Time is when the entry was made.
	Time time.Time
	// Turn is the id of the turn the entry belongs to, or empty for what
	// is stored between turns, such as a summary the person asked for.
	// The loop reads it back to tell whether a killed process left a turn
	// unfinished.
	Turn string

	// Blocks is the content of an EntryPrompt, EntryNote, EntrySummary or
	// EntryQueued: text and image blocks, exactly as the model is to read
	// them.
	Blocks []core.Block
	// Hidden marks an EntryPrompt or EntryQueued that gnosh sent for
	// itself. It is not shown as something the person typed and is not
	// offered by Up-arrow history.
	Hidden bool
	// Source says who wrote an EntryNote, an EntryNotice or a hidden
	// EntryPrompt: "nudge:empty", "driver:stop-hook", "mem-line".
	Source string
	// Prompt is, for an EntryNote, the id of the prompt it rides on, and
	// for an EntryUnqueued the id of the prompt that will not go out.
	Prompt string

	// Message is an EntryAssistant's reply.
	Message core.Message
	// Model and Provider say who wrote an EntryAssistant.
	Model    string
	Provider string
	// Usage is the token count the provider reported for the request.
	Usage core.Usage
	// StopReason is why an EntryAssistant's reply ended. A reply the model
	// finished carries what the provider said. A reply that was cut short
	// carries core.StopCancelled when the person stopped the turn, and
	// core.StopError when the provider failed and the turn ended on that;
	// its Message is then what had arrived, which may be nothing at all.
	// Either of the two ends the turn.
	StopReason core.StopReason
	// Silent marks an EntryAssistant that holds no text and no tool call
	// (reasoning only, or nothing at all). It is kept for the record and
	// is not part of the view.
	Silent bool
	// BadInputs holds, by tool call id, the argument text of every call of
	// an EntryAssistant whose arguments are not a JSON object: what
	// arrived of a call that was cut off. Message carries "{}" for them.
	BadInputs map[string]string

	// ToolUseID and ToolName say which call an EntryResult answers.
	ToolUseID string
	ToolName  string
	// Result is an EntryResult's result, with its display and metadata.
	Result core.ToolResult
	// Input is the arguments an EntryResult's call ran with, when the gate
	// changed them. It is nil when the call ran as the model wrote it.
	Input json.RawMessage
	// Denied marks an EntryResult whose call the gate refused.
	Denied bool
	// Interrupted marks an EntryResult whose call was stopped, or never
	// started, because the turn ended first.
	Interrupted bool

	// Duration is how long an EntryAssistant's request or an EntryResult's
	// call took.
	Duration time.Duration

	// Level and Text are an EntryNotice's weight and words. Text is also
	// the detail of an EntryTurn, and says why for an EntryUnqueued.
	Level Level
	Text  string

	// Reason is how an EntryTurn's turn ended.
	Reason EndReason

	// Trigger is "auto" or "manual" for an EntrySummary, and PreTokens is
	// about how long the conversation was before it.
	Trigger   string
	PreTokens int
}

// Gate answers one question: may this tool call run? Behind it are the
// permission rules, the hooks that guard tool calls, and asking a person.
type Gate interface {
	// Check decides about one call. It may take as long as a person takes
	// to answer, and it returns promptly when ctx is cancelled. An error
	// means the check could not be made; the call then does not run and
	// the model is told why.
	Check(ctx context.Context, req GateRequest) (Decision, error)
}

// GateFunc makes a Gate of a function.
type GateFunc func(ctx context.Context, req GateRequest) (Decision, error)

// Check calls f.
func (f GateFunc) Check(ctx context.Context, req GateRequest) (Decision, error) {
	return f(ctx, req)
}

// AllowAll is the Gate of a session that may act without asking.
var AllowAll Gate = GateFunc(func(context.Context, GateRequest) (Decision, error) {
	return Decision{Allow: true}, nil
})

// GateRequest is what a Gate is asked about.
type GateRequest struct {
	// Call is the call, with the arguments the tool would be given: what
	// the model wrote, through Options.CleanInput.
	Call core.ToolCall
	// Tool is the tool the call names.
	Tool core.Tool
	// Action is the tool's own account of what the call would do. A tool
	// that is not a core.Describer gives core.ActionOther and nothing
	// else.
	Action core.Action
	// CannotRun, when not nil, says the tool found that the call cannot
	// run as asked: its arguments are not usable, or the file was not
	// read first. The call will not run whatever the gate says. The gate
	// is still asked, about what the call was aimed at, so that a refusal
	// reaches the model in place of the tool's own error and a refused
	// path does not give away whether it exists. A gate answers such a
	// request from its rules and hooks alone: Ask returns ErrCannotRun.
	CannotRun error
	// Env is what the tool would be given to work with.
	Env *core.ToolEnv
	// Session is the session the person is in, AgentID and AgentType name
	// the sub-agent that makes the call (both empty for the main agent),
	// and ParentCall is the call that started that sub-agent.
	Session    string
	AgentID    string
	AgentType  string
	ParentCall string
	// Ask puts a question to the person and waits for the answer. It
	// returns ErrNobodyToAsk at once in a run that nobody attends.
	Ask func(ctx context.Context, q Question) (Answer, error)
}

// Decision is a Gate's answer.
type Decision struct {
	// Allow lets the call run.
	Allow bool
	// Input, when not nil, replaces the call's arguments. It must be a
	// JSON object.
	Input json.RawMessage
	// Reason is what the model reads when the call is refused.
	Reason string
	// Halt ends the turn with the refusal: a person who says no without a
	// comment wants the work to stop, not another way round.
	Halt bool
	// Unattended marks a refusal made only because nobody could be asked.
	Unattended bool
	// Context is what the model reads about the call beside its result,
	// whatever was decided: a PreToolUse hook's added text.
	Context []Part
}

// Concurrent is a Tool that can say whether one of its calls may run beside
// other calls. A tool that is not a Concurrent runs alone: every call the
// model wrote before it has finished and is stored before it starts, and
// nothing starts until it is done.
type Concurrent interface {
	// Parallel reports whether call only looks at things, so that it may
	// run at the same time as other calls that only look.
	Parallel(call core.ToolCall) bool
}

// MetaHalt is the key of core.ToolResult.Meta by which a tool says that the
// turn must end once the round's results are stored. Its value is the
// reason, in plain words, for the person. Only the harness's own tools set
// it; an adapter for an outside tool must never copy it from what that tool
// sent.
const MetaHalt = "gnosh.halt"

// StopWork is the error a moment returns to stop the work it stands before,
// as a hook's "continue": false does: the session's start refuses the
// prompt it stands before and every one after it (a sub-agent's start, its
// task), and the end of a summary made in a turn ends that turn. Reason is
// what the person and the model are told.
type StopWork struct{ Reason string }

func (h *StopWork) Error() string { return h.Reason }

// Part is a piece of hidden text that rides on a prompt or is put between
// two rounds. It is stored exactly as given, so its author marks it the way
// the harness marks its own words.
type Part struct {
	// Source names who wrote it.
	Source string
	// Text is what the model reads.
	Text string
	// Images go with Text, as image blocks: a picture an @ mention of the
	// prompt brought. (Only a prompt's lines carry them.)
	Images []core.Block
	// Before puts a prompt's part ahead of the prompt's own text. It is
	// for what belongs at the very start of a conversation, such as the
	// working directory and the date.
	Before bool
}

// Moments are the points at which other code is consulted or told. Every
// field may be nil. Each function is given a context that is cancelled when
// the person stops the turn, and must return promptly then.
type Moments struct {
	// Busy is called when a prompt makes an idle session busy: before the
	// first turn of a busy stretch, once the prompt is through its check.
	// What it returns is called when the session goes idle again, after
	// every prompt queued meanwhile has had its turn. The pair is called
	// exactly once per busy stretch. A stretch in which no turn runs (a
	// prompt its hook refused, a summary the person asked for) does not
	// call it. Its context is the first turn's: stopping that turn ends
	// the wait, and the prompt is kept as a turn that was stopped.
	Busy func(ctx context.Context) (release func())

	// SessionStart is called once, before the first prompt this process
	// sends to the session, and before that prompt's PromptSubmit. The
	// parts it returns go out with the first prompt that is sent, right
	// after its text; they are neither lost nor sent twice when that first
	// prompt is refused. An error is a warning and the prompt goes ahead.
	SessionStart func(ctx context.Context, info StartInfo) ([]Part, error)

	// PromptSubmit is called once for every prompt a person sends, when it
	// is sent, also while a turn runs. It may refuse the prompt, which
	// then comes back to the person with the reason, and it may add parts,
	// which are kept with the prompt. An error refuses the prompt: a check
	// that could not run has not passed. It is not called for prompts
	// gnosh sends for itself.
	PromptSubmit func(ctx context.Context, p Prompt) (Admission, error)

	// PromptLines are asked, in order, for the hidden lines of a prompt at
	// the moment it goes out, whether it starts a turn or is folded into a
	// running one. Their parts follow the prompt's text, after what
	// SessionStart and PromptSubmit added, in the order of this list. They
	// are asked for every prompt that was submitted, the hidden ones other
	// code submits included (LineInfo.Prompt.Hidden tells them apart), and
	// not for what the loop itself says in the middle of a turn: a nudge,
	// a driver's objection. An error is a warning and the prompt goes out
	// without that provider's lines.
	PromptLines []func(ctx context.Context, info LineInfo) ([]Part, error)

	// MoreRounds, when set, is asked after a round's tool calls, before
	// what the person queued meanwhile is handed to the turn and the model
	// asked again, with the number of requests the turn has made: false
	// ends the turn there, halted, with why as its detail, and what was
	// queued goes out as a turn of its own. (The cap on a run's replies,
	// --max-turns: one that stopped the next request instead had the
	// queued words handed to a turn that never answered them.)
	MoreRounds func(turn string, rounds int) (more bool, why string)
	// BeforeRequest are asked, in order, before every request of a turn
	// for reminders to put in front of the model. An error is a warning.
	BeforeRequest []func(ctx context.Context, info RequestInfo) ([]Part, error)

	// AfterTool is called after every tool call that ran, failed ones
	// included, on the goroutine the call ran on. It may add context for
	// the model, which is stored after the round's results, and it may end
	// the turn. An error is told to the model as feedback.
	AfterTool func(ctx context.Context, done ToolDone) (ToolVerdict, error)

	// AfterRound is called once after every tool call of a reply has
	// finished and its result is stored, when the model is about to be
	// asked again: not when the round ends the turn (a halt, a stop). It
	// may add context for the model, stored after the round's results, and
	// it may end the turn before the next request. An error ends the turn
	// too: a check that could not run has not passed.
	AfterRound func(ctx context.Context, done RoundDone) (ToolVerdict, error)

	// BeforeSummary is called before a summary is asked for. What it
	// returns is added to the instructions for the summary. It cannot stop
	// the summary; an error is a warning.
	BeforeSummary func(ctx context.Context, info SummaryInfo) (string, error)

	// AfterSummary is called once a summary is stored, for what the
	// session is to be given back: the files it had read, the work open in
	// mem, what session-start hooks print after a summary. An error is a
	// warning.
	AfterSummary func(ctx context.Context, info SummaryInfo) ([]Part, error)

	// TurnFailed is told when a turn ends failed: the provider stayed
	// gone or refused the key or the request, the conversation could not
	// be made to fit the model, or the harness itself failed. ErrorKind
	// in the result tells them apart. The drivers are not asked about
	// such a turn. It is called before TurnEnded.
	TurnFailed func(ctx context.Context, res TurnResult)

	// TurnEnded is told how every turn ended, after its last entry is
	// stored.
	TurnEnded func(ctx context.Context, res TurnResult)

	// SessionEnd is told that the session is closing, if it ever had a
	// message.
	SessionEnd func(ctx context.Context, info EndInfo)
}

// Fires lists the moments this loop fires, by the names the hook events go
// by, so that the wiring can refuse at startup a hook that was configured
// for a moment nobody fires. Each is reached through one port:
//
//	PreToolUse          Gate.Check, for every tool call, a sub-agent's too
//	PermissionRequest   GateRequest.Ask, when a person is about to be asked
//	PostToolUse         Moments.AfterTool, for a call whose result is no error
//	PostToolUseFailure  Moments.AfterTool, for a call whose result is one
//	UserPromptSubmit    Moments.PromptSubmit
//	SessionStart        Moments.SessionStart (Options.StartSource names the
//	                    source), and Moments.AfterSummary for "compact"
//	Stop                Options.Drivers
//	SubagentStart       the Moments.SessionStart of a sub-agent
//	SubagentStop        the Drivers of a sub-agent
//	StopFailure         Moments.TurnFailed
//	PreCompact          Moments.BeforeSummary
//	PostCompact         Moments.AfterSummary
//	SessionEnd          Moments.SessionEnd
//	PostToolBatch       Moments.AfterRound
//
// The loop fires nothing for Notification (a front end times it from
// QuestionEvent and IdleEvent) or for InstructionsLoaded.
func Fires() []string {
	return []string{
		"PreToolUse", "PermissionRequest", "PostToolUse", "PostToolUseFailure",
		"UserPromptSubmit", "SessionStart", "Stop", "SubagentStart", "SubagentStop",
		"StopFailure", "PreCompact", "PostCompact", "SessionEnd", "PostToolBatch",
	}
}

// StartInfo is what SessionStart is told.
type StartInfo struct {
	// Source is how the session came to be: "startup", "resume", "clear",
	// "fork", as the wiring said in Options.StartSource.
	Source string
	// Model is the model's id.
	Model string
	// Empty says the log held no conversation yet.
	Empty bool
}

// Admission is PromptSubmit's answer.
type Admission struct {
	// Refused sends the prompt back to the person. Nothing is stored and
	// no model is called.
	Refused bool
	// Reason says why, for the person.
	Reason string
	// Parts are kept with the prompt and go out after its text.
	Parts []Part
}

// LineInfo is what a PromptLines provider is told.
type LineInfo struct {
	// Prompt is the prompt that is going out.
	Prompt Prompt
	// ID is the prompt's id.
	ID string
	// First says no prompt has gone out in this session before, in this
	// process or an earlier one.
	First bool
	// Folded says the prompt joins a turn that is already running.
	Folded bool
	// Turn is the id of the turn it starts or joins.
	Turn string
}

// RequestInfo is what a BeforeRequest function is told.
type RequestInfo struct {
	// Turn is the turn's id and Round counts its requests from one.
	Turn  string
	Round int
	// Used and Point are the size of the conversation at the last count
	// and the point at which room is made, both in tokens.
	Used  int
	Point int
}

// RoundDone is what AfterRound is told: the tool calls of one reply, in
// the order the model wrote them, each with what it returned, and where
// they ran.
type RoundDone struct {
	Calls      []ToolDone
	Turn       string
	Session    string
	AgentID    string
	AgentType  string
	ParentCall string
}

// ToolDone is what AfterTool is told about a call.
type ToolDone struct {
	// Call is the call as it ran, with the arguments the gate left it.
	Call core.ToolCall
	// Tool is the tool that ran it, for a moment that asks what the call
	// did (core.Describer).
	Tool core.Tool
	// Result is what the tool returned, after Options.CleanResult.
	Result core.ToolResult
	// Duration is how long the call took.
	Duration time.Duration
	// Interrupted says the call was cut short because the turn was
	// stopped.
	Interrupted bool
	// Turn, Session, AgentID, AgentType and ParentCall place the call.
	Turn       string
	Session    string
	AgentID    string
	AgentType  string
	ParentCall string
}

// ToolVerdict is AfterTool's answer.
type ToolVerdict struct {
	// Context is stored after the round's results for the model to read.
	Context []Part
	// Replace, when not nil, is what the model reads in place of what the
	// tool returned (a PostToolUse hook's "updatedToolOutput"). The call
	// has run all the same.
	Replace *core.ToolResult
	// Halt ends the turn once the round's results are stored, and Reason
	// says why, for the person.
	Halt   bool
	Reason string
}

// SummaryInfo is what BeforeSummary and AfterSummary are told.
type SummaryInfo struct {
	// Trigger is "auto" or "manual".
	Trigger string
	// Instructions is what the person typed after /compact.
	Instructions string
	// Summary is the summary's text. It is empty before the summary.
	Summary string
	// PreTokens is about how long the conversation was before it.
	PreTokens int
	// Window is the model's window as the loop believes it: what is given
	// back after the summary keeps to a small share of it.
	Window int
}

// EndInfo is what SessionEnd is told.
type EndInfo struct {
	// Reason is the word given to End: "clear", "other",
	// "prompt_input_exit".
	Reason string
}

// StopMarker is the line a model ends its reply with to stop at a
// checkpoint of a Driver that names no marker of its own.
const StopMarker = "<!-- GNOSH_STOP -->"

// Driver is something that may send the model back to work when a turn is
// about to end: a refused close in mem, a goal that is not met, the
// person's Stop hooks. Drivers are asked in the order of Options.Drivers,
// and the first that sends the model back is the only one asked that time.
//
// No count ever ends the work. Instead, every Every-th time in a row that
// a driver sends the model back, what the model is sent is a checkpoint:
// it is asked to decide first, honestly, whether there is a real reason to
// stop, and to end its reply with Marker on a line of its own if there is.
// The marker counts only in the reply to a checkpoint. If the model stops
// that way, the person is shown its reason and what the driver last
// objected to.
type Driver struct {
	// Name identifies the driver in events and in the log.
	Name string
	// Every is how many times in a row the driver sends the model back
	// between checkpoints. Zero means ten.
	Every int
	// Marker is the line that stops the turn at a checkpoint. Empty means
	// StopMarker.
	Marker string
	// Decide says what happens now that the model has answered without
	// asking for a tool. An error means the driver could not decide; the
	// model is then sent back, because a check that could not run has not
	// passed, and the checkpoint is the way out.
	Decide func(ctx context.Context, end TurnEnd) (Verdict, error)
}

// TurnEnd is what a Driver is told.
type TurnEnd struct {
	// Turn is the turn's id.
	Turn string
	// Reply is the reply the turn would end on, and Text is its text.
	Reply core.Message
	Text  string
	// Streak is how many times in a row this driver has already sent the
	// model back in this turn. A Stop hook's stop_hook_active is
	// Streak > 0.
	Streak int
	// SentBack says some driver has already sent the model back in this
	// turn.
	SentBack bool
	// Checkpoint says that if the driver sends the model back now, the
	// model is sent a checkpoint. The driver may put the question in
	// Verdict.Checkpoint; otherwise the loop asks it in its own words and
	// quotes Verdict.Objection.
	Checkpoint bool
	// Marker is the line that stops the turn at this driver's checkpoint.
	Marker string
}

// Action is what a Driver wants.
type Action int

const (
	// End lets the turn end, as far as this driver is concerned.
	End Action = iota
	// Continue sends the model back to work with Verdict.Prompt.
	Continue
	// Halt ends the turn now, whatever a later driver would say.
	Halt
)

// Verdict is a Driver's answer.
type Verdict struct {
	// Action is what the driver wants: End, Continue or Halt.
	Action Action
	// Prompt is the hidden prompt the model reads next, for Continue. It
	// is stored exactly as given.
	Prompt string
	// Checkpoint, when not empty, is sent in place of Prompt when this
	// send-back falls on a checkpoint.
	Checkpoint string
	// Objection says in a line what holds the turn open, for the person.
	// When the model stops at a checkpoint it is quoted beside the model's
	// reason. Empty means Prompt.
	Objection string
	// Notice, when not empty, is a line for the person, whatever the
	// action: "the goal is met", "a hook ended the turn".
	Notice string
}

// Clock is the time as the loop reads it.
type Clock interface {
	// Now is the present moment.
	Now() time.Time
	// After returns a channel that is sent the time once d has passed.
	After(d time.Duration) <-chan time.Time
}

// systemClock is the machine's own clock.
type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
