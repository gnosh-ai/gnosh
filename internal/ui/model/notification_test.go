package model

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/agent/notify"
	mcptools "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/charmbracelet/crush/internal/workspace"
	"github.com/stretchr/testify/require"
)

// notificationTestWorkspace is a workspace with Notification hooks, so
// the model's notification waits can be driven without an app.
type notificationTestWorkspace struct {
	workspace.Workspace
	cfg        *config.Config
	dir        string
	grants     int
	denials    int
	runPrompts []string
}

func (w *notificationTestWorkspace) Config() *config.Config { return w.cfg }
func (w *notificationTestWorkspace) WorkingDir() string     { return w.dir }

func (w *notificationTestWorkspace) AgentReadyErr() error           { return nil }
func (w *notificationTestWorkspace) AgentIsReady() bool             { return true }
func (w *notificationTestWorkspace) AgentIsBusy() bool              { return false }
func (w *notificationTestWorkspace) AgentIsSessionBusy(string) bool { return false }
func (w *notificationTestWorkspace) AgentModel() workspace.AgentModel {
	return workspace.AgentModel{}
}
func (w *notificationTestWorkspace) PermissionSkipRequests() bool { return false }
func (w *notificationTestWorkspace) AgentQueuedPromptsList(string) []string {
	return nil
}

func (w *notificationTestWorkspace) AgentRun(_ context.Context, sessionID, prompt string, _ ...message.Attachment) error {
	w.runPrompts = append(w.runPrompts, sessionID+": "+prompt)
	return nil
}

func (w *notificationTestWorkspace) PermissionGrant(permission.PermissionRequest) bool {
	w.grants++
	return true
}

func (w *notificationTestWorkspace) PermissionGrantPersistent(permission.PermissionRequest) bool {
	w.grants++
	return true
}

func (w *notificationTestWorkspace) PermissionDeny(permission.PermissionRequest) bool {
	w.denials++
	return true
}

func (w *notificationTestWorkspace) LSPGetStates() map[string]workspace.LSPClientInfo {
	return nil
}

func (w *notificationTestWorkspace) MCPGetStates() map[string]mcptools.ClientInfo {
	return nil
}

// newNotificationTestUI builds a model with a session, a dialog overlay,
// and a workspace whose config carries the given Notification hooks.
func newNotificationTestUI(t *testing.T, dir string, hookCfgs []config.HookConfig) (*UI, *notificationTestWorkspace) {
	t.Helper()
	cfg := &config.Config{
		Hooks:   map[string][]config.HookConfig{config.EventNotification: hookCfgs},
		Options: &config.Options{TUI: &config.TUIOptions{}},
	}
	ws := &notificationTestWorkspace{cfg: cfg, dir: dir}
	u := newTestUI()
	u.com = common.DefaultCommon(ws)
	u.dialog = dialog.NewOverlay()
	u.keyMap = DefaultKeyMap()
	u.attachments = attachments.New(
		attachments.NewRenderer(u.com.Styles.Attachments),
		attachments.Keymap{
			DeleteMode: u.keyMap.Editor.AttachmentDeleteMode,
			DeleteAll:  u.keyMap.Editor.DeleteAllAttachments,
			Escape:     u.keyMap.Editor.Escape,
		},
	)
	u.session = &session.Session{ID: "sess-1"}
	return u, ws
}

// setNotificationDelays passes short waits in for the two notification
// waits, rather than the test waiting six seconds and a minute.
func setNotificationDelays(t *testing.T, perm, idle time.Duration) {
	t.Helper()
	oldPerm, oldIdle := permissionNotificationDelay, idleNotificationDelay
	permissionNotificationDelay = perm
	idleNotificationDelay = idle
	t.Cleanup(func() {
		permissionNotificationDelay = oldPerm
		idleNotificationDelay = oldIdle
	})
}

