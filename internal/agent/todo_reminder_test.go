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
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// The model is reminded of its todo list when neither a todos call nor a
// reminder is among its last ten replies, as Claude Code counts them: the
// reply that called todos does not count after it, the replies after a
// reminder do. A prompt that quotes the reminder is no reminder.
func TestTodoReminderDue(t *testing.T) {
	t.Parallel()

	prompt := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "count"}}}
	reply := message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "counting"}}}
	todos := message.Message{Role: message.Assistant, Parts: []message.ContentPart{message.ToolCall{ID: "c", Name: tools.TodosToolName, Finished: true}}}
	reminder := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: todoReminder(nil), Hidden: true}}}
	quoted := message.Message{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "what is " + todoReminder(nil)}}}
	replies := func(n int) []message.Message { return slices.Repeat([]message.Message{reply}, n) }
	for _, c := range []struct {
		name string
		msgs []message.Message
		due  bool
	}{
		{"no replies", []message.Message{prompt}, false},
		{"nine replies", slices.Concat([]message.Message{prompt}, replies(9)), false},
		{"ten replies", slices.Concat([]message.Message{prompt}, replies(10)), true},
		{"a todos call nine replies back", slices.Concat([]message.Message{prompt, todos}, replies(9)), false},
		{"a todos call ten replies back", slices.Concat([]message.Message{prompt, todos}, replies(10)), true},
		{"a reminder nine replies back", slices.Concat(replies(12), []message.Message{reminder}, replies(9)), false},
		{"a reminder ten replies back", slices.Concat(replies(12), []message.Message{reminder}, replies(10)), true},
		{"a prompt quoting it nine replies back", slices.Concat(replies(12), []message.Message{quoted}, replies(9)), true},
	} {
		require.Equal(t, c.due, todoReminderDue(c.msgs), c.name)
	}
}

// The reminder is Claude Code's, naming the todos tool, with the session's
// todo list in its format when there is one.
func TestTodoReminderText(t *testing.T) {
	t.Parallel()

	require.Equal(t, "<system_reminder>The todos tool hasn't been used recently. If you're working on tasks that would benefit from tracking progress, consider using the todos tool to track progress. Also consider cleaning up the todo list if has become stale and no longer matches what you are working on. Only use it if it's relevant to the current work. This is just a gentle reminder - ignore if not applicable.\n</system_reminder>", todoReminder(nil))
	withList := todoReminder([]session.Todo{
		{Content: "Write the test", Status: session.TodoStatusCompleted},
		{Content: "Land it", Status: session.TodoStatusInProgress},
	})
	require.True(t, strings.HasSuffix(withList, ".\n\n\nHere are the existing contents of your todo list:\n\n[1. [completed] Write the test\n2. [in_progress] Land it]</system_reminder>"), withList)
}

// countingModel calls the count tool with a new number on each of its
// first steps steps, then answers, and keeps the prompt of each request.
type countingModel struct {
	fakeLanguageModel
	steps   int
	mu      sync.Mutex
	prompts []fantasy.Prompt
}

func (m *countingModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, slices.Clone(call.Prompt))
	n := len(m.prompts)
	m.mu.Unlock()
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

// Ten replies with no todos call put Claude Code's reminder, with the
// session's todo list, before the model's next reply, and not again in
// the ten after it. The reminder is kept as a hidden user message, so the
// conversation reads the same when it is rebuilt and the chat does not
// show it; the turn's later steps do not carry it, as they do not carry
// prompts folded in. A model without the todos tool is not reminded.
func TestRunRemindsTheModelOfItsTodoList(t *testing.T) {
	for _, withTodos := range []bool{true, false} {
		t.Run(fmt.Sprintf("todos tool %v", withTodos), func(t *testing.T) {
			env := testEnv(t)
			llm := &countingModel{steps: 12}
			agentTools := []fantasy.AgentTool{fantasy.NewAgentTool("count", "Count.",
				func(_ context.Context, p countParams, _ fantasy.ToolCall) (fantasy.ToolResponse, error) {
					return fantasy.NewTextResponse(fmt.Sprint(p.N)), nil
				})}
			if withTodos {
				agentTools = append(agentTools, fantasy.NewAgentTool(tools.TodosToolName, "Keep a todo list.",
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
						if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok && msg.Role == fantasy.MessageRoleUser && strings.Contains(text.Text, todoReminderOpening) {
							reminders++
						}
					}
				}
				if withTodos && i == todoReminderTurns {
					require.Equal(t, 1, reminders, "request %d", i+1)
				} else {
					require.Zero(t, reminders, "request %d", i+1)
				}
			}
			if !withTodos {
				return
			}
			last := llm.prompts[todoReminderTurns][len(llm.prompts[todoReminderTurns])-1]
			require.Equal(t, fantasy.MessageRoleUser, last.Role, "the reminder comes right before the eleventh reply")
			text, _ := fantasy.AsMessagePart[fantasy.TextPart](last.Content[0])
			require.Contains(t, text.Text, "[1. [in_progress] Count to twelve]")

			msgs, err := env.messages.List(t.Context(), sess.ID)
			require.NoError(t, err)
			var kept []message.Message
			for _, msg := range msgs {
				if content := msg.Content(); msg.Role == message.User && strings.Contains(content.Text, todoReminderOpening) {
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
func TestRemindOfTodosCountsFromTheSummary(t *testing.T) {
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
	todosTool := fantasy.NewAgentTool(tools.TodosToolName, "Keep a todo list.",
		func(context.Context, countParams, fantasy.ToolCall) (fantasy.ToolResponse, error) {
			return fantasy.NewTextResponse("ok"), nil
		})
	remind := func(a *sessionAgent) []fantasy.Message {
		t.Helper()
		reminder, err := a.remindOfTodos(t.Context(), sess.ID, []fantasy.AgentTool{todosTool})
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
