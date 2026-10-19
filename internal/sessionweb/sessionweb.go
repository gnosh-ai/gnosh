// Package sessionweb serves a small local web page for looking at and editing
// the messages of Crush's sessions: the chat history that is re-sent to the
// model on every turn. It exists so the history can be changed by clicking
// and typing, and so what the model receives can be seen in one place.
package sessionweb

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
)

//go:embed page.html
var pageHTML embed.FS

// Store is the slice of Crush's session and message services the page needs.
type Store interface {
	ListSessions(ctx context.Context) ([]session.Session, error)
	ListMessages(ctx context.Context, sessionID string) ([]message.Message, error)
	GetMessage(ctx context.Context, id string) (message.Message, error)
	UpdateMessage(ctx context.Context, msg message.Message) error
	DeleteMessage(ctx context.Context, id string) error
	CreateMessage(ctx context.Context, sessionID string, params message.CreateMessageParams) (message.Message, error)
}

// Context is what the model receives besides the history: the prompt
// template in effect and the tool descriptions. Nil fields are allowed.
type Context struct {
	Template       string             `json:"template"`
	TemplateSource string             `json:"template_source"`
	Tools          []fantasy.ToolInfo `json:"tools"`
}

// ContextFunc builds the Context for the current project; may be nil.
type ContextFunc func(ctx context.Context) (Context, error)

// Server is the HTTP handler.
type Server struct {
	store   Store
	context ContextFunc
	mux     *http.ServeMux
}

// New wires the routes.
func New(store Store, contextFn ContextFunc) *Server {
	s := &Server{store: store, context: contextFn, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /{$}", s.page)
	s.mux.HandleFunc("GET /api/sessions", s.listSessions)
	s.mux.HandleFunc("GET /api/sessions/{id}/messages", s.listMessages)
	s.mux.HandleFunc("POST /api/sessions/{id}/messages", s.createMessage)
	s.mux.HandleFunc("PUT /api/messages/{id}", s.updateMessage)
	s.mux.HandleFunc("DELETE /api/messages/{id}", s.deleteMessage)
	s.mux.HandleFunc("GET /api/context", s.getContext)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	b, err := pageHTML.ReadFile("page.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

type sessionJSON struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	MessageCount int64  `json:"message_count"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

type messageJSON struct {
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	Role      string          `json:"role"`
	Model     string          `json:"model"`
	Provider  string          `json:"provider"`
	CreatedAt int64           `json:"created_at"`
	IsSummary bool            `json:"is_summary"`
	Parts     json.RawMessage `json:"parts"`
}

func toMessageJSON(m message.Message) (messageJSON, error) {
	parts, err := message.MarshalParts(m.Parts)
	if err != nil {
		return messageJSON{}, err
	}
	return messageJSON{
		ID: m.ID, SessionID: m.SessionID, Role: string(m.Role), Model: m.Model, Provider: m.Provider,
		CreatedAt: m.CreatedAt, IsSummary: m.IsSummaryMessage, Parts: parts,
	}, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.store.ListSessions(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]sessionJSON, 0, len(sessions))
	for _, se := range sessions {
		out = append(out, sessionJSON{ID: se.ID, Title: se.Title, MessageCount: se.MessageCount, CreatedAt: se.CreatedAt, UpdatedAt: se.UpdatedAt})
	}
	writeJSON(w, out)
}

func (s *Server) listMessages(w http.ResponseWriter, r *http.Request) {
	msgs, err := s.store.ListMessages(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]messageJSON, 0, len(msgs))
	for _, m := range msgs {
		mj, err := toMessageJSON(m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, mj)
	}
	writeJSON(w, out)
}

func (s *Server) createMessage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Role string `json:"role"`
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	role := message.MessageRole(in.Role)
	if role != message.User && role != message.Assistant {
		http.Error(w, "role must be user or assistant", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(in.Text) == "" {
		http.Error(w, "text is empty", http.StatusBadRequest)
		return
	}
	m, err := s.store.CreateMessage(r.Context(), r.PathValue("id"), message.CreateMessageParams{
		Role:  role,
		Parts: []message.ContentPart{message.TextContent{Text: in.Text}, message.Finish{Reason: message.FinishReasonEndTurn, Time: time.Now().Unix()}},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	mj, err := toMessageJSON(m)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, mj)
}

func (s *Server) updateMessage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Parts json.RawMessage `json:"parts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	parts, err := message.UnmarshalParts(in.Parts)
	if err != nil {
		http.Error(w, fmt.Sprintf("parts: %v", err), http.StatusBadRequest)
		return
	}
	if len(parts) == 0 {
		http.Error(w, "a message needs at least one part; delete it instead", http.StatusBadRequest)
		return
	}
	m, err := s.store.GetMessage(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	m.Parts = parts
	if err := s.store.UpdateMessage(r.Context(), m); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteMessage(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getContext(w http.ResponseWriter, r *http.Request) {
	if s.context == nil {
		http.Error(w, "no context available", http.StatusNotFound)
		return
	}
	c, err := s.context(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, c)
}

// ErrNotFound is returned by a Store for an unknown id.
var ErrNotFound = errors.New("not found")