// runBatchedCmds runs the commands a cmd carries, the way the Bubble Tea
// program does: batch messages are expanded, every command runs in the
// background, and each message it produces is reported as it comes.
func runBatchedCmds(t *testing.T, cmd tea.Cmd) <-chan tea.Msg {
	t.Helper()
	require.NotNil(t, cmd)
	ch := make(chan tea.Msg, 64)
	go func() {
		defer close(ch)
		var wg sync.WaitGroup
		var run func(tea.Cmd)
		run = func(c tea.Cmd) {
			if c == nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				m := c()
				if batch, ok := m.(tea.BatchMsg); ok {
					for _, cc := range batch {
						run(cc)
					}
					return
				}
				ch <- m
			}()
		}
		run(cmd)
		wg.Wait()
	}()
	t.Cleanup(func() { <-ch })
	return ch
}

// waitForTick drains a runBatchedCmds channel until a notification wait
// expires; the busy and queue refreshes the model asks for along the way
// are not what the test is waiting on.
func waitForTick(t *testing.T, ch <-chan tea.Msg) notificationHookMsg {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				t.Fatal("the commands finished without the notification wait expiring")
			}
			if tm, ok := m.(notificationHookMsg); ok {
				return tm
			}
		case <-deadline:
			t.Fatal("timed out waiting for the notification wait to expire")
		}
	}
}

// runOut executes the commands a model update returned, the way the
// program would: in the background, whatever messages they come back
// with.
func runOut(t *testing.T, out tea.Cmd) {
	t.Helper()
	if out == nil {
		return
	}
	m := out()
	if batch, ok := m.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				go c()
			}
		}
		return
	}
	_ = m
}

// requireNoHookRun checks that no Notification hook wrote to path for
// half a second: a hook that fires runs in the background, so its file
// can come a moment after the command that fired it, and checking at once
// passed even when one fired.
func requireNoHookRun(t *testing.T, path, msg string) {
	t.Helper()
	require.Never(t, func() bool {
		_, err := os.Stat(path)
		return err == nil
	}, 500*time.Millisecond, 10*time.Millisecond, msg)
}

func readNotified(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.NotEmpty(t, lines)
	payloads := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var p map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &p))
		payloads = append(payloads, p)
	}
	return payloads
}

func permissionRequestEvent(toolName string) pubsub.Event[permission.PermissionRequest] {
	return pubsub.Event[permission.PermissionRequest]{Payload: permission.PermissionRequest{
		ID:         "perm-1",
		ToolCallID: "call-1",
		ToolName:   toolName,
	}}
}

// As in Claude Code, a permission prompt that has waited the delay with
// no key pressed fires the Notification hooks once, with the message
// that names the tool and a title. It fires whether or not desktop
// notifications are on, and whether or not the terminal has focus: the
// test turns the desktop gate off and focuses the terminal, the case it
// would suppress.
func TestPermissionNotificationFiresAfterTheWait(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "notified.jsonl")
	setNotificationDelays(t, 80*time.Millisecond, 80*time.Millisecond)
	u, _ := newNotificationTestUI(t, dir, []config.HookConfig{{
		// The sleep stands in for a slow phone-alert script: the TUI
		// must not wait for it.
		Command: "sleep 0.2; cat >> " + payload,
	}})
	// Desktop notifications off, terminal focused.
	u.com.Config().Options.Notifications = "disabled"
	u.notifyWindowFocused = true

	_, cmd := u.Update(permissionRequestEvent("bash"))
	ch := runBatchedCmds(t, cmd)

	tick := waitForTick(t, ch)
	_, out := u.Update(tick)
	// The TUI has moved on: the hook is still running.
	_, err := os.Stat(payload)
	require.True(t, os.IsNotExist(err), "the TUI does not wait for the hook")
	runOut(t, out)

	require.Eventually(t, func() bool {
		_, err := os.Stat(payload)
		return err == nil
	}, 5*time.Second, 5*time.Millisecond)
	payloads := readNotified(t, payload)
	require.Len(t, payloads, 1, "the hook fires exactly once")
	require.Equal(t, "Notification", payloads[0]["hook_event_name"])
	require.Equal(t, "permission_prompt", payloads[0]["notification_type"])
	require.Equal(t, `Permission required to execute "bash"`, payloads[0]["message"])
	require.Equal(t, "Permission needed", payloads[0]["title"])
	require.Equal(t, "sess-1", payloads[0]["session_id"])
	require.Equal(t, dir, payloads[0]["cwd"])
	require.True(t, u.dialog.ContainsDialog(dialog.PermissionsID),
		"the notification does not answer the dialog")
}

