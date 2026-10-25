package model

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// startWorkspace is an agent never ready, so a prompt sent comes back
// refused with its text; it has a config and a working directory, for a
// session to load, and no sessions.
type startWorkspace struct {
	workspace.Workspace
}

func (startWorkspace) AgentReadyErr() error { return errors.New("not ready") }

func (startWorkspace) Config() *config.Config {
	return &config.Config{Options: &config.Options{TUI: &config.TUIOptions{ActiveTheme: "klash-cosmos"}}}
}

func (startWorkspace) WorkingDir() string { return "/project" }

func (startWorkspace) ListSessions(context.Context) ([]session.Session, error) { return nil, nil }

// A prompt given on the command line, as Claude Code's `claude "query"`,
// is sent once and in the session it is for: at once when nothing is
// loaded first; once the session asked for is loaded; after onboarding;
// or, with the list of sessions to pick from, it waits in the editor.
func TestStartPrompt(t *testing.T) {
	t.Parallel()

	starting := func(state uiState) *UI {
		u := newTestUI()
		u.com.Workspace = startWorkspace{}
		u.state = state
		u.SendAtStart("explain this project")
		return u
	}

	u := starting(uiLanding)
	cmd := u.startPromptAtInit(false)
	require.NotNil(t, cmd)
	require.Equal(t, startPromptMsg{}, cmd())
	_, _ = u.Update(startPromptMsg{})
	require.Empty(t, u.startPrompt, "sent")
	_, _ = u.Update(startPromptMsg{})
	require.Nil(t, u.sendStartPrompt(), "once")

	u = starting(uiLanding)
	require.Nil(t, u.startPromptAtInit(true), "a session is loading")
	require.Equal(t, "explain this project", u.startPrompt)
	sess := session.Session{ID: "s1"}
	_, _ = u.Update(loadSessionMsg{session: &sess})
	require.Empty(t, u.startPrompt, "sent once the session is loaded")

	u = starting(uiOnboarding)
	require.Nil(t, u.startPromptAtInit(false), "a provider is set up first")
	require.Equal(t, "explain this project", u.startPrompt)

	u = starting(uiLanding)
	u.PickSessionAtStart("")
	require.Nil(t, u.startPromptAtInit(false))
	require.Empty(t, u.startPrompt)
	require.Equal(t, "explain this project", u.textarea.Value(), "it waits in the editor for the session picked")

	u = newTestUI()
	require.Nil(t, u.startPromptAtInit(false), "no prompt given")

	// What is sent is the prompt.
	u = starting(uiLanding)
	var sent []string
	for _, msg := range msgsOf(u.sendStartPrompt()) {
		if m, ok := msg.(agentRunSubmittedMsg); ok {
			sent = append(sent, m.prompt)
		}
	}
	require.Equal(t, []string{"explain this project"}, sent)
}

// With --continue and no session to continue, the prompt given goes in a
// new session.
func TestStartPromptWithNothingToContinue(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.com.Workspace = startWorkspace{}
	u.state = uiLanding
	u.continueLastSession = true
	u.SendAtStart("explain this project")
	require.Equal(t, tea.Msg(startPromptMsg{}), u.loadInitialSession()())
}
