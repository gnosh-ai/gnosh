package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	gnovmtools "github.com/charmbracelet/crush/internal/gnovm/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// The model is reminded of mem when neither a call to mem nor a reminder is
// among its last ten replies, as Claude Code counts them for its todo list:
// the reply that called mem does not count after it, the replies after a
// reminder do. A call to another machine is no call to mem, and a prompt
// that quotes the reminder is no reminder.
func TestMemReminderDue(t *testing.T) {
	t.Parallel()

	prompt := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "count"}}}
	reply := message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "counting"}}}
	memCall := message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "c", Name: gnovmtools.RunToolName, Input: `{"name": "mem", "code": "mem.Show()"}`, Finished: true}}}
	scratch := message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "s", Name: gnovmtools.RunToolName, Input: `{"code": "1 + 1"}`, Finished: true}}}
	reminder := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: memReminder(nil, ""), Hidden: true}}}
	quoted := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "what is " + memReminder(nil, "")}}}
	replies := func(n int) []message.Message { return slices.Repeat([]message.Message{reply}, n) }
	for _, c := range []struct {
		name string
		msgs []message.Message
		due  bool
	}{
		{"no replies", []message.Message{prompt}, false},
		{"nine replies", slices.Concat([]message.Message{prompt}, replies(9)), false},
		{"ten replies", slices.Concat([]message.Message{prompt}, replies(10)), true},
		{"a call to mem nine replies back", slices.Concat([]message.Message{prompt, memCall}, replies(9)), false},
		{"a call to mem ten replies back", slices.Concat([]message.Message{prompt, memCall}, replies(10)), true},
		{"a call to a scratch machine nine replies back", slices.Concat([]message.Message{prompt}, replies(1), []message.Message{scratch}, replies(9)), true},
		{"a reminder nine replies back", slices.Concat(replies(12), []message.Message{reminder}, replies(9)), false},
		{"a reminder ten replies back", slices.Concat(replies(12), []message.Message{reminder}, replies(10)), true},
		{"a prompt quoting it nine replies back", slices.Concat(replies(12), []message.Message{quoted}, replies(9)), true},
	} {
		require.Equal(t, c.due, memReminderDue(c.msgs), c.name)
	}
}

// The reminder is in the shape of Claude Code's of its todo list, naming
// mem and how to plan in it, with where the session's goal stands when
// there is one.
func TestMemReminderText(t *testing.T) {
	t.Parallel()

	require.Equal(t, "<system_reminder>mem hasn't been used recently. If you're working on a task of two or more steps, plan it in mem (gnovm_run name \"mem\": mem.Goal(\"…\").Steps(\"…\", \"…\")) and mark each step done as it finishes; if it is planned, carry on from its next step. This is just a gentle reminder - ignore if not applicable.\n</system_reminder>", memReminder(nil, ""))
	withList := memReminder([]session.Todo{
		{Content: "Write the test", Status: session.TodoStatusCompleted},
		{Content: "Land it", Status: session.TodoStatusInProgress},
	}, "")
	require.True(t, strings.HasSuffix(withList, ".\n\n\nHere is where your goal in mem stands:\n\n[1. [completed] Write the test\n2. [in_progress] Land it]</system_reminder>"), withList)
}

// countingModel calls the count tool with a new number on each of its
// first steps steps, then answers, and keeps the prompt of each request.
// onStep, when set, is called with each request's number.
type countingModel struct {
	fakeLanguageModel
	steps   int
	onStep  func(n int)
	mu      sync.Mutex
	prompts []fantasy.Prompt
}

func (m *countingModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, slices.Clone(call.Prompt))
	n := len(m.prompts)
	m.mu.Unlock()
	if m.onStep != nil {
		m.onStep(n)
	}
	return func(yield func(fantasy.StreamPart) bool) {
		if n > m.steps {
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "t"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "t", Delta: "counted"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "t"})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
			return
		}
		id, input := fmt.Sprintf("call-%d", n), fmt.Sprintf(`{"n":%d}`, n)
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputStart, ID: id, ToolCallName: "count"})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputDelta, ID: id, Delta: input})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolInputEnd, ID: id})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeToolCall, ID: id, ToolCallName: "count", ToolCallInput: input})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonToolCalls})
	}, nil
}

type countParams struct {
	N int `json:"n"`
}

