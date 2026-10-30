package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/stretchr/testify/require"
)

// requestRecordingModel keeps every request it is sent and answers each
// with the next of its replies, "ok" once they run out. A reply that
// starts with "call " calls the tool it names instead, one that starts
// with "cut " is the rest stopped for length, as a llama.cpp server stops
// a reply that fills its window, and "hang" closes entered and waits for
// the request to be cancelled. Every answer reports usage as given. With
// refuseTools set, a request that lists tools fails with it; with
// tooLongOver set, one of more messages than it is refused as too long
// for the context. Title requests are answered but not kept: they are the
// agent's own side effect.
type requestRecordingModel struct {
	fakeLanguageModel
	mu          sync.Mutex
	calls       []fantasy.Call
	replies     []string
	usage       fantasy.Usage
	refuseTools error
	tooLongOver int
	entered     chan struct{}
}

func (m *requestRecordingModel) Stream(ctx context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	reply := "title"
	if !isTitleRequest(call) {
		m.mu.Lock()
		m.calls = append(m.calls, call)
		if m.refuseTools != nil && len(call.Tools) > 0 {
			m.mu.Unlock()
			return nil, m.refuseTools
		}
		if m.tooLongOver > 0 && len(call.Prompt) > m.tooLongOver {
			m.mu.Unlock()
			return nil, &fantasy.ProviderError{StatusCode: 400, Message: "the request exceeds the available context size, try increasing it"}
		}
		reply = "ok"
		if len(m.replies) > 0 {
			reply, m.replies = m.replies[0], m.replies[1:]
		}
		m.mu.Unlock()
	}
	return func(yield func(fantasy.StreamPart) bool) {
		if reply == "hang" {
			close(m.entered)
			<-ctx.Done()
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeError, Error: ctx.Err()})
			return
		}
		finish := fantasy.FinishReasonStop
		if text, ok := strings.CutPrefix(reply, "cut "); ok {
			reply, finish = text, fantasy.FinishReasonLength
		}
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
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: finish, Usage: m.usage})
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
// conversation again, twenty minutes for a full one. (170,000 of the
// 200,000-token window is past the point, 160,000, and leaves the summary
// its room; 190,000 would not, and the summary would go without them.)
func TestSummarize_RequestContinuesTheTurn(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{
		replies: []string{"done", "the summary"},
		usage:   fantasy.Usage{InputTokens: 170000, OutputTokens: 10},
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
		usage:   fantasy.Usage{InputTokens: 170000, OutputTokens: 10},
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
	// Most recent first: a skill, given as one, a file since deleted, a
	// directory, then f6 to f1.
	skillDir := filepath.Join(dir, "a-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: a-skill\n---\nDo it."), 0o600))
	a.fileTracker = &readRecorder{reads: append([]string{filepath.Join(skillDir, "SKILL.md"), filepath.Join(dir, "gone.txt"), dir}, reads...)}

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

// After a summary, the background jobs still running in the working
// directory are named, in Claude Code's words, so the agent neither loses
// track of them nor starts one again; one that ended is not.
func TestSummaryNamesTheJobsStillRunning(t *testing.T) {
	llm := &requestRecordingModel{
		replies: []string{"done", "the summary", "done again"},
		usage:   fantasy.Usage{InputTokens: 190000, OutputTokens: 10},
	}
	a, sessionID, _ := summaryRequestAgent(t, llm)
	dir := t.TempDir()
	a.cfg = config.NewTestStoreIn(&config.Config{Options: &config.Options{}}, dir)
	jobs := shell.GetBackgroundShellManager()
	running, err := jobs.Start(t.Context(), dir, nil, "sleep 30\n  # and wait", "a long job", 10)
	require.NoError(t, err)
	t.Cleanup(func() { _ = jobs.Kill(running.ID) })
	ended, err := jobs.Start(t.Context(), dir, nil, "true", "a short job", 10)
	require.NoError(t, err)
	t.Cleanup(func() { _ = jobs.Kill(ended.ID) })
	ended.Wait()

	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "go on"})
	require.NoError(t, err)
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "and then"})
	require.NoError(t, err)

	reqs := llm.requests()
	require.GreaterOrEqual(t, len(reqs), 3)
	want := "Background shell " + running.ID + " (\"a long job\") is still running (command, shown on one line: `sleep 30 # and wait`). " +
		"Do not start it again; to restart it, stop it with job_kill first. Call job_output with shell_id " + running.ID + " to read its output."
	require.Contains(t, userTexts(reqs[2].Prompt), want)
	require.NotContains(t, strings.Join(userTexts(reqs[2].Prompt), "\n"), "Background shell "+ended.ID+" ", "a job that ended is not named")
}

