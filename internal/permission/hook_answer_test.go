package permission

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// request asks s about a bash call with the given ID, as a tool does.
// Nobody answers here, so a request put to the user ends with the
// cancelled wait (asked is true); one answered otherwise returns granted.
func request(t *testing.T, ctx context.Context, s Service, toolCallID string) (granted, asked bool) {
	t.Helper()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	granted, err := s.Request(cancelled, CreatePermissionRequest{
		SessionID: "s1", ToolCallID: toolCallID, ToolName: "bash", Action: "execute", Path: t.TempDir(),
	})
	if err != nil {
		require.ErrorIs(t, err, context.Canceled)
		return false, true
	}
	return granted, false
}

// A call's PermissionRequest hooks answer its request when it is about to
// be put to the user, and only then: the request of another call, or of a
// sub-agent's call (WithoutHookAnswers), is put to the user, and one that
// needs no dialog does not run them.
func TestHookAnswerAnswersItsCallsDialog(t *testing.T) {
	t.Parallel()

	ran := 0
	answer := func(allow, deny bool) func(context.Context) HookAnswer {
		return func(context.Context) HookAnswer {
			ran++
			return HookAnswer{Allow: allow, Deny: deny}
		}
	}

	s := NewPermissionService(t.TempDir(), false, nil)
	granted, asked := request(t, WithHookAnswer(t.Context(), "call-1", answer(true, false)), s, "call-1")
	require.True(t, granted)
	require.False(t, asked)

	granted, asked = request(t, WithHookAnswer(t.Context(), "call-1", answer(false, true)), s, "call-1")
	require.False(t, granted)
	require.False(t, asked, "denied without a dialog")

	_, asked = request(t, WithHookAnswer(t.Context(), "call-1", answer(true, false)), s, "call-2")
	require.True(t, asked, "another call's hooks have no say")

	_, asked = request(t, WithoutHookAnswers(WithHookAnswer(t.Context(), "call-1", answer(true, false))), s, "call-1")
	require.True(t, asked, "a sub-agent's call with the same ID is put to the user")

	_, asked = request(t, WithHookAnswer(t.Context(), "call-1", answer(false, false)), s, "call-1")
	require.True(t, asked, "no answer: the user is asked")
	require.Equal(t, 3, ran)

	allowed := NewPermissionService(t.TempDir(), false, []string{"bash"})
	granted, asked = request(t, WithHookAnswer(t.Context(), "call-1", answer(false, true)), allowed, "call-1")
	require.True(t, granted, "an allowed tool needs no dialog")
	require.False(t, asked)
	require.Equal(t, 3, ran, "so its hooks do not run")
}

// A PreToolUse hook's "ask" is put to the user however the call is
// allowed (yolo here), and the PermissionRequest hooks answer that dialog
// too. With no answer, their note goes in the dialog.
func TestHookAnswerAnswersAHooksQuestion(t *testing.T) {
	t.Parallel()

	s := NewPermissionService(t.TempDir(), true, nil)
	ctx := WithHookAsk(t.Context(), "call-1")
	granted, asked := request(t, WithHookAnswer(ctx, "call-1", func(context.Context) HookAnswer { return HookAnswer{Allow: true} }), s, "call-1")
	require.True(t, granted)
	require.False(t, asked)

	events := s.Subscribe(t.Context())
	_, asked = request(t, WithHookAnswer(ctx, "call-1", func(context.Context) HookAnswer { return HookAnswer{Note: "the approver is down"} }), s, "call-1")
	require.True(t, asked)
	select {
	case event := <-events:
		require.Contains(t, event.Payload.Description, "the approver is down")
	case <-time.After(5 * time.Second):
		t.Fatal("the dialog was not published")
	}
}