// Ten replies with no call to mem put the reminder, with where the
// session's goal stands, before the model's next reply, and not again in
// the ten after it, while the turn's later steps keep it where it was put.
// The reminder is kept as a hidden user message, so the conversation
// reads the same when it is rebuilt and the chat does not show it. A
// model without gnovm_run is not reminded.
func TestRunRemindsTheModelOfMem(t *testing.T) {
	for _, withTodos := range []bool{true, false} {
		t.Run(fmt.Sprintf("gnovm_run %v", withTodos), func(t *testing.T) {
			env := testEnv(t)
			llm := &countingModel{steps: 12}
			agentTools := []fantasy.AgentTool{fantasy.NewAgentTool("count", "Count.",
				func(_ context.Context, p countParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
					return fantasy.NewTextResponse(fmt.Sprint(p.N)), nil
				})}
			if withTodos {
				agentTools = append(agentTools, fantasy.NewAgentTool(gnovmtools.RunToolName, "Run Gno.",
					func(context.Context, countParams, fantasy.ToolCall) (fantasy.ToolResponse, error) {
						return fantasy.NewTextResponse("ok"), nil
					}))
			}
			model := Model{Model: llm, CatwalkCfg: catwalk.Model{ContextWindow: 200000, DefaultMaxTokens: 1000}}
			a := NewSessionAgent(SessionAgentOptions{
				LargeModel:           model,
				SmallModel:           model,
				SystemPrompt:         "test",
				IsYolo:               true,
				DisableAutoSummarize: true,
				Sessions:             env.sessions,
				Messages:             env.messages,
				Tools:                agentTools,
			}).(*sessionAgent)
			sess, err := env.sessions.Create(t.Context(), "counting")
			require.NoError(t, err)
			// A user message already there skips title generation, which
			// would call the same model from another goroutine.
			_, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
				Role:  message.User,
				Parts: []message.ContentPart{message.TextContent{Text: "earlier prompt"}},
			})
			require.NoError(t, err)
			_, err = env.sessions.SetTodos(t.Context(), sess.ID, []session.Todo{{Content: "Count to twelve", Status: session.TodoStatusInProgress}})
			require.NoError(t, err)

			_, err = a.Run(t.Context(), SessionAgentCall{SessionID: sess.ID, Prompt: "count", NonInteractive: true})
			require.NoError(t, err)

			require.Len(t, llm.prompts, 13)
			for i, prompt := range llm.prompts {
				var reminders int
				for _, msg := range prompt {
					for _, part := range msg.Content {
						if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok && msg.Role == fantasy.MessageRoleUser && strings.Contains(text.Text, memReminderOpening) {
							reminders++
						}
					}
				}
				if withTodos && i >= memReminderTurns {
					require.Equal(t, 1, reminders, "request %d", i+1)
				} else {
					require.Zero(t, reminders, "request %d", i+1)
				}
			}
			if !withTodos {
				return
			}
			last := llm.prompts[memReminderTurns][len(llm.prompts[memReminderTurns])-1]
			require.Equal(t, fantasy.MessageRoleUser, last.Role, "the reminder comes right before the eleventh reply")
			text, _ := fantasy.AsMessagePart[fantasy.TextPart](last.Content[0])
			require.Contains(t, text.Text, "[1. [in_progress] Count to twelve]")

			msgs, err := env.messages.List(t.Context(), sess.ID)
			require.NoError(t, err)
			var kept []message.Message
			for _, msg := range msgs {
				if content := msg.Content(); msg.Role == message.User && strings.Contains(content.Text, memReminderOpening) {
					require.True(t, content.Hidden, "the chat does not show it")
					kept = append(kept, msg)
				}
			}
			require.Len(t, kept, 1, "kept once")
		})
	}
}

// The count starts again at the session's last summary, as the model's
// context does, and a sub-agent is not reminded.
func TestRemindOfMemCountsFromTheSummary(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sess, err := env.sessions.Create(t.Context(), "summarized")
	require.NoError(t, err)
	reply := func(n int, text string) message.Message {
		var msg message.Message
		for range n {
			msg, err = env.messages.Create(t.Context(), sess.ID, message.CreateMessageParams{
				Role:  message.Assistant,
				Parts: []message.ContentPart{message.TextContent{Text: text}},
			})
			require.NoError(t, err)
		}
		return msg
	}
	runTool := fantasy.NewAgentTool(gnovmtools.RunToolName, "Run Gno.",
		func(context.Context, countParams, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse("ok"), nil
		})
	remind := func(a *sessionAgent) []fantasy.Message {
		t.Helper()
		reminder, err := a.remindOfMem(t.Context(), sess.ID, []fantasy.AgentTool{runTool})
		require.NoError(t, err)
		return reminder
	}
	main := &sessionAgent{sessions: env.sessions, messages: env.messages}
	sub := &sessionAgent{sessions: env.sessions, messages: env.messages, isSubAgent: true}

	reply(12, "counting")
	summary := reply(1, "the summary")
	_, err = env.sessions.UpdateSummary(t.Context(), sess.ID, summary.ID, 0, 0, false)
	require.NoError(t, err)
	reply(9, "counting")
	require.Empty(t, remind(main), "nine replies since the summary")
	reply(1, "counting")
	require.Empty(t, remind(sub), "a sub-agent is not reminded")
	require.NotEmpty(t, remind(main), "ten replies since the summary")
}