// As in Claude Code, each key press starts the permission prompt's six
// seconds again: the original wait, when it comes due, fires nothing,
// and the restarted one fires once.
func TestPermissionNotificationKeypressStartsTheWaitAgain(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "notified.jsonl")
	const delay = 120 * time.Millisecond
	setNotificationDelays(t, delay, delay)
	u, _ := newNotificationTestUI(t, dir, []config.HookConfig{{Command: "cat >> " + payload}})

	_, cmd := u.Update(permissionRequestEvent("bash"))
	ch := runBatchedCmds(t, cmd)

	// A key press partway through the wait.
	time.Sleep(delay * 2 / 5)
	_, keyCmd := u.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	require.NotNil(t, keyCmd, "the key press starts the wait again")
	keyCh := runBatchedCmds(t, keyCmd)

	// The original wait comes due first: it was restarted, so it fires
	// nothing.
	_, out := u.Update(waitForTick(t, ch))
	runOut(t, out)
	requireNoHookRun(t, payload, "the original wait was restarted by the key press")

	// The restarted wait fires, once.
	_, out = u.Update(waitForTick(t, keyCh))
	runOut(t, out)
	require.Eventually(t, func() bool {
		_, err := os.Stat(payload)
		return err == nil
	}, 5*time.Second, 5*time.Millisecond)
	require.Len(t, readNotified(t, payload), 1)
}

// A permission dialog answered before the wait comes due fires nothing.
func TestPermissionNotificationAnsweredSoonerFiresNothing(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "notified.jsonl")
	const delay = 800 * time.Millisecond
	setNotificationDelays(t, delay, delay)
	u, ws := newNotificationTestUI(t, dir, []config.HookConfig{{Command: "cat >> " + payload}})

	_, cmd := u.Update(permissionRequestEvent("bash"))
	ch := runBatchedCmds(t, cmd)

	// The user allows, well before the wait comes due (and past the
	// dialog's grace period for in-flight keys).
	time.Sleep(500 * time.Millisecond)
	_, keyCmd := u.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	require.Equal(t, 1, ws.grants, "the answer still reaches the workspace")
	require.False(t, u.dialog.ContainsDialog(dialog.PermissionsID))
	keyCh := runBatchedCmds(t, keyCmd)

	// Both the original wait and the one the key press started are
	// stale: the dialog was answered, so neither fires.
	_, out := u.Update(waitForTick(t, ch))
	runOut(t, out)
	_, out = u.Update(waitForTick(t, keyCh))
	runOut(t, out)
	requireNoHookRun(t, payload, "")
}

