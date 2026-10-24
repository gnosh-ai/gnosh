package permission

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Under Claude Code's dontAsk mode a request that would be put to the
// user is denied at once instead, and the call's NotAsked says so; what
// runs without asking still runs, a PermissionRequest hook can still
// allow, and a PreToolUse hook's question is denied too.
func TestDontAskDeniesWhatWouldBeAsked(t *testing.T) {
	t.Parallel()

	s := New(Options{WorkingDir: t.TempDir(), DontAsk: true, AllowedTools: []string{"view"}})
	events := s.Subscribe(t.Context())
	ask := func(ctx context.Context, tool string) (bool, *NotAsked) {
		t.Helper()
		// A request put to the user would wait; this ends the wait.
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		ctx, notAsked := WithNotAsked(ctx, "call-1")
		granted, err := s.Request(ctx, CreatePermissionRequest{SessionID: "s1", ToolCallID: "call-1", ToolName: tool, Action: "execute", Path: t.TempDir()})
		require.NoError(t, err, "no one is waited for")
		return granted, notAsked
	}

	granted, notAsked := ask(t.Context(), "bash")
	require.False(t, granted)
	require.True(t, notAsked.Denied())

	granted, notAsked = ask(t.Context(), "view")
	require.True(t, granted, "an allowed tool runs")
	require.False(t, notAsked.Denied())

	granted, notAsked = ask(WithHookAnswer(t.Context(), "call-1", func(context.Context) HookAnswer { return HookAnswer{Allow: true} }), "bash")
	require.True(t, granted, "a PermissionRequest hook can still allow")
	require.False(t, notAsked.Denied())

	granted, notAsked = ask(WithHookAsk(t.Context(), "call-1"), "view")
	require.False(t, granted, "a hook's question is not put to anyone either")
	require.True(t, notAsked.Denied())

	select {
	case event := <-events:
		t.Fatalf("a dialog was published: %+v", event.Payload)
	default:
	}

	// Another call's NotAsked is not marked.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx, other := WithNotAsked(ctx, "call-2")
	_, err := s.Request(ctx, CreatePermissionRequest{SessionID: "s1", ToolCallID: "call-1", ToolName: "bash", Action: "execute", Path: t.TempDir()})
	require.NoError(t, err)
	require.False(t, other.Denied())

	// Without dontAsk the request is put to the user.
	asks := New(Options{WorkingDir: t.TempDir()})
	_, asked := request(t, t.Context(), asks, "call-1")
	require.True(t, asked)
}
