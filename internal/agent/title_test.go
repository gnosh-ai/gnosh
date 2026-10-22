package agent

import (
	"context"
	"sync"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/stretchr/testify/require"
)

// titleModel answers title requests with fixed text and a fixed finish
// reason, and records the options each request carried.
type titleModel struct {
	fakeLanguageModel
	text   string
	finish fantasy.FinishReason

	mu    sync.Mutex
	calls []fantasy.Call
}

func (m *titleModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	m.calls = append(m.calls, call)
	m.mu.Unlock()
	return func(yield func(fantasy.StreamPart) bool) {
		if m.text != "" {
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t", Delta: m.text})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t"})
		}
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: m.finish})
	}, nil
}

func (m *titleModel) recorded() []fantasy.Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]fantasy.Call(nil), m.calls...)
}

// titleAgent builds a session agent whose small and large models are both
// llm, served by an OpenAI-compatible provider "qwen", as on the AI box.
func titleAgent(t *testing.T, llm fantasy.LanguageModel) (*sessionAgent, fakeEnv, string) {
	t.Helper()
	env := testEnv(t)
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("qwen", config.ProviderConfig{ID: "qwen", Type: "llamacpp"})
	cfg := &config.Config{Providers: providers, Options: &config.Options{}}
	model := Model{
		Model:      llm,
		CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000},
		ModelCfg:   config.SelectedModel{Provider: "qwen", Model: "qwen3.8-27b"},
	}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel: model,
		SmallModel: model,
		Sessions:   env.sessions,
		Messages:   env.messages,
		Cfg:        config.NewTestStore(cfg),
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "")
	require.NoError(t, err)
	return a, env, sess.ID
}

// The cause of every session on the AI box being "Untitled Session": the
// title request went out with no thinking switch, so Qwen reasoned through
// its whole budget. Each attempt must carry enable_thinking false.
func TestGenerateTitleSwitchesThinkingOff(t *testing.T) {
	t.Parallel()

	llm := &titleModel{text: "Fix the flaky test", finish: fantasy.FinishReasonStop}
	a, env, id := titleAgent(t, llm)
	a.GenerateTitle(t.Context(), id, "please fix the flaky test in the agent package")

	calls := llm.recorded()
	require.NotEmpty(t, calls)
	for _, call := range calls {
		kw, ok := thinkingKwargs(t, call.ProviderOptions)
		require.True(t, ok, "the title request must carry the chat-template switch")
		require.Equal(t, false, kw["enable_thinking"])
	}

	sess, err := env.sessions.Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, "Fix the flaky test", sess.Title)
}

// If every model is still cut off, the session is named after the start of
// the prompt, not a generic name that makes it impossible to find later.
func TestGenerateTitleFallsBackToThePrompt(t *testing.T) {
	t.Parallel()

	llm := &titleModel{finish: fantasy.FinishReasonLength}
	a, env, id := titleAgent(t, llm)
	a.GenerateTitle(t.Context(), id, "make a short video of the fromhebrew site\nwith music")

	sess, err := env.sessions.Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, "make a short video of the fromhebrew site with mu…", sess.Title,
		"truncated to 50 columns, the ellipsis included")
	require.NotEqual(t, DefaultSessionName, sess.Title)
	require.Len(t, llm.recorded(), 2, "both the small and the large model are tried first")
}

func TestTitleFromPrompt(t *testing.T) {
	t.Parallel()

	require.Equal(t, "short prompt", titleFromPrompt("  short prompt  "))
	require.Equal(t, "two lines", titleFromPrompt("two\nlines"))
	require.Equal(t, DefaultSessionName, titleFromPrompt(""))
	require.Equal(t, DefaultSessionName, titleFromPrompt(" \n "))
	long := titleFromPrompt("abcdefghij abcdefghij abcdefghij abcdefghij abcdefghij abcdefghij")
	require.LessOrEqual(t, len([]rune(long)), 50)
	require.Contains(t, long, "…")
}

// A provider without the switch gets no options added, and an agent
// without a config does not panic.
func TestTitleProviderOptionsWithoutSwitch(t *testing.T) {
	t.Parallel()

	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("anthropic", config.ProviderConfig{ID: "anthropic", Type: "anthropic"})
	a := NewSessionAgent(SessionAgentOptions{
		Cfg: config.NewTestStore(&config.Config{Providers: providers, Options: &config.Options{}}),
	}).(*sessionAgent)
	require.Empty(t, a.titleProviderOptions(Model{ModelCfg: config.SelectedModel{Provider: "anthropic"}}))

	bare := NewSessionAgent(SessionAgentOptions{}).(*sessionAgent)
	require.Empty(t, bare.titleProviderOptions(Model{ModelCfg: config.SelectedModel{Provider: "qwen"}}))
}