// After a summary, the skills the agent loaded are given again, read from
// their SKILL.md files, in Claude Code's words: the latest first, a long
// one cut. After the next summary they are given again too.
func TestSummaryGivesTheLoadedSkillsAgain(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{
		replies: []string{"done", "the summary", "done again", "the second summary", "done a third time"},
		usage:   fantasy.Usage{InputTokens: 190000, OutputTokens: 10},
	}
	a, sessionID, _ := summaryRequestAgent(t, llm)
	for _, name := range []string{"gnosh-hooks", "jq"} {
		path := skills.BuiltinPrefix + name + "/SKILL.md"
		meta, err := json.Marshal(tools.ViewResponseMetadata{FilePath: path, ResourceType: tools.ViewResourceSkill, ResourceName: name})
		require.NoError(t, err)
		_, err = a.messages.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: "call-" + name, Name: tools.ViewToolName, Input: `{"file_path":"` + path + `"}`, Finished: true},
		}})
		require.NoError(t, err)
		_, err = a.messages.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call-" + name, Name: tools.ViewToolName, Content: "<file>the skill</file>", Metadata: string(meta)},
		}})
		require.NoError(t, err)
	}
	skillMD := func(name string) string {
		data, err := fs.ReadFile(skills.BuiltinFS(), "builtin/"+name+"/SKILL.md")
		require.NoError(t, err)
		return string(data)
	}
	hooks := skillMD("gnosh-hooks")
	require.Greater(t, len(hooks), restoredSkillChars, "gnosh-hooks is long enough to be cut")
	want := skillsGivenAgain +
		"### Skill: jq\nPath: gnosh://skills/jq/SKILL.md\n\n" + skillMD("jq") + "\n\n---\n\n" +
		"### Skill: gnosh-hooks\nPath: gnosh://skills/gnosh-hooks/SKILL.md\n\n" + hooks[:restoredSkillChars-len(skillCut)] + skillCut
	given := func(req fantasy.Call) string {
		for _, text := range userTexts(req.Prompt) {
			if strings.HasPrefix(text, skillsGivenAgain) {
				return text
			}
		}
		return ""
	}

	_, err := a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "go on"})
	require.NoError(t, err)
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "and then"})
	require.NoError(t, err)
	require.NoError(t, a.Summarize(t.Context(), sessionID, "", fantasy.ProviderOptions{}, nil, preCompactTriggerManual))
	_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sessionID, Prompt: "and after that"})
	require.NoError(t, err)
	reqs := llm.requests()
	require.GreaterOrEqual(t, len(reqs), 5)
	require.Equal(t, want, given(reqs[2]), "after the first summary")
	require.Equal(t, want, given(reqs[4]), "after the second, from what the first gave")
}

// The skills given again after a summary stay within restoredSkillsChars:
// a skill that would take them past it is left out, and later, smaller
// ones still come.
func TestRestoredSkillsStayWithinTheirBudget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var msgs []message.Message
	for i := 1; i <= 7; i++ {
		size := restoredSkillChars - 100
		if i == 7 {
			size = 50
		}
		path := filepath.Join(dir, fmt.Sprintf("s%d", i), "SKILL.md")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(strings.Repeat(fmt.Sprint(i), size)), 0o600))
		meta, err := json.Marshal(tools.ViewResponseMetadata{FilePath: path, ResourceType: tools.ViewResourceSkill, ResourceName: fmt.Sprintf("s%d", i)})
		require.NoError(t, err)
		msgs = append(msgs, message.Message{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{Name: tools.ViewToolName, Content: "<file>...</file>", Metadata: string(meta)},
		}})
	}

	text := restoredSkillsText(msgs)
	var given []string
	for _, m := range skillHeading.FindAllStringSubmatch(text, -1) {
		given = append(given, m[1])
	}
	// s7 is the latest; then five of the large ones fit, and s1 does not.
	require.Equal(t, []string{"s7", "s6", "s5", "s4", "s3", "s2"}, given)
	require.LessOrEqual(t, len(text)-len(skillsGivenAgain), restoredSkillsChars+1000)
}

