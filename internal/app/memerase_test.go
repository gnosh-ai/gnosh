package app

import (
	"encoding/json"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/gnovm"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// What mem erased leaves gnosh's own copies of mem's text: the <mem> block
// saved after a prompt and what recall gave one, the /goal binding, the
// pills and the summary. What
// the user saw stays, and the sessions where it still holds a redacted
// secret are named.
func TestMemErased(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	conn, err := db.Connect(ctx, t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	q := db.New(conn)
	sessions := session.NewService(q, conn)
	messages := message.NewService(q)
	a := &App{Sessions: sessions, Messages: messages, agentNotifications: pubsub.NewBroker[notify.Notification]()}

	s, err := sessions.Create(ctx, "Party planning")
	require.NoError(t, err)
	block := "<user-prompt-submit-hook>\n<mem>\n- `plan-surprise-party` \"plan the surprise party\"\n  next: `plan-surprise-party/1` \"book Moonlight Hall\"\n- `buy-milk` \"buy milk\"\n</mem>\n</user-prompt-submit-hook>"
	prompt, err := messages.Create(ctx, s.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{
		message.TextContent{Text: "plan the surprise party"},
		message.TextContent{Text: block, Hidden: true},
	}})
	require.NoError(t, err)
	_, err = messages.Create(ctx, s.ID, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{
		message.ToolCall{ID: "c1", Name: "gnovm_run", Input: `{"name":"mem","code":"mem.Fact(\"deploy\", \"sk-live-4f9a2c\")"}`, Finished: true},
	}})
	require.NoError(t, err)
	summary, err := messages.Create(ctx, s.ID, message.CreateMessageParams{Role: message.Assistant, Parts: []message.ContentPart{
		message.TextContent{Text: "We plan the surprise party; the deploy key is sk-live-4f9a2c."},
	}})
	require.NoError(t, err)
	_, err = sessions.UpdateSummary(ctx, s.ID, summary.ID, 10, 0, false)
	require.NoError(t, err)
	binding, err := json.Marshal(gnovm.GoalBinding{Condition: "plan the surprise party, then tell Ana", Name: "plan-surprise-party", Title: "plan the surprise party", Added: true})
	require.NoError(t, err)
	ok, err := sessions.SwapGoal(ctx, s.ID, "", string(binding))
	require.NoError(t, err)
	require.True(t, ok)
	_, err = sessions.SetTodos(ctx, s.ID, []session.Todo{{Content: "book Moonlight Hall", Status: session.TodoStatusPending}, {Content: "buy milk", Status: session.TodoStatusPending}})
	require.NoError(t, err)

	// What recall gave a later prompt goes the same way.
	recall := "<user-prompt-submit-hook>\n<mem>\nFrom your memory (mem), what may bear on what the user just wrote, found by meaning; `mem.Show(\"name\")` shows one in full:\n- fact `deploy` = sk-live-4f9a2c\n- [ ] `plan-surprise-party/1` \"book Moonlight Hall\"\n</mem>\n</user-prompt-submit-hook>"
	later, err := messages.Create(ctx, s.ID, message.CreateMessageParams{Role: message.User, Parts: []message.ContentPart{
		message.TextContent{Text: "deploy it"},
		message.TextContent{Text: recall, Hidden: true},
	}})
	require.NoError(t, err)

	note := a.memErased(ctx, gnovm.MemErasure{
		Texts:   map[string]string{"plan the surprise party": "(forgotten)", "book Moonlight Hall": "(forgotten)", "sk-live-4f9a2c": "(redacted)"},
		Names:   []string{"plan-surprise-party", "plan-surprise-party/1"},
		Renamed: map[string]string{"plan-surprise-party": "forgotten-1"},
	})
	require.Equal(t, `what you saw of it is still in 1 session(s), "Party planning", and reaches the model again in them: start a new session, or delete those`, note)

	got, err := messages.Get(ctx, prompt.ID)
	require.NoError(t, err)
	require.Equal(t, "plan the surprise party", got.Parts[0].(message.TextContent).Text, "what was typed stays")
	require.Equal(t, "<user-prompt-submit-hook>\n<mem>\n- `buy-milk` \"buy milk\"\n</mem>\n</user-prompt-submit-hook>", got.Parts[1].(message.TextContent).Text)
	got, err = messages.Get(ctx, later.ID)
	require.NoError(t, err)
	require.Equal(t, "<user-prompt-submit-hook>\n<mem>\nFrom your memory (mem), what may bear on what the user just wrote, found by meaning; `mem.Show(\"name\")` shows one in full:\n- fact `deploy` = (redacted)\n</mem>\n</user-prompt-submit-hook>",
		got.Parts[1].(message.TextContent).Text, "a recalled fact is redacted, and a forgotten item's line goes")
	got, err = messages.Get(ctx, summary.ID)
	require.NoError(t, err)
	require.Equal(t, "We (forgotten); the deploy key is (redacted).", got.Content().Text)
	stored, err := sessions.Goal(ctx, s.ID)
	require.NoError(t, err)
	var b gnovm.GoalBinding
	require.NoError(t, json.Unmarshal([]byte(stored), &b))
	require.Equal(t, gnovm.GoalBinding{Condition: "(forgotten), then tell Ana", Name: "forgotten-1", Title: "(forgotten)", Added: true}, b)
	s, err = sessions.Get(ctx, s.ID)
	require.NoError(t, err)
	require.Equal(t, []session.Todo{{Content: "buy milk", Status: session.TodoStatusPending}}, s.Todos)
}

// A rule a session's mem call sets, changes or removes is told to the
// user, each with how to undo it: every session follows it as the user's
// own.
func TestMemRulesNotice(t *testing.T) {
	t.Parallel()
	a := &App{agentNotifications: pubsub.NewBroker[notify.Notification]()}
	ch := a.agentNotifications.Subscribe(t.Context())
	a.memRulesNotice(t.Context(), "s1", []gnovm.MemRule{
		{Name: "setup", Text: "always run ./setup.sh first"},
		{Name: "push-after-landing", Text: "push only after landed:", Was: "push after the tests"},
		{Name: "no-emoji"},
	})
	var got []string
	for range 3 {
		e := <-ch
		require.Equal(t, "s1", e.Payload.SessionID)
		require.Equal(t, notify.TypeWarning, e.Payload.Type)
		got = append(got, e.Payload.Message)
	}
	require.Equal(t, []string{
		"mem: this session set a rule every session will follow as yours: `setup` = \"always run ./setup.sh first\". If you did not ask for it, /mem forget setup removes it",
		"mem: this session changed the rule `push-after-landing`, which every session follows as yours, to \"push only after landed:\" (it was \"push after the tests\"). If you did not ask for it, /mem log shows the change and /mem undo reverses it",
		"mem: this session removed the rule `no-emoji`, which every session followed as yours. If you did not ask for it, /mem log shows the change and /mem undo reverses it",
	}, got)
}
