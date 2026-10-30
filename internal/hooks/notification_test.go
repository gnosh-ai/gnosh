package hooks

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// logSwapMu keeps two tests that capture the default logger from
// running at once: each restores the logger it found, and two at once
// would each restore the other's, so a failure would land in the wrong
// buffer.
var logSwapMu sync.Mutex

// notificationRunner builds a Runner holding only Notification hooks,
// each with the given matcher.
func notificationRunner(t *testing.T, matcher string, commands ...string) *Runner {
	t.Helper()
	hookCfgs := make([]config.HookConfig, len(commands))
	for i, cmd := range commands {
		hookCfgs[i] = config.HookConfig{Command: cmd, Matcher: matcher}
	}
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{EventNotification: hookCfgs}}
	require.NoError(t, cfg.ValidateHooks())
	dir := t.TempDir()
	return NewRunner(cfg.Hooks[EventNotification], dir, dir)
}

// As in Claude Code, a Notification hook's payload carries the
// notification's text, its title and its type, and its env the type; a
// matcher is tested against the notification type.
func TestRunNotification(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	payloadPath := filepath.Join(dir, "payload.json")
	envPath := filepath.Join(dir, "env")
	r := notificationRunner(t, "", `cat > `+payloadPath, `echo "$GNOSH_NOTIFICATION_TYPE" > `+envPath)
	_, err := r.RunNotification(context.Background(), "sess", NotificationTypePermissionPrompt, `Permission required to execute "bash"`, "Permission needed")
	require.NoError(t, err)

	data, err := os.ReadFile(payloadPath)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "Notification", payload["hook_event_name"])
	require.Equal(t, "sess", payload["session_id"])
	require.Equal(t, r.cwd, payload["cwd"])
	require.Equal(t, "permission_prompt", payload["notification_type"])
	require.Equal(t, `Permission required to execute "bash"`, payload["message"])
	require.Equal(t, "Permission needed", payload["title"])
	env, err := os.ReadFile(envPath)
	require.NoError(t, err)
	require.Equal(t, "permission_prompt\n", string(env))

	// The title is optional, as in Claude Code: a notification without
	// one leaves it out of the payload.
	var noTitle map[string]any
	require.NoError(t, json.Unmarshal(BuildNotificationPayload("s", "/w", NotificationTypeIdlePrompt, "done", ""), &noTitle))
	require.NotContains(t, noTitle, "title")

	// A matcher is tested against the notification type.
	matched := notificationRunner(t, "idle_prompt", "true")
	result, err := matched.RunNotification(context.Background(), "sess", NotificationTypePermissionPrompt, "m", "")
	require.NoError(t, err)
	require.Zero(t, result.HookCount, "a hook matched on idle does not fire for a waiting permission prompt")
	result, err = matched.RunNotification(context.Background(), "sess", NotificationTypeIdlePrompt, "m", "")
	require.NoError(t, err)
	require.Equal(t, 1, result.HookCount)
}

// As in Claude Code, a Notification hook cannot block or change the
// notification: a hook that fails is logged, and the result does not
// hold the notification back.
func TestRunNotificationFailingHookIsLoggedAndIgnored(t *testing.T) {
	t.Parallel()

	logSwapMu.Lock()
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev); logSwapMu.Unlock() })

	r := notificationRunner(t, "", `echo "check the path" >&2; exit 1`)
	result, err := r.RunNotification(context.Background(), "sess", NotificationTypePermissionPrompt, "m", "t")
	require.NoError(t, err, "the notification goes ahead whatever the hook says")
	require.Len(t, result.Hooks, 1)
	require.True(t, result.Hooks[0].CouldNotEvaluate, "the failed hook is named in the result")
	require.Contains(t, buf.String(), "check the path", "the failure is logged")
}

// message, title and notification_type are in a Notification payload
// only.
func TestNotificationFieldsOnlyInNotificationPayloads(t *testing.T) {
	t.Parallel()

	for _, payload := range [][]byte{
		marshalPayload(toolPayload(EventPreToolUse, "s", "/w", "bash", `{}`)),
		BuildPostCompactPayload("s", "/w", PreCompactTriggerManual, "summary"),
		BuildSessionEndPayload("s", "/w", SessionEndReasonClear),
	} {
		var got map[string]any
		require.NoError(t, json.Unmarshal(payload, &got))
		require.NotContains(t, got, "message")
		require.NotContains(t, got, "title")
		require.NotContains(t, got, "notification_type")
	}
}
