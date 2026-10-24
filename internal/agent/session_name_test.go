package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// namingModel answers every request with answer and usage, and keeps
// the requests.
type namingModel struct {
	fakeLanguageModel
	answer string
	usage  fantasy.Usage

	mu    sync.Mutex
	calls []fantasy.Call
}

func (m *namingModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	m.calls = append(m.calls, call)
	m.mu.Unlock()
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t", Delta: m.answer})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop, Usage: m.usage})
	}, nil
}

func (m *namingModel) recorded() []fantasy.Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]fantasy.Call(nil), m.calls...)
}

// namingAgent builds a session agent whose models are both llm, on a
// llama.cpp provider with a price per token, and a session whose
// conversation is texts, user and assistant in turn.
func namingAgent(t *testing.T, llm fantasy.LanguageModel, texts ...string) (*sessionAgent, fakeEnv, string) {
	t.Helper()
	env := testEnv(t)
	providers := csync.NewMap[string, config.ProviderConfig]()
	providers.Set("qwen", config.ProviderConfig{ID: "qwen", Type: "llamacpp"})
	model := Model{
		Model: llm,
		CatwalkCfg: catwalk.Model{
			ContextWindow: 200000, DefaultMaxTokens: 1000,
			CostPer1MIn: 2, CostPer1MOut: 10,
		},
		ModelCfg: config.SelectedModel{Provider: "qwen", Model: "qwen3.8-27b"},
	}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel: model,
		SmallModel: model,
		Sessions:   env.sessions,
		Messages:   env.messages,
		Cfg:        config.NewTestStore(&config.Config{Providers: providers, Options: &config.Options{}}),
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "Untitled Session")
	require.NoError(t, err)
	for i, text := range texts {
		role := message.User
		if i%2 == 1 {
			role = message.Assistant
		}
		_, err := env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
			Role:  role,
			Parts: []message.ContentPart{message.TextContent{Text: text}},
		})
		require.NoError(t, err)
	}
	return a, env, sess.ID
}

// promptTexts joins the text of prompt's messages with role.
func promptTexts(prompt fantasy.Prompt, role fantasy.MessageRole) string {
	var b strings.Builder
	for _, msg := range prompt {
		if msg.Role != role {
			continue
		}
		for _, part := range msg.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				b.WriteString(text.Text)
			}
		}
	}
	return b.String()
}

// /rename with no name, as in Claude Code: the small model is shown the
// end of the conversation as data and asked for a kebab-case name, which
// becomes the session's title once cleaned as a typed one is. The request's cost counts toward the
// session's; its tokens do not count as context.
func TestGenerateSessionName(t *testing.T) {
	t.Parallel()

	llm := &namingModel{
		answer: "```json\n{\"name\": \" fix-login-bug\\t\"}\n```",
		usage:  fantasy.Usage{InputTokens: 500000, OutputTokens: 100000},
	}
	a, env, id := namingAgent(t, llm, "the login page rejects good passwords", "the hash compare was inverted")

	name, err := a.GenerateSessionName(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, "fix-login-bug", name)

	sess, err := env.sessions.Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, "fix-login-bug", sess.Title)
	require.InDelta(t, 2.0, sess.Cost, 1e-9, "half a million tokens in at $2/M and a hundred thousand out at $10/M")
	require.Zero(t, sess.PromptTokens)
	require.Zero(t, sess.CompletionTokens)

	calls := llm.recorded()
	require.Len(t, calls, 1)
	require.Contains(t, promptTexts(calls[0].Prompt, fantasy.MessageRoleSystem), "kebab-case")
	require.Equal(t,
		"<conversation>\nthe login page rejects good passwords\nthe hash compare was inverted\n</conversation>",
		promptTexts(calls[0].Prompt, fantasy.MessageRoleUser))
	kw, ok := thinkingKwargs(t, calls[0].ProviderOptions)
	require.True(t, ok, "the name request must switch thinking off")
	require.Equal(t, false, kw["enable_thinking"])
}

// With nothing written yet there is nothing to name the session by, and
// no request is made; an answer with no name in it renames nothing.
func TestGenerateSessionNameWithout(t *testing.T) {
	t.Parallel()

	llm := &namingModel{answer: `{"name": "unused"}`}
	a, env, id := namingAgent(t, llm)
	_, err := a.GenerateSessionName(t.Context(), id)
	require.ErrorIs(t, err, ErrNoConversation)
	require.Empty(t, llm.recorded())

	llm = &namingModel{answer: "fix-login-bug"}
	a, env, id = namingAgent(t, llm, "the login page rejects good passwords")
	_, err = a.GenerateSessionName(t.Context(), id)
	require.Error(t, err)
	sess, err := env.sessions.Get(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, "Untitled Session", sess.Title)
}

// The excerpt is what the user and the assistant wrote, newest last, cut
// to the last 1,000 characters: no tool output, no thinking, and no
// continuation the agent wrote for itself.
func TestConversationExcerpt(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "first ask"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{
			message.ReasoningContent{Thinking: "private thoughts"},
			message.TextContent{Text: "an answer"},
		}},
		{Role: message.Tool, Parts: []message.ContentPart{message.TextContent{Text: "tool output"}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "Continue.", Hidden: true}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "  "}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "second ask"}}},
	}
	require.Equal(t, "first ask\nan answer\nsecond ask", conversationExcerpt(msgs))

	long := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: strings.Repeat("a", 1500)}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "é"}}},
	}
	excerpt := []rune(conversationExcerpt(long))
	require.Len(t, excerpt, 1000)
	require.Equal(t, "a\né", string(excerpt[len(excerpt)-3:]))
}

func TestParseSessionName(t *testing.T) {
	t.Parallel()

	for answer, want := range map[string]string{
		`{"name": "fix-login-bug"}`:                           "fix-login-bug",
		"```json\n{\"name\":\"add-auth\"}\n```":               "add-auth",
		`<think>{"name": "no"}</think>{"name": "yes"}`:        "yes",
		`Here it is: {"name": "debug-tests"} — hope it helps`: "debug-tests",
		"fix-login-bug":       "",
		`{"title": "x"}`:      "",
		`{"name": "unclosed"`: "",
		"":                    "",
	} {
		require.Equal(t, want, parseSessionName(answer), "answer %q", answer)
	}
}