// As in Claude Code, a turn that has finished and the delay has passed
// with no key pressed in the TUI fires the Notification hooks once, with
// the finished-turn notification's text. It fires with the terminal
// focused and desktop notifications off, the case the desktop gate
// would suppress.
func TestIdleNotificationFiresAfterTheWait(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "notified.jsonl")
	setNotificationDelays(t, 80*time.Millisecond, 80*time.Millisecond)
	u, _ := newNotificationTestUI(t, dir, []config.HookConfig{{
		Command: "sleep 0.2; cat >> " + payload,
	}})
	u.com.Config().Options.Notifications = "disabled"
	u.notifyWindowFocused = true

	// The turn finished in another session than the one on screen; the
	// hook is told of that one, which the message names.
	_, cmd := u.Update(pubsub.Event[notify.Notification]{Payload: notify.Notification{
		Type:         notify.TypeAgentFinished,
		SessionID:    "sess-2",
		SessionTitle: "my project",
	}})
	ch := runBatchedCmds(t, cmd)

	_, out := u.Update(waitForTick(t, ch))
	// The TUI has moved on: the hook is still running.
	_, err := os.Stat(payload)
	require.True(t, os.IsNotExist(err), "the TUI does not wait for the hook")
	runOut(t, out)

	require.Eventually(t, func() bool {
		_, err := os.Stat(payload)
		return err == nil
	}, 5*time.Second, 5*time.Millisecond)
	payloads := readNotified(t, payload)
	require.Len(t, payloads, 1, "the hook fires exactly once")
	require.Equal(t, "Notification", payloads[0]["hook_event_name"])
	require.Equal(t, "idle_prompt", payloads[0]["notification_type"])
	require.Equal(t, `Agent's turn completed in "my project"`, payloads[0]["message"])
	require.Equal(t, version.Name+" is waiting...", payloads[0]["title"])
	require.Equal(t, "sess-2", payloads[0]["session_id"])
	require.Equal(t, dir, payloads[0]["cwd"])
}

// With no Notification hooks there is nothing to wait for: neither a
// permission prompt nor a finished turn arms a wait, so no tick runs on
// for a minute (a test that ran a finished turn's commands waited out the
// minute).
func TestNotificationWaitsOnlyWithHooks(t *testing.T) {
	u, _ := newNotificationTestUI(t, t.TempDir(), nil)
	u.Update(pubsub.Event[notify.Notification]{Payload: notify.Notification{
		Type:         notify.TypeAgentFinished,
		SessionID:    "sess-1",
		SessionTitle: "my project",
	}})
	require.Nil(t, u.idleNotification)
	u.Update(permissionRequestEvent("bash"))
	require.Nil(t, u.permNotification)
}

// A key press before the finished turn's wait comes due fires nothing.
func TestIdleNotificationKeypressFiresNothing(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "notified.jsonl")
	const delay = 800 * time.Millisecond
	setNotificationDelays(t, delay, delay)
	u, _ := newNotificationTestUI(t, dir, []config.HookConfig{{Command: "cat >> " + payload}})

	_, cmd := u.Update(pubsub.Event[notify.Notification]{Payload: notify.Notification{
		Type:         notify.TypeAgentFinished,
		SessionID:    "sess-1",
		SessionTitle: "my project",
	}})
	ch := runBatchedCmds(t, cmd)

	time.Sleep(300 * time.Millisecond)
	u.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})

	_, out := u.Update(waitForTick(t, ch))
	runOut(t, out)
	requireNoHookRun(t, payload, "")
}

// A new prompt before the finished turn's wait comes due fires nothing.
func TestIdleNotificationNewPromptFiresNothing(t *testing.T) {
	dir := t.TempDir()
	payload := filepath.Join(dir, "notified.jsonl")
	const delay = 800 * time.Millisecond
	setNotificationDelays(t, delay, delay)
	u, ws := newNotificationTestUI(t, dir, []config.HookConfig{{Command: "cat >> " + payload}})

	_, cmd := u.Update(pubsub.Event[notify.Notification]{Payload: notify.Notification{
		Type:         notify.TypeAgentFinished,
		SessionID:    "sess-1",
		SessionTitle: "my project",
	}})
	ch := runBatchedCmds(t, cmd)

	// The user sends a new prompt before the wait comes due.
	time.Sleep(300 * time.Millisecond)
	out := u.sendMessage("hi")
	require.NotNil(t, out)
	runOut(t, out)
	require.Eventually(t, func() bool {
		return len(ws.runPrompts) == 1 && ws.runPrompts[0] == "sess-1: hi"
	}, 2*time.Second, time.Millisecond, "the prompt was sent")

	_, out = u.Update(waitForTick(t, ch))
	runOut(t, out)
	requireNoHookRun(t, payload, "")
}
