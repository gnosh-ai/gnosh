package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/proto"
	"github.com/stretchr/testify/require"
)

func TestPopAgentSessionQueuedPromptsDecodesAttachments(t *testing.T) {
	t.Parallel()

	paste := proto.Attachment{
		FileName: "paste_1.txt",
		FilePath: "paste_1.txt",
		MimeType: "text/plain",
		Content:  []byte("log"),
	}
	want := []proto.PoppedPrompt{
		{Prompt: "look at this", Attachments: []proto.Attachment{paste}},
		{Prompt: "second prompt"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/v1/workspaces/ws1/agent/sessions/s1/prompts/pop", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode(want))
	}))
	defer srv.Close()

	got, err := captureClient(t, srv).PopAgentSessionQueuedPrompts(t.Context(), "ws1", "s1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "look at this", got[0].Prompt)
	require.Len(t, got[0].Attachments, 1)
	require.Equal(t, message.Attachment{
		FileName: "paste_1.txt",
		FilePath: "paste_1.txt",
		MimeType: "text/plain",
		Content:  []byte("log"),
	}, got[0].Attachments[0], "the attachment must survive the wire round-trip")
	require.Equal(t, "second prompt", got[1].Prompt)
	require.Empty(t, got[1].Attachments)
}

func TestPopAgentSessionQueuedPromptsEmpty(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/workspaces/ws1/agent/sessions/s1/prompts/pop", r.URL.Path)
		require.NoError(t, json.NewEncoder(w).Encode([]proto.PoppedPrompt{}))
	}))
	defer srv.Close()

	got, err := captureClient(t, srv).PopAgentSessionQueuedPrompts(t.Context(), "ws1", "s1")
	require.NoError(t, err)
	require.Empty(t, got)
}
