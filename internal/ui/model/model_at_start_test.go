package model

import (
	"context"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// restoreWorkspace has a config with two models of one provider, the
// first selected, and a theme of its own, so restoring a model changes no
// theme, and records the preferred models it is asked to save.
type restoreWorkspace struct {
	workspace.Workspace
	cfg   *config.Config
	saved []config.SelectedModel
}

func newRestoreWorkspace() *restoreWorkspace {
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("standin", config.ProviderConfig{ID: "standin", Models: []catwalk.Model{{ID: "qwen-a"}, {ID: "qwen-b"}}})
	return &restoreWorkspace{cfg: &config.Config{
		Providers: providers,
		Models: map[config.SelectedModelType]config.SelectedModel{
			config.SelectedModelTypeLarge: {Provider: "standin", Model: "qwen-a"},
			config.SelectedModelTypeSmall: {Provider: "standin", Model: "qwen-a"},
		},
		Options: &config.Options{TUI: &config.TUIOptions{ActiveTheme: "klash-cosmos"}},
	}}
}

func (w *restoreWorkspace) Config() *config.Config { return w.cfg }

func (w *restoreWorkspace) UpdatePreferredModel(_ config.Scope, _ config.SelectedModelType, model config.SelectedModel) error {
	w.saved = append(w.saved, model)
	return nil
}

func (w *restoreWorkspace) UpdateAgentModel(context.Context) error { return nil }

func (w *restoreWorkspace) WorkingDir() string { return "/project" }

// Claude Code's --model: loading the session the UI starts in keeps the
// model chosen, where a session loaded otherwise switches to (and saves)
// the model it last used; a session loaded after the first does.
func TestModelChosenAtStart(t *testing.T) {
	t.Parallel()

	lastUsedB := []message.Message{{ID: "a1", Role: message.Assistant, SessionID: "s1", Provider: "standin", Model: "qwen-b"}}
	restored := []config.SelectedModel{{Provider: "standin", Model: "qwen-b"}}

	u := newTestUI()
	ws := newRestoreWorkspace()
	u.com.Workspace = ws
	require.NotNil(t, u.restoreModelOnLoad(lastUsedB))
	require.Equal(t, restored, ws.saved, "without --model a loaded session brings back its model")

	u = newTestUI()
	ws = newRestoreWorkspace()
	u.com.Workspace = ws
	u.ModelChosenAtStart()
	require.Nil(t, u.restoreModelOnLoad(lastUsedB))
	require.Empty(t, ws.saved, "the session the UI starts in keeps the model --model chose")
	require.NotNil(t, u.restoreModelOnLoad(lastUsedB))
	require.Equal(t, restored, ws.saved, "a session loaded after it switches, as before")

	// The same as each session's load arrives.
	u = newTestUI()
	ws = newRestoreWorkspace()
	u.com.Workspace = ws
	u.ModelChosenAtStart()
	sess := session.Session{ID: "s1"}
	_, _ = u.Update(loadSessionMsg{session: &sess, messages: lastUsedB})
	require.Empty(t, ws.saved)
	_, _ = u.Update(loadSessionMsg{session: &sess, messages: lastUsedB})
	require.Equal(t, restored, ws.saved)
}
