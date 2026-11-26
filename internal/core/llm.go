package core

import (
	"context"
	"encoding/json"
	"iter"
)

// ToolSpec is what a model is told about a tool: its name, what it is for,
// and the JSON Schema of the arguments it takes.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// Thinking says how much a model should reason before it answers. The zero
// value leaves it to the model.
type Thinking struct {
	// Mode is "" (the model's default), "off" or "on".
	Mode string
	// BudgetTokens caps the reasoning, for providers that take a budget.
	// Zero means the provider's default.
	BudgetTokens int
	// BudgetNote is what a provider that can (llama.cpp's server) puts at
	// the end of the reasoning when BudgetTokens runs out, so the model
	// knows why its thinking ended. Empty means nothing is put there.
	BudgetNote string
	// Effort is "low", "medium" or "high", for providers that take a level.
	// Empty means the provider's default.
	Effort string
}

// Request is one call to a model.
type Request struct {
	// Model is the provider's name for the model.
	Model string

	// System is the system prompt, in parts ordered from the part that
	// changes least to the part that changes most. A provider with prompt
	// caching may cache up to a part boundary; others join the parts with
	// blank lines.
	System []string

	// Messages is the conversation so far. It starts with a user message,
	// roles alternate, no message or text block is empty, and every
	// tool_use block in an assistant message is answered by exactly one
	// tool_result block in the user message that follows, where the
	// tool_result blocks come before any other block.
	Messages []Message

	// Tools is what the model may call.
	Tools []ToolSpec

	// MaxTokens caps the answer. Zero means the provider's default.
	MaxTokens int

	// Temperature and TopP are left to the provider when nil.
	Temperature *float64
	TopP        *float64

	// Stop lists strings that end the answer when the model writes one.
	Stop []string

	Thinking Thinking

	// Extra holds provider-specific fields, merged into the request body as
	// they are. Fields set here win over the ones the provider would write.
	Extra map[string]any
}

// EventType says what a stream Event carries.
type EventType string

const (
	// EventTextDelta carries the next piece of the answer in Text.
	EventTextDelta EventType = "text_delta"
	// EventThinkingDelta carries the next piece of reasoning in Text.
	EventThinkingDelta EventType = "thinking_delta"
	// EventThinkingSignature closes the reasoning that came before it and
	// carries its Signature, its Opaque payload, or both.
	EventThinkingSignature EventType = "thinking_signature"
	// EventToolUseStart opens a tool call: ID and Name are set.
	EventToolUseStart EventType = "tool_use_start"
	// EventToolUseDelta carries, in Text, the next piece of the JSON
	// arguments of the call named by ID.
	EventToolUseDelta EventType = "tool_use_delta"
	// EventToolUseStop says the arguments of the call named by ID are
	// complete. A provider may leave it out; the end of the stream closes
	// every open call.
	EventToolUseStop EventType = "tool_use_stop"
	// EventUsage carries the request's token counts so far in Usage. Each
	// one replaces the one before.
	EventUsage EventType = "usage"
	// EventStop is the last event of a stream that ended well. StopReason
	// says why the model stopped.
	EventStop EventType = "stop"
)

// Event is one step of a streamed answer.
type Event struct {
	Type       EventType
	Text       string
	ID         string
	Name       string
	Signature  string
	Opaque     string
	Usage      Usage
	StopReason StopReason
}

// StopReason says why a model stopped writing.
type StopReason string

const (
	// StopEndTurn: the model finished what it had to say.
	StopEndTurn StopReason = "end_turn"
	// StopToolUse: the model asked for tools and waits for their results.
	StopToolUse StopReason = "tool_use"
	// StopMaxTokens: the answer hit the token cap and is cut short.
	StopMaxTokens StopReason = "max_tokens"
	// StopSequence: the model wrote one of the request's stop strings.
	StopSequence StopReason = "stop_sequence"
	// StopRefusal: the provider withheld the answer.
	StopRefusal StopReason = "refusal"
	// StopCancelled: the reply was cut off by the person or by gnosh being
	// stopped. No provider sends this; the harness records it for a reply
	// it did not let finish.
	StopCancelled StopReason = "cancelled"
	// StopError: the provider failed while the reply was being written. No
	// provider sends this either; the harness records it beside what had
	// arrived.
	StopError StopReason = "error"
)

// Usage counts the tokens of one request. The whole prompt is
// InputTokens + CacheReadTokens + CacheWriteTokens.
type Usage struct {
	// InputTokens is the part of the prompt the provider read fresh.
	InputTokens int `json:"input_tokens"`
	// OutputTokens is what the model wrote, reasoning included.
	OutputTokens int `json:"output_tokens"`
	// CacheReadTokens is the part of the prompt served from the provider's
	// cache.
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
	// CacheWriteTokens is the part of the prompt newly written to the cache.
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// PromptTokens is the size of the whole prompt.
func (u Usage) PromptTokens() int {
	return u.InputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// Provider is a model service: something that takes a Request and streams
// back an answer.
type Provider interface {
	// Name is the name the provider was configured under.
	Name() string

	// Stream sends req and yields the answer's events in order. The
	// sequence ends after an EventStop, or by yielding a non-nil error (a
	// *ProviderError where the provider is at fault) as its last pair.
	// Cancelling ctx stops the request and ends the sequence with ctx's
	// error. Stream does not retry; the caller decides that.
	Stream(ctx context.Context, req *Request) iter.Seq2[Event, error]
}

// ModelInfo describes one model a provider serves.
type ModelInfo struct {
	// ID is the provider's name for the model.
	ID string
	// ContextWindow is how many tokens the model can read at once. Zero
	// means unknown.
	ContextWindow int
	// MaxOutput is the most tokens the model writes in one answer. Zero
	// means unknown.
	MaxOutput int
	// State is whether the server has the model loaded, as a server that
	// loads and unloads models says it ("loaded", "unloaded"; llama.cpp's
	// router): a switch to one not loaded waits while it is loaded. Empty
	// means the server does not say.
	State string
}

// ModelLister is a Provider that can say which models it serves.
type ModelLister interface {
	Models(ctx context.Context) ([]ModelInfo, error)
}

// ServerLoad is what a model server says of its work at a moment: each of
// its slots (the requests it can work on side by side), and what is in it.
type ServerLoad struct {
	Slots []SlotLoad
}

// SlotLoad is one slot of a model server.
type SlotLoad struct {
	// Busy says a request is in it.
	Busy bool
	// Prompt is how many tokens the prompt of that request is, and Read
	// how many of them the server has read so far, a cache's included.
	// Some servers count a prompt only as far as they have read it, while
	// they read (llama.cpp's builds of late 2026: Prompt is then Read and
	// the batch in hand), and the reply's tokens with it after.
	Prompt, Read int
	// Replying says the request has begun its reply: the server is done
	// reading its prompt and writes. A server that does not say leaves it
	// false.
	Replying bool
}

// LoadReporter is a Provider whose server can say how busy it is, for the
// person waiting on it: a model server on a machine of one's own may be
// reading a long prompt, or answering someone else.
type LoadReporter interface {
	Load(ctx context.Context, model string) (ServerLoad, error)
}

// ImageDropper is a Provider that may leave a request's images out,
// because its server said it cannot take them: it then sends each as a
// line saying it was left out.
type ImageDropper interface {
	// LeavesOutImages reports whether it does so now for model.
	LeavesOutImages(model string) bool
}

// TokenCounter is a Provider that can count a request's prompt tokens
// without running it.
type TokenCounter interface {
	CountTokens(ctx context.Context, req *Request) (int, error)
}