// A conversation too long for the context even without the system prompt
// and tools, as after a switch to a model with a smaller one, is
// summarized from its later turns: the earliest are left out, half at a
// time, until the rest fits, as Claude Code leaves them out, and the
// summary says what it does not cover. It was not summarized at all, and
// every turn after it was refused as too long.
func TestSummarize_TooLongEvenAloneLeavesOutTheEarliestPart(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{replies: []string{"the summary"}, tooLongOver: 8}
	a, sessionID, _ := summaryRequestAgent(t, llm)
	dataDir := t.TempDir()
	a.cfg = config.NewTestStore(&config.Config{Options: &config.Options{DataDirectory: dataDir}})
	for i := 1; i <= 8; i++ {
		for _, role := range []message.MessageRole{message.User, message.Assistant} {
			_, err := a.messages.Create(t.Context(), sessionID, message.CreateMessageParams{Role: role, Parts: []message.ContentPart{message.TextContent{Text: fmt.Sprintf("%s %d", role, i)}}})
			require.NoError(t, err)
		}
	}

	require.NoError(t, a.Summarize(t.Context(), sessionID, "", fantasy.ProviderOptions{}, nil, preCompactTriggerManual))
	reqs := llm.requests()
	last := reqs[len(reqs)-1]
	require.LessOrEqual(t, len(last.Prompt), 8, "the request that fit")
	// Refused with tools, refused without, refused from "user 5" on, and
	// fit from "user 7": half the turns left out at each cut.
	require.Len(t, reqs, 4)
	conversation := userTexts(last.Prompt)
	require.Equal(t, []string{"user 7", "user 8", buildSummaryPrompt(nil, "")}, conversation)
	require.Equal(t, "user 5", userTexts(reqs[2].Prompt)[0])
	for _, req := range reqs[2:] {
		require.Equal(t, fantasy.MessageRoleUser, withoutSystem(req.Prompt)[0].Role, "each cut starts at a user message")
	}

	sess, err := a.sessions.Get(t.Context(), sessionID)
	require.NoError(t, err)
	summary, err := a.messages.Get(t.Context(), sess.SummaryMessageID)
	require.NoError(t, err)
	require.Equal(t, "the summary\n\n"+fmt.Sprintf(earliestLeftOut, " (the full transcript named below still has it)"), summary.Content().Text)
	transcript, err := os.ReadFile(filepath.Join(dataDir, "transcripts", sessionID+"-"+sess.SummaryMessageID+".txt"))
	require.NoError(t, err)
	require.Contains(t, string(transcript), "User: user 1", "the transcript keeps what the summary left out")
}

// withoutSystem is msgs without their system messages.
func withoutSystem(msgs []fantasy.Message) []fantasy.Message {
	var out []fantasy.Message
	for _, msg := range msgs {
		if msg.Role != fantasy.MessageRoleSystem {
			out = append(out, msg)
		}
	}
	return out
}

// A long turn of tool calls, too long to summarize, is cut at one of the
// agent's messages, never between a call and its result, with a user
// message first that says the earlier part was left out: one turn has no
// user message to cut at, and the summary failed as it did before.
func TestSummarize_TooLongCutsInALongTurn(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{replies: []string{"the summary"}, tooLongOver: 8}
	a, sessionID, _ := summaryRequestAgent(t, llm)
	for i := 1; i <= 6; i++ {
		id := fmt.Sprintf("call-%d", i)
		_, err := a.messages.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{
			message.ToolCall{ID: id, Name: "probe", Input: probeInput, Finished: true},
		}})
		require.NoError(t, err)
		_, err = a.messages.Create(t.Context(), sessionID, message.CreateMessageParams{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: id, Name: "probe", Content: fmt.Sprintf("looked %d", i)},
		}})
		require.NoError(t, err)
	}

	require.NoError(t, a.Summarize(t.Context(), sessionID, "", fantasy.ProviderOptions{}, nil, preCompactTriggerManual))
	reqs := llm.requests()
	last := withoutSystem(reqs[len(reqs)-1].Prompt)
	require.LessOrEqual(t, len(reqs[len(reqs)-1].Prompt), 8, "the request that fit")
	require.Equal(t, []string{leftOutTurns, buildSummaryPrompt(nil, "")}, userTexts(last))
	require.Equal(t, fantasy.MessageRoleAssistant, last[1].Role, "cut at the agent's message")
	for i, msg := range last {
		if msg.Role == fantasy.MessageRoleTool {
			require.Equal(t, fantasy.MessageRoleAssistant, last[i-1].Role, "a result follows its call")
		}
	}
	sess, err := a.sessions.Get(t.Context(), sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, sess.SummaryMessageID)
}

