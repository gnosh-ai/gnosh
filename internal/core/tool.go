package core

import (
	"context"
	"encoding/json"
	"sync"
)

// ToolCall is a model's request to run a tool.
type ToolCall struct {
	// ID is the id of the tool_use block that made the request.
	ID string
	// Name is the tool asked for.
	Name string
	// Input is the arguments: one JSON object.
	Input json.RawMessage
}

// ToolResult is what running a tool produced.
type ToolResult struct {
	// Content is what the model is shown: text and image blocks.
	Content []Block

	// IsError marks a failure the model should see and react to: a file
	// that is not there, a command that exited non-zero, arguments that
	// make no sense.
	IsError bool

	// Display is a richer view for the person watching, such as the diff
	// an edit made. It is never sent to the model.
	Display *Display

	// Cwd, when not empty, is the session's working directory after the
	// call: a shell command that changed directory says so here.
	Cwd string

	// Meta holds facts about the call for hooks and the transcript: the
	// path written, the exit code. Values must be JSON-encodable.
	Meta map[string]any

	// Notes are short lines for the person, shown under the call, never
	// sent to the model: what came of the call besides its result (the
	// new problems a language server found after an edit).
	Notes []string
}

// TextResult makes a result that shows the model s.
func TextResult(s string) ToolResult {
	return ToolResult{Content: []Block{Text(s)}}
}

// ErrorResult makes a failed result that shows the model s.
func ErrorResult(s string) ToolResult {
	return ToolResult{Content: []Block{Text(s)}, IsError: true}
}

// Tool is something a model can call.
type Tool interface {
	// Spec is what the model is told about the tool.
	Spec() ToolSpec

	// Run carries out the call. A failure the model can do something about
	// comes back as a ToolResult with IsError set and a nil error. A
	// non-nil error means the harness itself could not run the tool; the
	// caller reports it and the model is told the call did not run.
	// Run must stop promptly when ctx is cancelled.
	Run(ctx context.Context, call ToolCall, env *ToolEnv) (ToolResult, error)
}

// ActionKind sorts what a tool call would do, for permission rules.
type ActionKind string

const (
	// ActionRead looks at files without changing anything.
	ActionRead ActionKind = "read"
	// ActionEdit creates, changes or deletes files.
	ActionEdit ActionKind = "edit"
	// ActionExecute runs a command.
	ActionExecute ActionKind = "execute"
	// ActionNetwork reaches out to a URL.
	ActionNetwork ActionKind = "network"
	// ActionOther is anything else.
	ActionOther ActionKind = "other"
)

// Action describes what a tool call would do, before it does it, so that
// permission rules can be matched against it and a person can be asked.
type Action struct {
	Kind ActionKind
	// Paths are the absolute paths a read or edit would touch.
	Paths []string
	// Command is the command line an execute would run.
	Command string
	// URL is where a network call would go.
	URL string
	// Summary is one plain line saying what would happen.
	Summary string
	// Preview shows the change in full, such as the diff an edit would
	// make. It may be nil.
	Preview *Display
	// Confirm says a person must be asked before this call runs, even
	// where the rules or the mode would let it run without asking: the
	// call throws away something that cannot be got back, such as a
	// machine's saved state. A deny rule still refuses it outright, and
	// where nobody can be asked it does not run.
	Confirm bool
}

// Describer is a Tool that can say what a call would do. A tool that does
// not implement it is treated as ActionOther with no details.
type Describer interface {
	// Describe reports what running call would do, without doing it. An
	// error means the call cannot run as asked (the arguments are not
	// usable, the file was not read first); it will fail the same way if
	// run. The Action returned beside an error still names what the call
	// was aimed at, as far as that could be worked out: its kind, paths,
	// command or URL. A caller checks its deny rules against that Action
	// before it tells the model about the error, so that a refused path
	// does not give away whether it exists.
	Describe(call ToolCall, env *ToolEnv) (Action, error)
}

// ToolEnv is what a tool is given to work with.
type ToolEnv struct {
	// Cwd is the session's working directory: an absolute path.
	Cwd string

	// Roots are the directories the session works in: Cwd's project and
	// any added to it. Tools use them to tell inside from outside.
	Roots []string

	// SessionID names the session the call belongs to. For a call a
	// sub-agent makes it is the sub-agent's own id, not the id of the
	// session that started it: a sub-agent is a session of its own.
	SessionID string

	// ParentSessionID is empty for a call of a session a person is in.
	// For a call a sub-agent makes it names the session that started the
	// sub-agent: the person's session, or, for a sub-agent started by a
	// sub-agent, that sub-agent's own id.
	ParentSessionID string

	// Store keeps a tool's own state for the length of a session, such as
	// which files the model has read. Tools key it by an unexported type
	// of their own. It is never nil.
	Store *sync.Map

	// Progress, when not nil, takes live output while a call runs. It may
	// be called from any goroutine and must not block.
	Progress func(Progress)

	// Tree, for a call of a session or sub-agent that works in a git work
	// tree apart from the person's checkout (--worktree, isolation
	// worktree, or its parent's), is that work tree; nil otherwise.
	Tree *WorkTree
}

// WorkTree is a git work tree an agent works in, apart from the person's
// own checkout.
type WorkTree struct {
	// Path is the work tree's folder, and Repo the top folder of the
	// checkout it was made from.
	Path, Repo string
	// Outer are the checkouts Repo was itself made from, when it is a
	// work tree too (a sub-agent's, made in a --worktree session's): the
	// innermost first, the person's own checkout last.
	Outer []string
}

// Checkouts are every checkout the work tree stays out of: Repo, then
// Outer.
func (t WorkTree) Checkouts() []string { return append([]string{t.Repo}, t.Outer...) }

// Progress is a piece of live output from a running tool call.
type Progress struct {
	// CallID is the ToolCall.ID the output belongs to.
	CallID string
	// Stream is "stdout", "stderr" or "status"; or StreamRunning, which is
	// no output.
	Stream string
	// Text is the output.
	Text string
}

// StreamRunning is the Progress stream that says what a running call runs
// just now, such as the programs a shell command has going, each time in
// place of what it said before. It is for the person; the model is not
// shown it.
const StreamRunning = "running"

// Display is something to show a person: more than the model needs.
type Display struct {
	// Kind is "diff", "command", "text" or "markdown".
	Kind string
	// Title is a short heading, such as a file name.
	Title string
	// Path is the file a diff is of.
	Path string
	// Before and After are a diff's two sides. A new file has an empty
	// Before.
	Before string
	After  string
	// Text is the body of every other kind.
	Text string
}
