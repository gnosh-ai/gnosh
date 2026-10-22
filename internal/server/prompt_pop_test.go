package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/agent"
	"github.com/charmbracelet/crush/internal/agent/agenttest"
	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/backend"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// stubPopCoordinator is a coordinator that returns fixed popped prompts
// from PopQueuedPrompts.
type stubPopCoordinator struct {
	agenttest.NopCoordinator
	popped []agent.PoppedPrompt
}

func (s *stubPopCoordinator) PopQueuedPrompts(string) []agent.PoppedPrompt {
	out := s.popped
	s.popped = nil
	return out
}

func TestPromptPopReturnsAttachments(t *testing.T) {
	t.Parallel()

	b := backend.New(t.Context(), nil, nil)
	wsID := uuid.New().String()
	paste := message.Attachment{
		FileName: "paste_1.txt",
		FilePath: "paste_1.txt",
		MimeType: "text/plain",
		Content:  []byte("log"),
	}
	coord := &stubPopCoordinator{popped: []agent.PoppedPrompt{
		{Prompt: "look at this", Attachments: []message.Attachment{paste}},
		{Prompt: "second prompt"},
	}}
	a := &app.App{AgentCoordinator: coord}
	ws := &backend.Workspace{ID: wsID, Path: t.TempDir(), App: a}
	backend.InsertWorkspaceForTest(b, ws)

	s := &Server{backend: b}
	c := &controllerV1{backend: b, server: s}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/workspaces/"+wsID+"/agent/sessions/s1/prompts/pop", nil)
	req.SetPathValue("id", wsID)
	req.SetPathValue("sid", "s1")
	rec := httptest.NewRecorder()
	c.handlePostWorkspaceAgentSessionPromptPop(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var got []proto.PoppedPrompt
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 2)
	require.Equal(t, "look at this", got[0].Prompt)
	require.Len(t, got[0].Attachments, 1)
	require.Equal(t, "paste_1.txt", got[0].Attachments[0].FileName,
		"the attachment must survive the wire format")
	require.Equal(t, "text/plain", got[0].Attachments[0].MimeType)
	require.Equal(t, []byte("log"), got[0].Attachments[0].Content,
		"the attachment content must survive the wire format")
	require.Equal(t, "second prompt", got[1].Prompt)
	require.Empty(t, got[1].Attachments)
}

func TestPromptPopEmptyQueue(t *testing.T) {
	t.Parallel()

	b := backend.New(t.Context(), nil, nil)
	wsID := uuid.New().String()
	coord := &stubPopCoordinator{}
	a := &app.App{AgentCoordinator: coord}
	ws := &backend.Workspace{ID: wsID, Path: t.TempDir(), App: a}
	backend.InsertWorkspaceForTest(b, ws)

	s := &Server{backend: b}
	c := &controllerV1{backend: b, server: s}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/workspaces/"+wsID+"/agent/sessions/s1/prompts/pop", nil)
	req.SetPathValue("id", wsID)
	req.SetPathValue("sid", "s1")
	rec := httptest.NewRecorder()
	c.handlePostWorkspaceAgentSessionPromptPop(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var got []proto.PoppedPrompt
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Empty(t, got)
}
