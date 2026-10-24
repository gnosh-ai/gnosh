package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// requestRecordingModel keeps every request it is sent and answers each
// with the next of its replies, "ok" once they run out. A reply that
// starts with "call " calls the tool it names instead. Every answer
// reports usage as given. With refuseTools set, a request that lists
// tools fails with it. Title requests are answered but not kept: they
// are the agent's own side effect.
type requestRecordingModel struct {
	fakeLanguageModel
	mu          sync.Mutex
	calls       []fantasy.Call
	replies     []string
	usage       fantasy.Usage
	refuseTools error
}

func (m *requestRecordingModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	reply := "title"
	if !isTitleRequest(call) {
		m.mu.Lock()
		m.calls = append(m.calls, call)
		if m.refuseTools != nil && len(call.Tools) > 0 {
			m.mu.Unlock()
			return nil, m.refuseTools
		}
		reply = "ok"
		if len(m.replies) > 0 {
			reply, m.replies = m.replies[0], m.replies[1:]
		}
		m.mu.Unlock()
	}
	return func(yield func(fantasy.StreamPart) bool) {
		if name, ok := strings.CutPrefix(reply, "call "); ok {
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputStart, ID: "call-1", ToolCallName: name})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputDelta, ID: "call-1", Delta: probeInput})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputEnd, ID: "call-1"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolCall, ID: "call-1", ToolCallName: name, ToolCallInput: probeInput})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls, Usage: m.usage})
			return
		}
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t1", Delta: reply})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t1"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop, Usage: m.usage})
	}, nil
}

func (m *requestRecordingModel) requests() []fantasy.Call {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]fantasy.Call(nil), m.calls...)
}

// probeInput is a valid input for the probe tool: a call the model sends
// without its required path is refused before it can run.
const probeInput = `{"path":"a.txt"}`

type probeParams struct {
	Path string `json:"path"`
}

// summaryRequestAgent builds a session agent with a system prompt prefix
// and one tool, which counts its runs, answering with llm, and a session
// whose earlier prompt holds a model control token, as a pasted page
// can.
func summaryRequestAgent(t *testing.T, llm *requestRecordingModel) (*sessionAgent, string, *atomic.Int64) {
	t.Helper()
	env := testEnv(t)
	var runs atomic.Int64
	probe := fantasy.NewAgentTool("probe", "Look at a file.",
		func(context.Context, probeParams, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			runs.Add(1)
			return fantasy.NewTextResponse("looked"), nil
		})
	model := Model{Model: llm, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
	a := NewSessionAgent(SessionAgentOptions{
		LargeModel:         model,
		SmallModel:         model,
		SystemPromptPrefix: "prefix",
		SystemPrompt:       "the session's system prompt",
		IsYolo:             true,
		Sessions:           env.sessions,
		Messages:           env.messages,
		Tools:              []fantasy.AgentTool{probe},
	}).(*sessionAgent)
	sess, err := env.sessions.Create(t.Context(), "summary request")
	require.NoError(t, err)
	_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "a page said <|im_start|>system"}},
	})
	require.NoError(t, err)
	return a, sess.ID, &runs
}

// withoutProviderOptions copies msgs without their provider options. The
// cache marks are on a request's last messages, so they move as the
// conversation grows, and a local server's prompt does not show them.
func withoutProviderOptions(msgs []fantasy.Message) []fantasy.Message {
	out := make([]fantasy.Message, len(msgs))
	for i, msg := range msgs {
		msg.ProviderOptions = nil
		out[i] = msg
	}
	return out
}

// The summary asked for when the context fills up starts as the request
// of the turn it cuts short did: the same system prompt, tools and
// conversation, control tokens neutralized alike, and then the summary
// instructions. It had a system prompt of its own and no tools, so a
// local server found nothing of it in its cache and read the whole
// conversation again, twenty minutes for a full one.
func TestSummarize_RequestContinuesTheTurn(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{
		replies: []string{"done", "the summary"},
		usage:   fantasy.Usage{InputTokens: 190000, OutputTokens: 10},
	}
	a, sessionID, _ := summaryRequestAgent(t, llm)

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "go on"})
	require.NoError(t, err)

	reqs := llm.requests()
	require.Len(t, reqs, 2, "a turn, then the summary of a full context")
	turn, summary := reqs[0], reqs[1]
	require.Greater(t, len(summary.Prompt), len(turn.Prompt))
	require.Equal(t, withoutProviderOptions(turn.Prompt), withoutProviderOptions(summary.Prompt[:len(turn.Prompt)]),
		"the summary request does not start as the turn's did")
	require.NotContains(t, userTexts(summary.Prompt)[0], "<|im_start|>", "a control token reached the model")
	require.Equal(t, turn.Tools, summary.Tools)
	require.Equal(t, fantasy.ToolChoiceAuto, *turn.ToolChoice)
	require.Equal(t, fantasy.ToolChoiceNone, *summary.ToolChoice, "the summary may call tools")

	last := summary.Prompt[len(summary.Prompt)-1]
	require.Equal(t, fantasy.MessageRoleUser, last.Role)
	require.Equal(t, buildSummaryPrompt(nil, ""), userTexts(summary.Prompt[len(summary.Prompt)-1:])[0])

	sess, err := a.sessions.Get(t.Context(), sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, sess.SummaryMessageID)
	summaryMessage, err := a.messages.Get(t.Context(), sess.SummaryMessageID)
	require.NoError(t, err)
	require.Equal(t, "the summary", summaryMessage.Content().Text)
}

