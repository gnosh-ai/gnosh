package cmd

import (
	"cmp"
	"encoding/json"
	"io"
	"slices"

	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/message"
)

// streamJSON writes a run as Claude Code's -p --output-format stream-json
// does, one JSON object per line: a "system" init line once the session
// is known, an "assistant" line for each of the agent's messages (its
// thinking, text and tool_use blocks), a "user" line for each tool result,
// and the "result" line at the end (see runOutput.finish). Sub-agents'
// sessions and summaries are left out.
//
// An assistant message is written when it finishes or, if it called tools,
// when the first of their results comes, whichever is first: the agent
// marks the message finished only after the tools have run, so waiting for
// that would put each result before the call that asked for it.
type streamJSON struct {
	enc       *json.Encoder
	sessionID string
	pending   []string                   // assistant messages not yet written, in order
	latest    map[string]message.Message // the newest copy of each
	callOwner map[string]string          // tool call ID -> its assistant message
	written   map[string]bool            // written assistant messages and tool results
	err       error
}

func newStreamJSON(w io.Writer) *streamJSON {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &streamJSON{
		enc:       enc,
		latest:    map[string]message.Message{},
		callOwner: map[string]string{},
		written:   map[string]bool{},
	}
}

// streamInit is what the init line says about the run, as Claude Code's
// does: the tools the model is offered and the MCP servers by name and
// status, beside the session, directory, model and permission mode.
type streamInit struct {
	SessionID      string
	CWD            string
	Model          string
	PermissionMode string
	Tools          []string
	MCPServers     []mcpServerStatus
}

type mcpServerStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// mcpServerStatuses are the MCP servers as the init line lists them, by
// name, each with Claude Code's word for its state.
func mcpServerStatuses() []mcpServerStatus {
	var servers []mcpServerStatus
	for name, info := range mcp.GetStates() {
		status := info.State.String()
		switch info.State {
		case mcp.StateStarting:
			status = "pending"
		case mcp.StateError:
			status = "failed"
		case mcp.StateNeedsAuth:
			status = "needs-auth"
		}
		servers = append(servers, mcpServerStatus{Name: name, Status: status})
	}
	slices.SortFunc(servers, func(a, b mcpServerStatus) int { return cmp.Compare(a.Name, b.Name) })
	return servers
}

// start writes the init line for the run's session.
func (s *streamJSON) start(init streamInit) {
	s.sessionID = init.SessionID
	s.write(map[string]any{
		"type":           "system",
		"subtype":        "init",
		"session_id":     init.SessionID,
		"cwd":            init.CWD,
		"model":          init.Model,
		"permissionMode": init.PermissionMode,
		"tools":          nonNil(init.Tools),
		"mcp_servers":    nonNil(init.MCPServers),
	})
}

// nonNil is list, or an empty one for nil: the init line's lists are
// never null.
func nonNil[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

// message takes one update of a message of the run.
func (s *streamJSON) message(msg message.Message) {
	if msg.SessionID != s.sessionID || msg.IsSummaryMessage {
		return
	}
	switch msg.Role {
	case message.Assistant:
		if s.written[msg.ID] {
			return
		}
		if _, seen := s.latest[msg.ID]; !seen {
			s.pending = append(s.pending, msg.ID)
		}
		s.latest[msg.ID] = msg
		for _, call := range msg.ToolCalls() {
			s.callOwner[call.ID] = msg.ID
		}
		if msg.IsFinished() {
			s.writeAssistant(msg.ID)
		}
	case message.Tool:
		for _, result := range msg.ToolResults() {
			key := "result:" + result.ToolCallID
			if s.written[key] {
				continue
			}
			if owner, ok := s.callOwner[result.ToolCallID]; ok {
				s.writeAssistant(owner)
			}
			s.written[key] = true
			s.write(map[string]any{
				"type": "user",
				"message": map[string]any{
					"role": "user",
					"content": []any{map[string]any{
						"type":        "tool_result",
						"tool_use_id": result.ToolCallID,
						"content":     result.Content,
						"is_error":    result.IsError,
					}},
				},
				"parent_tool_use_id": nil,
				"session_id":         s.sessionID,
			})
		}
	}
}

// flush writes the assistant messages that never finished, as a canceled
// or failed turn leaves them, so their text is not lost.
func (s *streamJSON) flush() {
	for _, id := range s.pending {
		s.writeAssistant(id)
	}
}

func (s *streamJSON) writeAssistant(id string) {
	msg, ok := s.latest[id]
	if !ok || s.written[id] {
		return
	}
	s.written[id] = true
	var content []any
	if thinking := msg.ReasoningContent().Thinking; thinking != "" {
		content = append(content, map[string]any{"type": "thinking", "thinking": thinking})
	}
	if text := msg.Content().Text; text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for _, call := range msg.ToolCalls() {
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    call.ID,
			"name":  call.Name,
			"input": toolInput(call.Input),
		})
	}
	if len(content) == 0 {
		return
	}
	var stopReason any
	switch {
	case msg.IsFinished():
		stopReason = string(msg.FinishReason())
	case len(msg.ToolCalls()) > 0:
		stopReason = string(message.FinishReasonToolUse)
	}
	s.write(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"id":          msg.ID,
			"type":        "message",
			"role":        "assistant",
			"model":       msg.Model,
			"content":     content,
			"stop_reason": stopReason,
		},
		"parent_tool_use_id": nil,
		"session_id":         s.sessionID,
	})
}

// toolInput is a tool call's input as a JSON value: the object the model
// sent, or {} for none; input that is not JSON at all is kept as a string.
func toolInput(input string) json.RawMessage {
	if input == "" {
		return json.RawMessage("{}")
	}
	if json.Valid([]byte(input)) {
		return json.RawMessage(input)
	}
	quoted, _ := json.Marshal(input)
	return quoted
}

func (s *streamJSON) write(v any) {
	if s.err == nil {
		s.err = s.enc.Encode(v)
	}
}
