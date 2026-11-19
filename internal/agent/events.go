package agent

import (
	"encoding/json"
	"time"

	"github.com/gnosh-ai/gnosh/internal/core"
)

// Event is one thing a front end is told. Every event is a pointer to one
// of the types below, each of which embeds Meta. A consumer switches on the
// type:
//
//	switch ev := ev.(type) {
//	case *agent.TextEvent:
//	case *agent.ToolEndEvent:
//	}
type Event interface {
	// EventMeta returns what every event carries.
	EventMeta() *Meta
}

// Meta is what every event carries.
type Meta struct {
	// Seq numbers the events of a stream from one, without gaps, so that a
	// consumer can tell that it has missed none.
	Seq uint64
	// Time is when the event was made.
	Time time.Time
	// Session is the session the person is in.
	Session string
	// Agent is empty for the main agent's events and the sub-agent's id
	// for a sub-agent's. Parent is then the id of the tool call that
	// started the sub-agent.
	Agent  string
	Parent string
	// Turn is the id of the turn the event belongs to, or empty.
	Turn string
}

// EventMeta returns m.
func (m *Meta) EventMeta() *Meta { return m }

// Level is the weight of a notice.
type Level string

const (
	// LevelInfo is something worth a quiet line.
	LevelInfo Level = "info"
	// LevelWarning is something the person should read: a check that could
	// not run, the model choosing to stop. A run prints it on stderr.
	LevelWarning Level = "warning"
	// LevelError is a failure.
	LevelError Level = "error"
)

// EndReason says how a turn ended.
type EndReason string

const (
	// EndCompleted: the model answered and nothing sent it back.
	EndCompleted EndReason = "completed"
	// EndStopped: the person stopped the turn.
	EndStopped EndReason = "stopped"
	// EndHalted: something ended the turn for good: a permission the
	// person refused, a hook that ended the turn, a tool that said stop.
	EndHalted EndReason = "halted"
	// EndFailed: the provider failed and stayed failed, the conversation
	// could not be made to fit the model, or the log could not be written.
	EndFailed EndReason = "failed"
	// EndModelStopped: at a checkpoint the model judged that there is a
	// real reason to stop, and said so.
	EndModelStopped EndReason = "model_stopped"
	// EndRefused: the prompt never went out, because a hook refused it.
	// No turn ran.
	EndRefused EndReason = "refused"
	// EndWithdrawn: the prompt never went out, because it was taken back
	// or dropped before its turn. No turn ran.
	EndWithdrawn EndReason = "withdrawn"
	// EndInterrupted: the process was killed in the middle of the turn.
	// It is only ever written to the log, by the next process to open the
	// session.
	EndInterrupted EndReason = "interrupted"
)

// Denial records a tool call the gate refused.
type Denial struct {
	// ToolName, ToolUseID and Input say which call.
	ToolName  string
	ToolUseID string
	Input     json.RawMessage
	// Reason is what the model was told.
	Reason string
	// Unattended says the call was refused only because nobody could be
	// asked.
	Unattended bool
}

// TurnResult is how a turn ended.
type TurnResult struct {
	// Turn is the turn's id, and Prompts the ids of the prompts that
	// started it. Turn is empty when no turn ran (EndRefused,
	// EndWithdrawn).
	Turn    string
	Prompts []string
	// Reason says how it ended.
	Reason EndReason
	// Text is the answer: the text of the last reply, without the stop
	// marker when the model stopped at a checkpoint.
	Text string
	// Detail says more, in plain words: who halted the turn and why, why
	// a prompt was refused, the model's reason for stopping.
	Detail string
	// Objection is, for EndModelStopped, what the driver last objected to.
	Objection string
	// Driver is the driver that halted the turn or at whose checkpoint the
	// model stopped.
	Driver string
	// Err is the failure of an EndFailed turn, in plain words.
	Err error
	// ErrorKind sorts that failure: "rate_limit", "overloaded",
	// "authentication_failed", "invalid_request", "server_error",
	// "context_too_long", "internal".
	ErrorKind string
	// Status is the HTTP status of the failure, when there was one.
	Status int
	// StopReason is why the model stopped writing its last reply.
	StopReason core.StopReason
	// Rounds counts the replies of the model in this turn.
	Rounds int
	// Usage adds up the token counts of the turn's own requests.
	Usage core.Usage
	// Duration is how long the turn took, and APIDuration how much of that
	// was spent waiting on the model.
	Duration    time.Duration
	APIDuration time.Duration
	// Denials lists the tool calls the gate refused.
	Denials []Denial
	// Started and Ended are when the turn began and ended.
	Started time.Time
	Ended   time.Time
}