// A cut that leaves no fewer messages is no cut: the summary stops trying.
func TestLaterPartShrinks(t *testing.T) {
	t.Parallel()
	user := fantasy.NewUserMessage("u")
	agent := fantasy.Message{Role: fantasy.MessageRoleAssistant}
	tool := fantasy.Message{Role: fantasy.MessageRoleTool}
	require.Nil(t, laterPart([]fantasy.Message{user, agent}))
	require.Nil(t, laterPart([]fantasy.Message{user, agent, tool}))
	require.Nil(t, laterPart([]fantasy.Message{user}))
	require.Len(t, laterPart([]fantasy.Message{user, agent, tool, agent, tool}), 3, "a placeholder and the last call and result")
	require.Equal(t, []fantasy.Message{user, agent}, laterPart([]fantasy.Message{agent, tool, user, agent}))
}

// addTurns adds n turns to the session, each a user message "user i" and
// the agent's "assistant i".
func addTurns(t *testing.T, a *sessionAgent, sessionID string, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		for _, role := range []message.MessageRole{message.User, message.Assistant} {
			_, err := a.messages.Create(t.Context(), sessionID, message.CreateMessageParams{Role: role, Parts: []message.ContentPart{message.TextContent{Text: fmt.Sprintf("%s %d", role, i)}}})
			require.NoError(t, err)
		}
	}
}

// The room a summary request must leave is half of what compacting at
// the default point leaves: a tenth of a small window, 10,000 tokens of
// a large one.
func TestSummaryRoom(t *testing.T) {
	t.Parallel()
	require.EqualValues(t, 13_107, summaryRoom(131_072), "gpt-oss's 128k window on the AI box")
	require.EqualValues(t, 10_000, summaryRoom(262_144), "Qwen's 256k")
	require.EqualValues(t, 3_276, summaryRoom(32_768))
}

// A summary request that leaves the model too little room to write the
// summary in is made smaller before it is sent: first without the system
// prompt and tools, then without the conversation's earliest part, cut at
// a user message. The provider's count raises estimates that fall short
// of it.
func TestFitSummary(t *testing.T) {
	t.Parallel()
	// Messages of 1,000 estimated tokens each, the role's included.
	text := func(role fantasy.MessageRole, tokens int) fantasy.Message {
		return fantasy.Message{Role: role, Content: []fantasy.MessagePart{fantasy.TextPart{Text: strings.Repeat("x", 4*tokens)}}}
	}
	var conversation []fantasy.Message
	for range 10 {
		conversation = append(conversation, text(fantasy.MessageRoleUser, 999), text(fantasy.MessageRoleAssistant, 997))
	}
	require.EqualValues(t, 20_000, estimateMessageTokens(conversation))

	// A 100,000-token window must leave a summary 10,000. With 5,000 of
	// system prompt and tools and 1,000 of instructions, the estimates
	// alone fit, and so does a count of 88,000, 3.52 times them.
	for _, counted := range []int64{0, 88_000} {
		bare, kept, cut := fitSummary(100_000, counted, 5_000, 1_000, conversation)
		require.False(t, bare, counted)
		require.False(t, cut, counted)
		require.Len(t, kept, 20, counted)
	}
	// 95,000 does not; without the system prompt and tools it does.
	bare, kept, cut := fitSummary(100_000, 95_000, 5_000, 1_000, conversation)
	require.True(t, bare)
	require.False(t, cut)
	require.Len(t, kept, 20)
	// 125,000, five times the estimates, as on the model before a /model
	// switch: the rest from the fourth message on, 17,000, fits five times
	// over, and the cut is at the user message after it.
	bare, kept, cut = fitSummary(100_000, 125_000, 5_000, 1_000, conversation)
	require.True(t, bare)
	require.True(t, cut)
	require.Equal(t, conversation[4:], kept)
	// No cut fits: the request goes bare and whole, to be cut as refused.
	bare, kept, cut = fitSummary(100_000, 10_000_000, 5_000, 1_000, conversation)
	require.True(t, bare)
	require.False(t, cut)
	require.Len(t, kept, 20)
	// With the window unknown it goes as it is.
	bare, kept, cut = fitSummary(0, 125_000, 5_000, 1_000, conversation)
	require.False(t, bare)
	require.False(t, cut)
	require.Len(t, kept, 20)

	// One long turn of tool calls is cut at the agent's message, after a
	// note that the earlier part was left out.
	turn := []fantasy.Message{text(fantasy.MessageRoleUser, 999)}
	for range 4 {
		turn = append(turn, text(fantasy.MessageRoleAssistant, 997), text(fantasy.MessageRoleTool, 999))
	}
	kept = fittingPart(turn, func(tokens int64) bool { return tokens <= 4_000 })
	require.Equal(t, append([]fantasy.Message{fantasy.NewUserMessage(leftOutTurns)}, turn[5:]...), kept)
	require.Nil(t, fittingPart(turn, func(tokens int64) bool { return tokens <= 500 }), "not even the last message fits")
}

