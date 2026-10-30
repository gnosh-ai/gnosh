package proto

import "github.com/charmbracelet/crush/internal/session"

// Session represents a session in the proto layer.
//
// IsBusy is computed on read (it is not persisted with the session) and
// reflects whether an agent run is currently in flight for this session.
// It is populated by REST handlers in internal/server/proto.go from the
// workspace's AgentCoordinator. The Session SSE event path does not set
// it, since SSE consumers can compute presence from other agent signals.
//
// AttachedClients counts the number of clients currently viewing this
// session — i.e. entries in the workspace's clients map whose
// currentSessionID equals this session's ID and which have at least one
// live SSE stream. Hold-only clients (streams == 0) do not contribute.
// Like IsBusy, it is computed on read by REST handlers.
type Session struct {
	ID               string  `json:"id"`
	ParentSessionID  string  `json:"parent_session_id"`
	Title            string  `json:"title"`
	MessageCount     int64   `json:"message_count"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	SummaryMessageID string  `json:"summary_message_id"`
	Cost             float64 `json:"cost"`
	Todos            []Todo  `json:"todos,omitempty"`
	Channel          string  `json:"channel,omitempty"`
	CreatedAt        int64   `json:"created_at"`
	UpdatedAt        int64   `json:"updated_at"`
	IsBusy           bool    `json:"is_busy"`
	AttachedClients  int     `json:"attached_clients"`
}

// Todo represents a single todo entry on a session in the proto layer.
type Todo struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"active_form"`
}

// SessionFromDomain is s as the proto layer carries it, apart from what is
// computed on read (IsBusy, AttachedClients).
func SessionFromDomain(s session.Session) Session {
	return Session{
		ID:               s.ID,
		ParentSessionID:  s.ParentSessionID,
		Title:            s.Title,
		SummaryMessageID: s.SummaryMessageID,
		MessageCount:     s.MessageCount,
		PromptTokens:     s.PromptTokens,
		CompletionTokens: s.CompletionTokens,
		Cost:             s.Cost,
		Todos:            TodosFromDomain(s.Todos),
		Channel:          s.Channel,
		CreatedAt:        s.CreatedAt,
		UpdatedAt:        s.UpdatedAt,
	}
}

// TodosFromDomain is todos as the proto layer carries them; nil for none.
func TodosFromDomain(todos []session.Todo) []Todo {
	if len(todos) == 0 {
		return nil
	}
	out := make([]Todo, len(todos))
	for i, t := range todos {
		out[i] = Todo{
			Content:    t.Content,
			Status:     string(t.Status),
			ActiveForm: t.ActiveForm,
		}
	}
	return out
}
