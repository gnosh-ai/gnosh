package bridge

import (
	"encoding/json"

	"github.com/gnosh-ai/gnosh/internal/core"
	"github.com/gnosh-ai/gnosh/internal/transcript"
)

// Protocol is the version of spec/gnosh/bridge.md this package speaks.
const Protocol = 1

// MaxLine is the longest request line the bridge reads (bridge.md §3).
const MaxLine = 64 << 20

// The requests the bridge answers, in the order hello lists them.
const (
	OpHello = "hello"
	OpTools = "tools"
	OpCall  = "call"
	OpView  = "view"
)

// ops is what hello says the bridge answers.
var ops = []string{OpHello, OpTools, OpCall, OpView}

// The error codes of bridge.md §8.
const (
	CodeBadRequest  = "bad_request"
	CodeProtocol    = "protocol"
	CodeUnknownOp   = "unknown_op"
	CodeUnknownTool = "unknown_tool"
	CodeConfirm     = "confirm"
	CodeUnavailable = "unavailable"
	CodeInternal    = "internal"
)

// Request is one line a host sends. Which fields count depends on Op.
type Request struct {
	Op string          `json:"op"`
	ID json.RawMessage `json:"id,omitempty"`

	// hello
	Protocol int    `json:"protocol,omitempty"`
	Client   string `json:"client,omitempty"`

	// call
	Session   string          `json:"session,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Confirmed bool            `json:"confirmed,omitempty"`

	// view (Session too)
	Entries    []transcript.Entry `json:"entries,omitempty"`
	Budget     int                `json:"budget,omitempty"`
	Room       int                `json:"room,omitempty"`
	KeepRounds int                `json:"keep_rounds,omitempty"`
	Prompt     string             `json:"prompt,omitempty"`
}

// Reply is one line the bridge writes. Which fields are set depends on the
// request it answers.
type Reply struct {
	OK    bool            `json:"ok"`
	ID    json.RawMessage `json:"id,omitempty"`
	Error *Error          `json:"error,omitempty"`

	// hello
	Protocol int      `json:"protocol,omitempty"`
	Build    string   `json:"build,omitempty"`
	Version  string   `json:"version,omitempty"`
	Project  string   `json:"project,omitempty"`
	Ops      []string `json:"ops,omitempty"`

	// tools
	Tools []ToolInfo `json:"tools,omitempty"`

	// call
	Content []core.Block `json:"content,omitempty"`
	IsError bool         `json:"is_error,omitempty"`
	Frames  []Frame      `json:"frames,omitempty"`

	// view
	Messages   []core.Message     `json:"messages,omitempty"`
	Sources    [][]string         `json:"sources,omitempty"`
	Stack      []StackFrame       `json:"stack,omitempty"`
	Tokens     int                `json:"tokens,omitempty"`
	OverBudget bool               `json:"over_budget,omitempty"`
	NewSlides  []transcript.Entry `json:"new_slides,omitempty"`
}

// Error is what a reply with ok false says went wrong.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Summary is, for CodeConfirm, the one line saying what the call
	// would do.
	Summary string `json:"summary,omitempty"`
	// Protocols is, for CodeProtocol, the versions the bridge speaks.
	Protocols []int `json:"protocols,omitempty"`
}

// ToolInfo is one tool as tools lists it.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	// Confirm says some calls of the tool need a person's approval first.
	Confirm bool `json:"confirm,omitempty"`
}

// Frame is one frame a call pushed or popped, as mem kept it.
type Frame struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Purpose  string `json:"purpose,omitempty"`
	Result   string `json:"result,omitempty"`
	Thoughts int    `json:"thoughts,omitempty"`
	Session  string `json:"session"`
}

// StackFrame is one frame a session stands in, as view reports it.
type StackFrame struct {
	ID     string `json:"id"`
	Label  string `json:"label,omitempty"`
	CallID string `json:"call_id,omitempty"`
	Slid   bool   `json:"slid,omitempty"`
}
