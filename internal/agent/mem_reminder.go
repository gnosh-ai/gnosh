package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/gnovm"
	gnovmtools "github.com/charmbracelet/crush/internal/gnovm/tools"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
)

// memReminderTurns is how many of the model's replies may go by without a
// call to mem, and without a mem reminder, before it is reminded of mem, as
// Claude Code reminds its model of TodoWrite.
const memReminderTurns = 10

// memReminderOpening opens the reminder. It is how the reminder is known in
// the conversation.
const memReminderOpening = "mem hasn't been used recently."

// isMemCall reports whether call ran code in mem.
func isMemCall(call message.ToolCall) bool {
	if call.Name != gnovmtools.RunToolName {
		return false
	}
	var input struct {
		Name string `json:"name"`
	}
	return json.Unmarshal([]byte(call.Input), &input) == nil && input.Name == gnovm.MemVM
}

// memReminderDue reports whether the model is to be reminded of mem before
// its next reply: neither a call to mem nor a mem reminder is in the last
// memReminderTurns of its replies in the conversation msgs, as Claude Code
// counts them.
func memReminderDue(msgs []message.Message) bool {
	sinceMem, sinceReminder := 0, 0
	memFound, reminderFound := false, false
	for i := len(msgs) - 1; i >= 0 && !(memFound && reminderFound); i-- {
		switch msg := msgs[i]; msg.Role {
		case message.Assistant:
			if !memFound && slices.ContainsFunc(msg.ToolCalls(), isMemCall) {
				memFound = true
			}
			if !memFound {
				sinceMem++
			}
			if !reminderFound {
				sinceReminder++
			}
		case message.User:
			if !reminderFound && isMemReminder(msg) {
				reminderFound = true
			}
		}
	}
	return sinceMem >= memReminderTurns && sinceReminder >= memReminderTurns
}

// isMemReminder reports whether msg is a mem reminder gnosh put in.
func isMemReminder(msg message.Message) bool {
	text := msg.Content()
	return text.Hidden && strings.Contains(text.Text, memReminderOpening)
}

// memReminder is the reminder of mem, in the shape of Claude Code's of its
// todo list, with todos, where the session's goal in mem stands, when it
// has one.
func memReminder(todos []session.Todo) string {
	text := memReminderOpening + ` If you're working on a task of two or more steps, plan it in mem (gnovm_run name "mem": mem.Goal("…").Steps("…", "…")) and mark each step done as it finishes; if it is planned, carry on from its next step. This is just a gentle reminder - ignore if not applicable.` + "\n"
	if len(todos) > 0 {
		lines := make([]string, len(todos))
		for i, todo := range todos {
			lines[i] = fmt.Sprintf("%d. [%s] %s", i+1, todo.Status, todo.Content)
		}
		text += "\n\nHere is where your goal in mem stands:\n\n[" + strings.Join(lines, "\n") + "]"
	}
	return "<system_reminder>" + text + "</system_reminder>"
}

// remindOfMem is the mem reminder to put before the model's next reply,
// when one is due and the model has gnovm_run, as Claude Code puts in its
// reminder of the todo list: kept as a hidden user message, so the
// conversation reads the same when it is rebuilt. It is counted on the
// conversation kept since the session's last summary, as the model reads
// it, since the steps of a turn do not carry what a step put before them.
// A sub-agent is not reminded.
func (a *sessionAgent) remindOfMem(ctx context.Context, sessionID string, requestTools []fantasy.AgentTool) ([]fantasy.Message, error) {
	if a.isSubAgent || !slices.ContainsFunc(requestTools, func(tool fantasy.AgentTool) bool { return tool.Info().Name == gnovmtools.RunToolName }) {
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
	if !memReminderDue(msgs) {
		return nil, nil
	}
	msg, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: memReminder(sess.Todos), Hidden: true}},
	})
	if err != nil {
		return nil, err
	}
	return msg.ToAIMessage(), nil
}
