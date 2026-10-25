package model

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/version"
)

// How long each of Klash's two notifications waits for the user before
// its Notification hooks fire, as in Claude Code: a permission prompt
// that has waited about six seconds, and a finished turn with no reply
// for about a minute. They are variables, not constants, so a test
// passes in a shorter wait than waiting.
var (
	permissionNotificationDelay = 6 * time.Second
	idleNotificationDelay       = 60 * time.Second
)

// notificationHookKind names which of the two notifications a wait is
// for.
type notificationHookKind int

const (
	notificationHookPermission notificationHookKind = iota
	notificationHookIdle
)

// notificationHookArmed is a Notification hook that has been armed and
// is waiting out its delay: the permission prompt's, or the finished
// turn's. The token is what its tick carries; a key press or a new
// prompt bumps it, so a stale tick is ignored.
type notificationHookArmed struct {
	token            int
	kind             notificationHookKind
	toolName         string
	sessionID        string
	cwd              string
	notificationType string
	message          string
	title            string
}

// notificationHookMsg is what the tick sends once a wait has elapsed.
type notificationHookMsg struct {
	kind  notificationHookKind
	token int
}

// armPermissionNotification starts the wait for a permission prompt's
// Notification hook, as in Claude Code: it fires once the prompt has
// waited permissionNotificationDelay with no key pressed, and each key
// press starts the wait again. It runs whether or not desktop
// notifications are on, and whether or not the terminal has focus.
func (m *UI) armPermissionNotification(toolName string) tea.Cmd {
	return m.armNotificationHook(notificationHookPermission, permissionNotificationDelay,
		toolName, "",
		hooks.NotificationTypePermissionPrompt,
		fmt.Sprintf("Permission required to execute \"%s\"", toolName),
		"Permission needed")
}

// armIdleNotification starts the wait for a finished turn's Notification
// hook, as in Claude Code: it fires once the turn finished
// idleNotificationDelay ago and the user has not typed since.
func (m *UI) armIdleNotification(sessionTitle string) tea.Cmd {
	return m.armNotificationHook(notificationHookIdle, idleNotificationDelay,
		"", "",
		hooks.NotificationTypeIDlePrompt,
		fmt.Sprintf("Agent's turn completed in \"%s\"", sessionTitle),
		version.Name+" is waiting...")
}

// armNotificationHook arms one of the two Notification waits, replacing
// the wait of the same kind already running (if any): the old wait's
// token is bumped, so its tick is ignored when it comes.
func (m *UI) armNotificationHook(kind notificationHookKind, delay time.Duration, toolName, sessionID, notificationType, message, title string) tea.Cmd {
	m.notifToken++
	cwd := ""
	if m.com != nil && m.com.Workspace != nil {
		cwd = m.com.Workspace.WorkingDir()
	}
	if sessionID == "" {
		sessionID = m.currentSessionID()
	}
	armed := &notificationHookArmed{
		token:            m.notifToken,
		kind:             kind,
		toolName:         toolName,
		sessionID:        sessionID,
		cwd:              cwd,
		notificationType: notificationType,
		message:          message,
		title:            title,
	}
	if kind == notificationHookPermission {
		m.permNotification = armed
	} else {
		m.idleNotification = armed
	}
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return notificationHookMsg{kind: kind, token: armed.token}
	})
}

// onUserKeypress keeps the armed Notification waits in step with the
// user, as in Claude Code: each key press starts a waiting permission
// prompt's delay again, and any key press ends a finished turn's wait,
// since the user is no longer idle.
func (m *UI) onUserKeypress() tea.Cmd {
	var cmd tea.Cmd
	if armed := m.permNotification; armed != nil {
		cmd = m.armNotificationHook(armed.kind, permissionNotificationDelay,
			armed.toolName, armed.sessionID,
			armed.notificationType, armed.message, armed.title)
	}
	m.cancelIdleNotification()
	return cmd
}

// cancelIdleNotification ends a finished turn's wait: a key press or a
// new prompt means the user is no longer idle, so the Notification hook
// fires nothing.
func (m *UI) cancelIdleNotification() {
	if m.idleNotification != nil {
		m.notifToken++
		m.idleNotification = nil
	}
}

// cancelPermissionNotification ends the waiting permission prompt's wait:
// the dialog was answered, so the Notification hook fires nothing.
func (m *UI) cancelPermissionNotification() {
	if m.permNotification != nil {
		m.notifToken++
		m.permNotification = nil
	}
}

// handleNotificationHookExpired fires a Notification hook whose wait has
// elapsed, if the user has not since answered, typed or sent a prompt.
func (m *UI) handleNotificationHookExpired(msg notificationHookMsg) tea.Cmd {
	var armed *notificationHookArmed
	switch msg.kind {
	case notificationHookPermission:
		if m.permNotification == nil || m.permNotification.token != msg.token {
			return nil
		}
		armed = m.permNotification
		m.permNotification = nil
	case notificationHookIdle:
		if m.idleNotification == nil || m.idleNotification.token != msg.token {
			return nil
		}
		armed = m.idleNotification
		m.idleNotification = nil
	}
	return m.fireNotificationHook(armed)
}

// fireNotificationHook runs the Notification hooks for a notification
// whose wait has elapsed, as in Claude Code: in the background, so the
// TUI does not wait for them, and without a say — a hook's exit code and
// output are ignored, and a hook that fails is logged.
func (m *UI) fireNotificationHook(armed *notificationHookArmed) tea.Cmd {
	if m.com == nil || m.com.Workspace == nil {
		return nil
	}
	cfg := m.com.Config()
	if cfg == nil {
		return nil
	}
	hookCfgs := cfg.Hooks[hooks.EventNotification]
	if len(hookCfgs) == 0 {
		return nil
	}
	return func() tea.Msg {
		runner := hooks.NewRunner(hookCfgs, armed.cwd, armed.cwd)
		result, err := runner.RunNotification(context.Background(), armed.sessionID, armed.notificationType, armed.message, armed.title)
		if err != nil {
			slog.Warn("Notification hook execution error", "session_id", armed.sessionID, "notification_type", armed.notificationType, "error", err)
		} else {
			for _, h := range result.Hooks {
				if h.CouldNotEvaluate {
					slog.Warn("Notification hook could not evaluate", "notification_type", armed.notificationType, "error", h.Error)
				}
			}
		}
		return nil
	}
}
