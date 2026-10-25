package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
)

// todoReminderTurns is how many of the model's replies may go by without
// a todos call, and without a todo reminder, before it is reminded of its
// todo list, as Claude Code reminds it of TodoWrite.
const todoReminderTurns = 10

// todoReminderOpening opens Claude Code's reminder, with the tool named
// as klash names it. It is how the reminder is known in the conversation.
const todoReminderOpening = "The todos tool hasn't been used recently."

// todoReminderDue reports whether the model is to be reminded of its todo
// list before its next reply: neither a todos call nor a todo reminder is
// in the last todoReminderTurns of its replies in the conversation msgs,
// as Claude Code counts them.
func todoReminderDue(msgs []message.Message) bool {
	sinceTodos, sinceReminder := 0, 0
	todosFound, reminderFound := false, false
	for i := len(msgs) - 1; i >= 0 && !(todosFound && reminderFound); i-- {
		switch msg := msgs[i]; msg.Role {
		case message.Assistant:
			if !todosFound && slices.ContainsFunc(msg.ToolCalls(), func(call message.ToolCall) bool { return call.Name == tools.TodosToolName }) {
				todosFound = true
			}
			if !todosFound {
				sinceTodos++
			}
			if !reminderFound {
				sinceReminder++
			}
		case message.User:
			if !reminderFound && isTodoReminder(msg) {
				reminderFound = true
			}
		}
	}
	return sinceTodos >= todoReminderTurns && sinceReminder >= todoReminderTurns
}

// isTodoReminder reports whether msg is a todo reminder klash put in.
func isTodoReminder(msg message.Message) bool {
	text := msg.Content()
	return text.Hidden && strings.Contains(text.Text, todoReminderOpening)
}

// todoReminder is Claude Code's reminder of the todo list, with todos, the
// list the session has, when it has one.
func todoReminder(todos []session.Todo) string {
	text := todoReminderOpening + " If you're working on tasks that would benefit from tracking progress, consider using the todos tool to track progress. Also consider cleaning up the todo list if has become stale and no longer matches what you are working on. Only use it if it's relevant to the current work. This is just a gentle reminder - ignore if not applicable.\n"
	if len(todos) > 0 {
		lines := make([]string, len(todos))
		for i, todo := range todos {
			lines[i] = fmt.Sprintf("%d. [%s] %s", i+1, todo.Status, todo.Content)
		}
		text += "\n\nHere are the existing contents of your todo list:\n\n[" + strings.Join(lines, "\n") + "]"
	}
	return "<system_reminder>" + text + "</system_reminder>"
}

// remindOfTodos is the todo reminder to put before the model's next reply,
// when one is due and the model has the todos tool, as Claude Code puts
// one in: kept as a hidden user message, so the conversation reads the
// same when it is rebuilt. It is counted on the conversation kept since
// the session's last summary, as the model reads it, since the steps of a
// turn do not carry what a step put before them. A sub-agent is not
// reminded.
func (a *sessionAgent) remindOfTodos(ctx context.Context, sessionID string, requestTools []fantasy.AgentTool) ([]fantasy.Message, error) {
	if a.isSubAgent || !slices.ContainsFunc(requestTools, func(tool fantasy.AgentTool) bool { return tool.Info().Name == tools.TodosToolName }) {
		return nil, nil
	}
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	msgs, err := a.getSessionMessages(ctx, sess)
	if err != nil {
		return nil, err
	}
	if !todoReminderDue(msgs) {
		return nil, nil
	}
	msg, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: todoReminder(sess.Todos), Hidden: true}},
	})
	if err != nil {
		return nil, err
	}
	return msg.ToAIMessage(), nil
}