// A summary is cut short by the window when it stopped for length with
// it and its request filling the window, as a llama.cpp server stops a
// reply that runs out of room; one stopped at a limit on its output,
// far from the window, is not.
func TestFilledWindow(t *testing.T) {
	t.Parallel()
	result := func(finish fantasy.FinishReason, usage fantasy.Usage) *fantasy.AgentResult {
		return &fantasy.AgentResult{Steps: []fantasy.StepResult{{Response: fantasy.Response{FinishReason: finish, Usage: usage}}}}
	}
	// The AI box's: 131,071 tokens of gpt-oss's 131,072, the summary 2,254
	// tokens in.
	full := fantasy.Usage{InputTokens: 1_017, CacheReadTokens: 127_800, OutputTokens: 2_254}
	require.True(t, filledWindow(result(fantasy.FinishReasonLength, full), 131_072))
	require.False(t, filledWindow(result(fantasy.FinishReasonStop, full), 131_072), "it ended on its own")
	limited := fantasy.Usage{InputTokens: 50_000, OutputTokens: 4_096}
	require.False(t, filledWindow(result(fantasy.FinishReasonLength, limited), 131_072), "an output limit")
	require.True(t, filledWindow(result(fantasy.FinishReasonLength, fantasy.Usage{}), 131_072), "usage not known")
	require.True(t, filledWindow(result(fantasy.FinishReasonLength, limited), 0), "the window not known")
	require.False(t, filledWindow(nil, 131_072))
	require.False(t, filledWindow(&fantasy.AgentResult{}, 131_072))
}

// A summary request that would leave the model too little room to write
// the summary in, as after a /model switch to a model with a smaller
// window, is made smaller before it goes: one request, without the
// system prompt and tools, from a later user message on, and the summary
// says what it does not cover. A llama.cpp server took such a request
// whole and stopped the summary where its window ran out.
func TestSummarize_LeavesRoomForTheSummary(t *testing.T) {
	t.Parallel()
	llm := &requestRecordingModel{replies: []string{"the summary"}}
	a, sessionID, _ := summaryRequestAgent(t, llm)
	addTurns(t, a, sessionID, 20)
	// Counted at 360,000 on a model with a 400,000-token window, under its
	// point, then switched to one of 200,000.
	_, err := a.sessions.UpdateUsage(t.Context(), sessionID, 360_000, 0, 0, false)
	require.NoError(t, err)
	a.SetModels(Model{Model: llm, CatwalkCfg: catwalk.Model{ContextWindow: 200_000, DefaultMaxTokens: 1000}}, a.smallModel.Get())

	require.NoError(t, a.Summarize(t.Context(), sessionID, "", fantasy.ProviderOptions{}, nil, preCompactTriggerManual))
	reqs := llm.requests()
	require.Len(t, reqs, 1, "made to fit before it was sent, not after a refusal")
	require.Empty(t, reqs[0].Tools)
	require.Equal(t, []string{"prefix"}, systemTexts(reqs[0].Prompt), "only the prefix, which goes in any case")
	sent := withoutSystem(reqs[0].Prompt)
	require.Equal(t, fantasy.MessageRoleUser, sent[0].Role, "cut at a user message")
	texts := userTexts(sent)
	require.NotContains(t, texts, "user 1", "the earliest turns are left out")
	require.Contains(t, texts, "user 20")
	require.Equal(t, buildSummaryPrompt(nil, ""), texts[len(texts)-1])

	sess, err := a.sessions.Get(t.Context(), sessionID)
	require.NoError(t, err)
	summary, err := a.messages.Get(t.Context(), sess.SummaryMessageID)
	require.NoError(t, err)
	require.Equal(t, "the summary\n\n"+fmt.Sprintf(earliestLeftOut, ""), summary.Content().Text)
}