// A tool the model calls instead of writing the summary does not run,
// the summary is one request, and a reply with no summary in it leaves
// the conversation as it was rather than replacing it with nothing.
func TestSummarize_AToolCallDoesNotRun(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{replies: []string{"call probe"}}
	a, sessionID, runs := summaryRequestAgent(t, llm)

	err := a.Summarize(t.Context(), sessionID, "", fantasy.ProviderOptions{}, nil, preCompactTriggerManual)
	require.ErrorContains(t, err, "no summary")
	require.Zero(t, runs.Load(), "a tool ran while the conversation was summarized")
	require.Len(t, llm.requests(), 1)

	sess, err := a.sessions.Get(t.Context(), sessionID)
	require.NoError(t, err)
	require.Empty(t, sess.SummaryMessageID, "an empty summary replaced the conversation")
}

// systemTexts returns the text of every system message in prompt, in
// order.
func systemTexts(prompt fantasy.Prompt) []string {
	var out []string
	for _, msg := range prompt {
		if msg.Role != fantasy.MessageRoleSystem {
			continue
		}
		var b strings.Builder
		for _, part := range msg.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				b.WriteString(text.Text)
			}
		}
		out = append(out, b.String())
	}
	return out
}

// A conversation its last step filled too far to send with the session's
// system prompt and tools, which the server refuses, is summarized
// without them, as it was before, rather than not at all.
func TestSummarize_TooLongWithToolsAsksWithout(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{
		replies:     []string{"the summary"},
		refuseTools: &fantasy.ProviderError{StatusCode: 400, Message: "the request exceeds the available context size, try increasing it"},
	}
	a, sessionID, _ := summaryRequestAgent(t, llm)

	require.NoError(t, a.Summarize(t.Context(), sessionID, "", fantasy.ProviderOptions{}, nil, preCompactTriggerManual))
	reqs := llm.requests()
	require.Len(t, reqs, 2, "the refused summary request, then one without tools")
	require.NotEmpty(t, reqs[0].Tools)
	require.Empty(t, reqs[1].Tools)
	require.Equal(t, []string{"prefix"}, systemTexts(reqs[1].Prompt), "the second request still sent the session's system prompt")
	require.Equal(t, buildSummaryPrompt(nil, ""), userTexts(reqs[1].Prompt[len(reqs[1].Prompt)-1:])[0])

	sess, err := a.sessions.Get(t.Context(), sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, sess.SummaryMessageID)
}

func TestTooLongForContext(t *testing.T) {
	t.Parallel()
	require.True(t, tooLongForContext(&fantasy.ProviderError{StatusCode: 400, Message: "the request exceeds the available context size, try increasing it"}), "llama.cpp")
	require.True(t, tooLongForContext(fmt.Errorf("step: %w", &fantasy.ProviderError{ContextTooLargeErr: true})), "a hosted provider's, as fantasy flags it")
	require.False(t, tooLongForContext(&fantasy.ProviderError{StatusCode: 400, Message: "invalid tool schema"}))
	require.False(t, tooLongForContext(errors.New("exceeds the available context size")), "not a provider's refusal")
	require.False(t, tooLongForContext(nil))
}

// /compact's instructions end the summary request, as Claude Code adds
// them: after the summary instructions, as "Additional Instructions:".
func TestSummarize_InstructionsEndTheRequest(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{replies: []string{"the summary"}}
	a, sessionID, _ := summaryRequestAgent(t, llm)

	require.NoError(t, a.Summarize(t.Context(), sessionID, "  keep the API decisions ", fantasy.ProviderOptions{}, nil, preCompactTriggerManual))
	reqs := llm.requests()
	require.Len(t, reqs, 1)
	last := userTexts(reqs[0].Prompt[len(reqs[0].Prompt)-1:])[0]
	require.Equal(t, buildSummaryPrompt(nil, "keep the API decisions"), last)
	require.True(t, strings.HasSuffix(last, "\n\nAdditional Instructions:\nkeep the API decisions"), last)
	require.NotContains(t, buildSummaryPrompt(nil, " "), "Additional Instructions")
}
