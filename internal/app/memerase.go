package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/gnovm"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
)

// memErased takes what mem erased (a Redact or a Forget, by the agent or
// by /mem) out of gnosh's own copies of mem's text in the project's
// sessions, as MEMPLAN §5 says:
//   - a session's /goal binding: a goal renamed by the erasure is bound
//     under its new name (a forgotten one then clears at the turn's end),
//     and its condition and title lose the text;
//   - its task list, the pills, which show goals' step titles; a pill left
//     with nothing but "(forgotten)" goes;
//   - the hidden part after a prompt's text, which carried the <mem> block
//     the session started with, and what hooks added;
//   - its summary, which every later request of the session sends in
//     place of what came before it.
//
// What the user saw stays as it was: the prompts typed, the agent's
// replies, its tool calls and their results, mem's receipts among them.
// Those still reach the model with every later turn of their session, so
// the sessions where they still hold a redacted secret are named in what
// it returns, for the user to start a new session or delete them.
//
// It writes past messages of sessions that may be mid-turn; a turn only
// appends messages and updates its current reply, which this leaves alone.
func (a *App) memErased(ctx context.Context, e gnovm.MemErasure) string {
	sessions, err := a.Sessions.List(ctx)
	if err != nil {
		slog.Warn("Sessions could not be read to erase from them what mem erased", "error", err)
		return ""
	}
	secrets := e.Secrets()
	var holding []string
	for _, s := range sessions {
		a.eraseFromGoal(ctx, s.ID, e)
		if todos, changed := eraseFromTodos(s.Todos, e); changed {
			if _, err := a.Sessions.SetTodos(ctx, s.ID, todos); err != nil {
				slog.Warn("A session's task list kept what mem erased", "session_id", s.ID, "error", err)
			}
		}
		msgs, err := a.Messages.List(ctx, s.ID)
		if err != nil {
			slog.Warn("A session's messages could not be read to erase from them what mem erased", "session_id", s.ID, "error", err)
			continue
		}
		visible := false
		for _, msg := range msgs {
			if eraseFromMessage(&msg, msg.ID == s.SummaryMessageID, e) {
				if err := a.Messages.Update(ctx, msg); err == nil {
					err = a.Messages.Flush(ctx, msg.ID)
				}
				if err != nil {
					slog.Warn("A message kept what mem erased", "session_id", s.ID, "error", err)
				}
			}
			visible = visible || holdsAny(msg, secrets)
		}
		if visible {
			holding = append(holding, strconv.Quote(s.Title))
		}
	}
	if len(holding) == 0 {
		return ""
	}
	return fmt.Sprintf("what you saw of it is still in %d session(s), %s, and reaches the model again in them: start a new session, or delete those", len(holding), strings.Join(holding, ", "))
}

// eraseFromGoal follows the erasure in a session's /goal binding.
func (a *App) eraseFromGoal(ctx context.Context, sessionID string, e gnovm.MemErasure) {
	stored, err := a.Sessions.Goal(ctx, sessionID)
	if err != nil || stored == "" {
		return
	}
	var b gnovm.GoalBinding
	if json.Unmarshal([]byte(stored), &b) != nil {
		return
	}
	was := b
	if n, ok := e.Renamed[b.Name]; ok {
		b.Name = n
	}
	b.Condition, b.Title, b.Verdict, b.Asked = e.Scrub(b.Condition), e.Scrub(b.Title), e.Scrub(b.Verdict), e.Scrub(b.Asked)
	if b == was {
		return
	}
	data, err := json.Marshal(b)
	if err == nil {
		_, err = a.Sessions.SwapGoal(ctx, sessionID, stored, string(data))
	}
	if err != nil {
		slog.Warn("A session's goal kept what mem erased", "session_id", sessionID, "error", err)
	}
}

// eraseFromTodos is a task list without what mem erased, and whether that
// changed it.
func eraseFromTodos(todos []session.Todo, e gnovm.MemErasure) ([]session.Todo, bool) {
	out := make([]session.Todo, 0, len(todos))
	changed := false
	for _, t := range todos {
		content, active := e.Scrub(t.Content), e.Scrub(t.ActiveForm)
		if content == t.Content && active == t.ActiveForm {
			out = append(out, t)
			continue
		}
		changed = true
		if strings.TrimSpace(content) != "(forgotten)" {
			t.Content, t.ActiveForm = content, active
			out = append(out, t)
		}
	}
	return out, changed
}

// eraseFromMessage takes what mem erased out of a message's hidden parts
// after its text, and out of its whole text if it is the session's
// summary, and reports whether that changed it.
func eraseFromMessage(msg *message.Message, summary bool, e gnovm.MemErasure) bool {
	changed := false
	first := true
	for i, part := range msg.Parts {
		text, ok := part.(message.TextContent)
		if !ok {
			continue
		}
		isFirst := first
		first = false
		if !summary && (msg.Role != message.User || isFirst || !text.Hidden) {
			continue
		}
		if scrubbed := e.Scrub(text.Text); scrubbed != text.Text {
			text.Text = scrubbed
			msg.Parts[i] = text
			changed = true
		}
	}
	return changed
}

// holdsAny reports whether what a message shows the user holds one of
// texts: its text, its tool calls' input, or their results.
func holdsAny(msg message.Message, texts []string) bool {
	if len(texts) == 0 {
		return false
	}
	var shown []string
	for _, part := range msg.Parts {
		switch p := part.(type) {
		case message.TextContent:
			if !p.Hidden {
				shown = append(shown, p.Text)
			}
		case message.ToolCall:
			shown = append(shown, p.Input)
		case message.ToolResult:
			shown = append(shown, p.Content)
		}
	}
	return slices.ContainsFunc(texts, func(t string) bool {
		return slices.ContainsFunc(shown, func(s string) bool { return strings.Contains(s, t) })
	})
}

// memErasedNotice tells the user, in session sessionID, where what the
// agent's mem call erased is still shown (see memErased).
func (a *App) memErasedNotice(ctx context.Context, sessionID string, e gnovm.MemErasure) {
	if text := a.memErased(ctx, e); text != "" {
		a.agentNotifications.Publish(pubsub.CreatedEvent, notify.Notification{SessionID: sessionID, Type: notify.TypeHookWarning, Message: "mem erased it, but " + text})
	}
}
