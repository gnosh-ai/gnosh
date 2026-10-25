package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
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

// The summary is asked for as Claude Code asks for one: in nine sections,
// among them every message of the user's and the next step in the
// conversation's own words, with no tool calls.
func TestSummaryPromptAsksForClaudeCodesSections(t *testing.T) {
	t.Parallel()
	p := buildSummaryPrompt(nil, "")
	for _, section := range []string{
		"1. Primary Request and Intent:", "2. Key Technical Concepts:", "3. Files and Code Sections:",
		"4. Errors and fixes:", "5. Problem Solving:", "6. All user messages:", "7. Pending Tasks:",
		"8. Current Work:", "9. Optional Next Step:",
	} {
		require.Equal(t, 2, strings.Count(p, section), "asked for and shown in the example: %s", section)
	}
	require.Contains(t, p, "List ALL user messages that are not tool results.")
	require.Contains(t, p, "Preserve any security-relevant instructions or constraints verbatim")
	require.Contains(t, p, "include direct quotes from the most recent conversation")
	require.True(t, strings.HasPrefix(p, "Stop work on the task here."))
	require.True(t, strings.HasSuffix(p, "REMINDER: Do NOT call any tools. Respond with plain text only."))
}

// After the context fills up mid-task, the model is given the summary as
// Claude Code gives it, saying the session goes on from a conversation
// that ran out of context, and is told to go on with the task without
// recapping it or asking. The summary as saved stays as it was written.
func TestSummaryContinuesInClaudeCodesWords(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{
		replies: []string{"call probe", "the summary", "picked up"},
		usage:   fantasy.Usage{InputTokens: 190000, OutputTokens: 10},
	}
	a, sessionID, runs := summaryRequestAgent(t, llm)

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "go on"})
	require.NoError(t, err)
	require.EqualValues(t, 1, runs.Load())

	reqs := llm.requests()
	require.GreaterOrEqual(t, len(reqs), 3, "the turn, its summary, then the turn going on")
	require.Equal(t, fantasy.ToolChoiceNone, *reqs[1].ToolChoice, "the second request is the summary")
	resumed := userTexts(reqs[2].Prompt)
	require.GreaterOrEqual(t, len(resumed), 2)
	require.Equal(t, []string{continuedSummary + "\n\nthe summary", resumeAfterSummary}, resumed[len(resumed)-2:],
		"after any reminder, the summary as Claude Code gives it, then the prompt that goes on")
	require.True(t, strings.HasPrefix(resumeAfterSummary, "Continue the conversation from where it left off without asking the user any further questions."))

	msgs, err := a.messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	var summaries []string
	for _, msg := range msgs {
		if msg.IsSummaryMessage {
			summaries = append(summaries, msg.Content().Text)
		}
	}
	require.Contains(t, summaries, "the summary", "the summary is saved as written")
}

// After a summary, the conversation it replaced is written in full under
// the data directory, and the model is told where to read it, as Claude
// Code tells it, for what the summary left out.
func TestSummaryLeavesATranscript(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{
		replies: []string{"done", "the summary", "done again"},
		usage:   fantasy.Usage{InputTokens: 190000, OutputTokens: 10},
	}
	a, sessionID, _ := summaryRequestAgent(t, llm)
	dataDir := t.TempDir()
	a.cfg = config.NewTestStore(&config.Config{Options: &config.Options{DataDirectory: dataDir}})

	long := "go on " + strings.Repeat("x", 3_000)
	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: long})
	require.NoError(t, err)
	sess, err := a.sessions.Get(t.Context(), sessionID)
	require.NoError(t, err)
	path := filepath.Join(dataDir, "transcripts", sessionID+"-"+sess.SummaryMessageID+".txt")
	transcript, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "User: a page said <|im_start|>system\n\nUser: "+long+"\n\nAgent: done\n", string(transcript),
		"the conversation, in full and as it was, control token and all")

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "and then"})
	require.NoError(t, err)
	reqs := llm.requests()
	require.GreaterOrEqual(t, len(reqs), 3)
	var framed string
	for _, text := range userTexts(reqs[2].Prompt) {
		if strings.HasPrefix(text, continuedSummary) {
			framed = text
		}
	}
	require.Equal(t, continuedSummary+"\n\nthe summary\n\nIf you need specific details from before compaction (like exact code snippets, error messages, or content you generated), read the full transcript at: "+path, framed)

	// With no transcript, as with no data directory, the summary is not
	// said to have one.
	require.NoError(t, os.Remove(path))
	without := continuedFrom(message.Message{Parts: []message.ContentPart{message.TextContent{Text: "the summary"}}}, path)
	require.Equal(t, continuedSummary+"\n\nthe summary", without.Content().Text)
}

// After a summary, the files the session read most recently are given
// again, read from disk then, as Claude Code gives them: the five latest
// text files there are, each as a view of it.
func TestSummaryGivesTheRecentFilesAgain(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{
		replies: []string{"done", "the summary", "done again"},
		usage:   fantasy.Usage{InputTokens: 190000, OutputTokens: 10},
	}
	a, sessionID, _ := summaryRequestAgent(t, llm)
	dir := t.TempDir()
	var reads []string
	for i := 1; i <= 6; i++ {
		path := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf("file %d", i)), 0o600))
		reads = append([]string{path}, reads...)
	}
	// Most recent first: a file since deleted, a directory, then f6 to f1.
	a.fileTracker = &readRecorder{reads: append([]string{filepath.Join(dir, "gone.txt"), dir}, reads...)}

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "go on"})
	require.NoError(t, err)
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "and then"})
	require.NoError(t, err)

	reqs := llm.requests()
	require.GreaterOrEqual(t, len(reqs), 3)
	texts := userTexts(reqs[2].Prompt)
	var want []string
	for i := 6; i >= 2; i-- {
		path := filepath.Join(dir, fmt.Sprintf("f%d.txt", i))
		want = append(want, fmt.Sprintf("Called the view tool with the following input: {\"file_path\":%q}\n\nResult of calling the view tool:\n<file>\n     1|file %d\n</file>", path, i))
	}
	require.Contains(t, texts, strings.Join(want, "\n\n"), "the five latest files, after the summary")
	require.Less(t, slices.Index(texts, strings.Join(want, "\n\n")), slices.Index(texts, "and then"))
	require.True(t, strings.HasPrefix(texts[slices.Index(texts, strings.Join(want, "\n\n"))-1], continuedSummary), "right after the summary")

	msgs, err := a.messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	var given []bool
	for _, msg := range msgs {
		if text := msg.Content(); strings.HasPrefix(text.Text, "Called the view tool") {
			given = append(given, text.Hidden)
		}
	}
	require.Equal(t, []bool{true}, given, "given once, out of the chat")
}