// Prompt is something sent to the session for the model to answer.
type Prompt struct {
	// ID names the prompt. Empty lets the loop make one.
	ID string
	// Text is the words.
	Text string
	// Attachments are image blocks, and text blocks holding the text of
	// attached or pasted files. They follow Text in the same message.
	Attachments []core.Block
	// Hidden marks a prompt gnosh sends for itself: the model reads it,
	// but it is not shown as something the person typed, PromptSubmit is
	// not asked about it, and stopping the turn drops it from the queue.
	// Its Text is stored as given.
	Hidden bool
	// Source says who wrote a hidden prompt: "task-notification", "goal".
	Source string
	// Notice is one quiet line shown to the person when a hidden prompt
	// goes out, so that they need not wonder why a turn started.
	Notice string
	// Later holds a prompt sent while a turn runs until that turn has
	// ended, instead of handing it to the model between two rounds.
	Later bool
	// Own marks a prompt gnosh sends for itself (/loop's next prompt, the
	// "go on" after the session moved): stopping a turn drops it, or the
	// work would start again. A hidden prompt the person caused (a
	// !command's output, a skill, /review) is theirs and stays in line;
	// stopping dropped every hidden prompt, and a !command's output never
	// reached the model (the agent loop review of 2026-11-19).
	Own bool
	// Meta travels with the prompt into its events.
	Meta map[string]any
}