// A summary the model's window cut short, as a llama.cpp server stops a
// reply that fills it (finish reason "length"), is asked for again with
// less sent: without the system prompt and tools, then without the
// earliest turns. Only a whole one replaces the conversation. gpt-oss on
// the AI box stopped one 2,254 tokens in, and it was kept as if whole:
// the session went on from half a summary.
func TestSummarize_CutShortIsAskedAgainWithLess(t *testing.T) {
	t.Parallel()
	// The whole 200,000-token window, request and reply.
	full := fantasy.Usage{InputTokens: 198_000, OutputTokens: 2_000}
	llm := &requestRecordingModel{replies: []string{"cut half a sum", "cut half a summ", "the summary"}, usage: full}
	a, sessionID, _ := summaryRequestAgent(t, llm)
	addTurns(t, a, sessionID, 8)

	require.NoError(t, a.Summarize(t.Context(), sessionID, "", fantasy.ProviderOptions{}, nil, preCompactTriggerManual))
	reqs := llm.requests()
	require.Len(t, reqs, 3)
	require.NotEmpty(t, reqs[0].Tools)
	require.Empty(t, reqs[1].Tools, "then without the system prompt and tools")
	require.Equal(t, withoutProviderOptions(withoutSystem(reqs[0].Prompt)), withoutProviderOptions(withoutSystem(reqs[1].Prompt)))
	require.Less(t, len(reqs[2].Prompt), len(reqs[1].Prompt), "then without the earliest turns")
	sess, err := a.sessions.Get(t.Context(), sessionID)
	require.NoError(t, err)
	summary, err := a.messages.Get(t.Context(), sess.SummaryMessageID)
	require.NoError(t, err)
	require.Equal(t, "the summary\n\n"+fmt.Sprintf(earliestLeftOut, ""), summary.Content().Text, "nothing of the summaries cut short")
	require.Equal(t, message.FinishReasonEndTurn, summary.FinishReason())

	// Stopped at a limit on its output, far from the window, a summary is
	// kept as it is.
	llm = &requestRecordingModel{replies: []string{"cut a long summary"}, usage: fantasy.Usage{InputTokens: 1_000, OutputTokens: 4_096}}
	a, sessionID, _ = summaryRequestAgent(t, llm)
	require.NoError(t, a.Summarize(t.Context(), sessionID, "", fantasy.ProviderOptions{}, nil, preCompactTriggerManual))
	require.Len(t, llm.requests(), 1)
	sess, err = a.sessions.Get(t.Context(), sessionID)
	require.NoError(t, err)
	summary, err = a.messages.Get(t.Context(), sess.SummaryMessageID)
	require.NoError(t, err)
	require.Equal(t, "a long summary", summary.Content().Text)

	// Cut short however little is sent, there is no summary: the
	// conversation is kept, and none of a summary is left to be sent as
	// the agent's words.
	llm = &requestRecordingModel{replies: slices.Repeat([]string{"cut half"}, 20), usage: full}
	a, sessionID, _ = summaryRequestAgent(t, llm)
	addTurns(t, a, sessionID, 8)
	err = a.Summarize(t.Context(), sessionID, "", fantasy.ProviderOptions{}, nil, preCompactTriggerManual)
	require.ErrorContains(t, err, "did not fit")
	sess, err = a.sessions.Get(t.Context(), sessionID)
	require.NoError(t, err)
	require.Empty(t, sess.SummaryMessageID)
	msgs, err := a.messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	last := msgs[len(msgs)-1]
	require.True(t, last.IsSummaryMessage)
	require.Empty(t, last.Content().Text)
	require.Equal(t, message.FinishReasonError, last.FinishReason())
}
