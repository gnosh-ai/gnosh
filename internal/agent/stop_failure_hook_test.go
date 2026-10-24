package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// The kinds of error a turn ends on are Claude Code's StopFailure names; a
// model server that never answered is server_error.
func TestStopFailureKind(t *testing.T) {
	t.Parallel()

	for want, err := range map[string]error{
		"rate_limit":            &fantasy.ProviderError{StatusCode: http.StatusTooManyRequests},
		"overloaded":            &fantasy.ProviderError{StatusCode: http.StatusServiceUnavailable},
		"authentication_failed": &fantasy.ProviderError{StatusCode: http.StatusUnauthorized},
		"billing_error":         &fantasy.ProviderError{StatusCode: http.StatusPaymentRequired},
		"model_not_found":       &fantasy.ProviderError{StatusCode: http.StatusNotFound},
		"invalid_request":       &fantasy.ProviderError{StatusCode: http.StatusBadRequest},
		"server_error":          &fantasy.ProviderError{StatusCode: http.StatusInternalServerError},
		"unknown":               errors.New("boom"),
	} {
		require.Equalf(t, want, stopFailureKind(err), "%v", err)
	}
	refused := &fantasy.ProviderError{Cause: fmt.Errorf("dial tcp 127.0.0.1:8080: connect: %w", syscall.ECONNREFUSED)}
	require.Equal(t, "server_error", stopFailureKind(refused), "the model server did not answer")
	require.Equal(t, "server_error", stopFailureKind(&requestTimeoutError{}))
}

// A turn that ends on an error from the model's API runs the StopFailure
// hooks, as Claude Code does in place of Stop, with the error's kind,
// what the API said and the error as the conversation shows it.
func TestRun_StopFailureHookGetsTheError(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	payloadPath := filepath.Join(t.TempDir(), "payload.json")
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{
		hooks.EventStopFailure: {{Command: `cat > ` + payloadPath}},
	}}
	require.NoError(t, cfg.ValidateHooks())
	llm := &fakeLanguageModel{stream: func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: &fantasy.ProviderError{
			StatusCode: http.StatusBadRequest, Title: "bad request", Message: "the prompt is too long",
		}})
	}}
	model := Model{Model: llm, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:           model,
		SmallModel:           model,
		SystemPrompt:         "test",
		IsYolo:               true,
		DisableAutoSummarize: true,
		Sessions:             env.sessions,
		Messages:             env.messages,
		Cfg:                  config.NewTestStore(cfg),
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "stop failure")
	require.NoError(t, err)
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
	})
	require.NoError(t, err)

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, RunID: "run-1", Prompt: "go"})
	require.Error(t, err)

	data, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "StopFailure", payload["hook_event_name"])
	require.Equal(t, sess.ID, payload["session_id"])
	require.Equal(t, "invalid_request", payload["error"])
	require.Contains(t, payload["error_details"], "the prompt is too long")

	// last_assistant_message is the error as the conversation shows it.
	msgs, err := env.messages.List(t.Context(), sess.ID)
	require.NoError(t, err)
	last := msgs[len(msgs)-1]
	require.Equal(t, message.Assistant, last.Role)
	require.Contains(t, shownError(&last), "the prompt is too long")
	require.Equal(t, shownError(&last), payload["last_assistant_message"])
}

// A sub-agent runs no StopFailure hooks, as it runs no Stop hooks.
func TestStopFailureHooksSkipSubAgents(t *testing.T) {
	t.Parallel()

	ran := filepath.Join(t.TempDir(), "ran")
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{hooks.EventStopFailure: {{Command: `touch ` + ran}}}}
	require.NoError(t, cfg.ValidateHooks())
	sub := NewSessionAgent(SessionAgentOptions{IsSubAgent: true, Cfg: config.NewTestStore(cfg)}).(*sessionAgent)
	sub.runStopFailureHooks(context.Background(), "s1", errors.New("boom"), "Provider Error: boom")
	require.NoFileExists(t, ran)

	top := NewSessionAgent(SessionAgentOptions{Cfg: config.NewTestStore(cfg)}).(*sessionAgent)
	top.runStopFailureHooks(context.Background(), "s1", errors.New("boom"), "Provider Error: boom")
	require.FileExists(t, ran)
}