// blank reports whether p holds nothing to send.
func (p Prompt) blank() bool {
	if len(p.Attachments) > 0 {
		return false
	}
	for _, r := range p.Text {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}

// blocks returns p as the blocks of a message. Text that is only blanks
// is left out beside attachments, because no message may hold a blank
// block.
func (p Prompt) blocks() []core.Block {
	out := make([]core.Block, 0, 1+len(p.Attachments))
	if !(Prompt{Text: p.Text}).blank() || len(p.Attachments) == 0 {
		out = append(out, core.Text(p.Text))
	}
	return append(out, p.Attachments...)
}

// Note is something the harness tells the model that is not a prompt: a
// background command has ended, a file changed on disk. See Agent.Tell.
type Note struct {
	// Text is what the model reads. It is stored as given, so its author
	// marks it the way the harness marks its own words.
	Text string
	// Source says who wrote it: "task-notification".
	Source string
	// Notice is one quiet line shown to the person when the note goes
	// out.
	Notice string
	// Wake makes the note start a turn of its own in an idle session, so
	// that the model reacts to it at once. Without it a note waits for
	// the next request.
	Wake bool
	// recorded marks a note Record put in while a turn ran: when no
	// request takes it, it is written to the log as the turn ends.
	recorded bool
}

// QueuedPrompt is a prompt that waits for its turn.
type QueuedPrompt struct {
	// ID is the prompt's id and Prompt the prompt as it was sent.
	ID     string
	Prompt Prompt
	// Checking says the prompt's PromptSubmit moment has not finished.
	Checking bool
}

// Question is something put to the person.
type Question struct {
	// ID names the question. The loop sets it.
	ID string
	// Kind says what sort of question it is, for the front end:
	// "permission", "question".
	Kind string
	// Title is a short heading and Text the question in full.
	Title string
	Text  string
	// Choices are the answers offered, if any.
	Choices []Choice
	// CallID and Tool name the tool call the question is about, and Action
	// says what that call would do.
	CallID string
	Tool   string
	Action *core.Action
	// Data is anything else the asker and the front end have agreed on.
	Data any
}

// Choice is one answer a Question offers.
type Choice struct {
	// Key is what Answer.Choice carries when this one is picked.
	Key string
	// Label is what the person reads.
	Label string
}

// Answer is the person's answer to a Question.
type Answer struct {
	// Choice is the Key of the choice picked, or empty.
	Choice string
	// Text is a comment or a free answer.
	Text string
	// Data is anything else the asker and the front end have agreed on.
	Data any
	// By names who gave the answer when it was not the person at the
	// front end: "the channel hook", for a channel that relays permission
	// questions. "" is the person.
	By string
}

// BusyEvent says a prompt has made the session busy.
type BusyEvent struct{ Meta }

// IdleEvent says the session has nothing left to do.
type IdleEvent struct{ Meta }

// QueueEvent gives the prompts that wait for their turn, each time that
// list changes.
type QueueEvent struct {
	Meta
	// Queued are the prompts that wait, in the order they will go out.
	Queued []QueuedPrompt
}

// ReturnReason says why a prompt came back instead of going out.
type ReturnReason string

const (
	// ReturnRefused: PromptSubmit refused it.
	ReturnRefused ReturnReason = "refused"
	// ReturnStopped: the person stopped the session while the prompt was
	// still being checked, or it was one of gnosh's own and stopping
	// dropped it.
	ReturnStopped ReturnReason = "stopped"
	// ReturnClosed: the session was closed before the prompt went out.
	ReturnClosed ReturnReason = "closed"
	// ReturnUnsaved: the prompt could not be written to the log.
	ReturnUnsaved ReturnReason = "unsaved"
	// ReturnRestart: an earlier process accepted the prompt and died
	// before sending it.
	ReturnRestart ReturnReason = "restart"
)

// PromptReturnedEvent says a prompt will not go out. A front end puts a
// typed one back into the input box, with its attachments and the reason.
type PromptReturnedEvent struct {
	Meta
	// ID is the prompt's id and Prompt the prompt as it was sent.
	ID     string
	Prompt Prompt
	// Why sorts the reason, and Reason says it in plain words.
	Why    ReturnReason
	Reason string
}

// TurnStartEvent says a turn has begun.
type TurnStartEvent struct {
	Meta
	// Prompts are the ids of the prompts that start it.
	Prompts []string
}

// PromptEvent says a prompt has gone out to the model, at the start of a
// turn or folded into a running one.
type PromptEvent struct {
	Meta
	// ID is the prompt's id and Prompt the prompt. One that gnosh sent for
	// itself has Hidden set, and is not shown as something the person
	// typed.
	ID     string
	Prompt Prompt
	// Folded says it joined a running turn.
	Folded bool
	// Parts are the hidden lines that went out with it.
	Parts []Part
}

// RequestEvent says a request is on its way to a model.
type RequestEvent struct {
	Meta
	// Purpose is "main", "summary" or the purpose of a side request.
	Purpose string
	// Round counts the turn's main requests from one.
	Round int
	// Attempt counts the tries of this request from one.
	Attempt int
	// Provider and Model say whom the request goes to.
	Provider string
	Model    string
	// Messages is how many messages the request holds, and Estimate about
	// how many tokens it is.
	Messages int
	Estimate int
}

// TextEvent carries the next piece of the answer.
type TextEvent struct {
	Meta
	// Text is the piece.
	Text string
}

// ThinkingEvent carries the next piece of the model's reasoning.
type ThinkingEvent struct {
	Meta
	// Text is the piece.
	Text string
}

// ToolCallEvent says the model has begun to write a tool call.
type ToolCallEvent struct {
	Meta
	// CallID is the call's id and Name the tool it asks for.
	CallID string
	Name   string
}

// ToolInputEvent carries the next piece of a tool call's arguments, as the
// model writes them.
type ToolInputEvent struct {
	Meta
	// CallID is the call's id and Text the piece of its arguments.
	CallID string
	Text   string
}

// ReplyEvent says a reply has ended, and carries all of it.
type ReplyEvent struct {
	Meta
	// Purpose is "main" or "summary", and Round counts the turn's main
	// requests from one.
	Purpose string
	Round   int
	// Message is the whole reply, and StopReason why it ended. A reply
	// that was cut short holds what had arrived, which may be nothing,
	// and its StopReason is core.StopCancelled (the person stopped the
	// turn) or core.StopError (the provider failed).
	Message    core.Message
	StopReason core.StopReason
	// Silent says the reply holds no text and no tool call.
	Silent bool
	// BadInputs holds the argument text of calls whose arguments are not a
	// JSON object.
	BadInputs map[string]string
	// Marker is the stop marker the reply ended on, when it counted.
	Marker string
	// Provider and Model say who wrote the reply.
	Provider string
	Model    string
	// Usage is what the provider counted for the request.
	Usage core.Usage
	// Duration is how long the request took, and FirstPiece how long until
	// the first piece of the reply arrived.
	Duration   time.Duration
	FirstPiece time.Duration
}

// RetryEvent says a request failed and will be tried again after a wait.
type RetryEvent struct {
	Meta
	// Purpose is "main" or "summary".
	Purpose string
	// Attempt counts the failures since the provider last showed life.
	Attempt int
	// Wait is how long the loop waits before it tries again.
	Wait time.Duration
	// Until is when the loop stops waiting for the provider and ends the
	// turn. It is zero when there is no such moment.
	Until time.Time
	// Kind is the failure's kind and Status its HTTP status, if any.
	Kind   core.ErrorKind
	Status int
	// Message says, in plain words, what failed and what happens next.
	Message string
	// Discarded says a reply had begun to arrive and is thrown away: the
	// next try writes it afresh.
	Discarded bool
	// FellBack names the fallback model the turn goes on with, at once and
	// for the rest of the turn (Options.Fallbacks), or is empty when the
	// same model is asked again after Wait.
	FellBack string
}

// ToolStartEvent says a tool call is about to be put to the gate and run.
type ToolStartEvent struct {
	Meta
	// CallID is the call's id, Name the tool, and Input the arguments the
	// gate and the tool are given.
	CallID string
	Name   string
	Input  json.RawMessage
	// Action is the tool's own account of what the call will do.
	Action core.Action
	// Parallel says the call may run beside others.
	Parallel bool
}

// ToolProgressEvent carries live output of a running call.
type ToolProgressEvent struct {
	Meta
	// CallID is the id of the call the output belongs to.
	CallID string
	// Stream is "stdout", "stderr" or "status", and Text the output.
	Stream string
	Text   string
}

// ToolEndEvent says a tool call has its result.
type ToolEndEvent struct {
	Meta
	// CallID is the call's id and Name the tool.
	CallID string
	Name   string
	// Input is the arguments the call ran with.
	Input json.RawMessage
	// Result is what the model will read, with the display for the person,
	// and Duration how long the call took.
	Result   core.ToolResult
	Duration time.Duration
	// Denied says the gate refused the call. Unattended says it did so
	// only because nobody could be asked.
	Denied     bool
	Unattended bool
	// Interrupted says the call was stopped, or never started, because the
	// turn ended first.
	Interrupted bool
	// Ran says the tool was really started.
	Ran bool
}

// QuestionEvent puts a question to the person. The front end answers with
// Agent.Answer.
type QuestionEvent struct {
	Meta
	// Question is the question, with the id to answer it by.
	Question Question
}

// AnswerEvent says a question is over.
type AnswerEvent struct {
	Meta
	// ID is the question's id and Answer what the person said.
	ID     string
	Answer Answer
	// Withdrawn says the question was taken back unanswered, because the
	// turn was stopped.
	Withdrawn bool
}

// NoticeEvent is a line for the person.
type NoticeEvent struct {
	Meta
	// Level is the line's weight.
	Level Level
	// Source says where it comes from: "loop", "driver:stop-hook". Text is
	// the line.
	Source string
	Text   string
	// TurnEnd marks the notice that says how a turn ended, in the words
	// of the TurnEndEvent that comes right after it: a front end that
	// shows that event itself leaves the notice out.
	TurnEnd bool
}

// NoteEvent says a note of the harness's own was put in front of the
// model: see Agent.Tell.
type NoteEvent struct {
	Meta
	// Note is the note as it was handed to Tell.
	Note Note
}

// DriverEvent says what a turn-end driver did, when it did anything but
// let the turn end.
type DriverEvent struct {
	Meta
	// Name is the driver's name.
	Name string
	// Action is Continue or Halt.
	Action Action
	// Objection is what holds the turn open, or why it was halted.
	Objection string
	// Streak is how many times in a row the driver has now sent the model
	// back, and Checkpoint says this one asked the model to judge.
	Streak     int
	Checkpoint bool
}

// ContextEvent gives the size of the conversation after a count.
type ContextEvent struct {
	Meta
	// Context is the sizes as they stand.
	Context ContextStatus
}

// RoomEvent says the oldest part of the conversation left the model's view
// without a summary.
type RoomEvent struct {
	Meta
	// Freed is about how many tokens left.
	Freed int
	// Notice is the log owner's line for the person.
	Notice string
}

// SummaryEvent says a summary has begun or ended.
type SummaryEvent struct {
	Meta
	// Phase is "start" or "end".
	Phase string
	// Trigger is "auto" or "manual".
	Trigger string
	// PreTokens and PostTokens are about how long the conversation was
	// before and after.
	PreTokens  int
	PostTokens int
	// Err says why the summary could not be made. It is nil on success.
	Err error
}

// SideEvent says a side request has ended.
type SideEvent struct {
	Meta
	// Purpose is what the request was for.
	Purpose string
	// Provider and Model say who was asked, Usage what the provider
	// counted, and Duration how long it took.
	Provider string
	Model    string
	Usage    core.Usage
	Duration time.Duration
	// Fallback says the small model failed and the large one was asked.
	Fallback bool
	// Err is nil when an answer came back.
	Err error
}

// LoopEvent says /loop was switched on or off.
type LoopEvent struct {
	Meta
	// On says whether /loop is on now.
	On bool
	// Turns is how many turns the loop has started since it was switched
	// on.
	Turns int
	// Why says why it went off.
	Why string
}

// TurnEndEvent says a turn has ended, and how.
type TurnEndEvent struct {
	Meta
	// Result is how the turn ended, with its answer and what it cost.
	Result TurnResult
}

// LostEvent stands where pieces of live output were thrown away because the
// consumer of events fell too far behind. Only TextEvent, ThinkingEvent,
// ToolInputEvent and ToolProgressEvent are ever thrown away, and what they
// carried arrives whole in the ReplyEvent or ToolEndEvent that follows.
type LostEvent struct {
	Meta
	// Events is how many were thrown away and Bytes how much text they
	// held.
	Events int
	Bytes  int
}

// Phase says what a session is doing.
type Phase string

const (
	// PhaseIdle: nothing.
	PhaseIdle Phase = "idle"
	// PhaseStarting: a prompt is being taken in, or the session is
	// becoming busy.
	PhaseStarting Phase = "starting"
	// PhaseRoom: making the conversation fit the model.
	PhaseRoom Phase = "making-room"
	// PhaseSummary: a summary is being written.
	PhaseSummary Phase = "summarizing"
	// PhaseRequest: a request is out and nothing has come back yet.
	PhaseRequest Phase = "waiting-for-model"
	// PhaseReply: a reply is arriving.
	PhaseReply Phase = "reading-reply"
	// PhaseRetry: waiting before a request is tried again.
	PhaseRetry Phase = "waiting-to-retry"
	// PhaseTools: tool calls are running.
	PhaseTools Phase = "running-tools"
	// PhaseQuestion: waiting for the person to answer.
	PhaseQuestion Phase = "waiting-for-person"
	// PhaseEnding: asking the turn-end drivers.
	PhaseEnding Phase = "turn-end-checks"
)

// RunningCall is a tool call in flight.
type RunningCall struct {
	// CallID is the call's id, Name the tool, and Since when it started.
	CallID string
	Name   string
	Since  time.Time
}

// State is what a session is doing right now.
type State struct {
	// Busy says the session has work in hand or waiting.
	Busy bool
	// Phase says what it is doing and Waiting says, in plain words, what
	// it is waiting for. Since is when that began.
	Phase   Phase
	Waiting string
	Since   time.Time
	// Turn is the running turn's id and Round its present round.
	Turn  string
	Round int
	// Running are the tool calls in flight.
	Running []RunningCall
	// Queued is how many prompts wait for their turn, and Notes how many
	// notes wait to be told to the model.
	Queued int
	Notes  int
	// Loop says /loop is on.
	Loop bool
	// Model and Level are the model main requests go to and its thinking
	// level.
	Model string
	Level string
}

// ContextStatus is the size of the conversation against the model's
// window, in tokens.
type ContextStatus struct {
	// Window is the model's window as the loop believes it, and Known says
	// whether it was learned or assumed.
	Window int
	Known  bool
	// Point is where room is made, and Limit the size past which a request
	// is never sent.
	Point int
	Limit int
	// Used is the size of the conversation at the last count.
	Used int
	// AutoOff says automatic summaries have been stopped for this session
	// because one did not help.
	AutoOff bool
}

// Room is how many tokens are left before room has to be made.
func (c ContextStatus) Room() int {
	if c.Used >= c.Point {
		return 0
	}
	return c.Point - c.Used
}
