package agent

import (
	"fmt"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// warnHookMessages shows the user, in sessionID, what each of event's
// hooks asked to show them, its systemMessage, as Claude Code does. It
// blocks nothing: it is how a hook warns, now that an error it reports on
// stderr blocks.
func (a *sessionAgent) warnHookMessages(sessionID, event string, ran []hooks.HookInfo) {
	if a.notify == nil {
		return
	}
	for _, h := range ran {
		if h.Message == "" {
			continue
		}
		a.notify.Publish(pubsub.CreatedEvent, notify.Notification{
			SessionID: sessionID,
			Type:      notify.TypeWarning,
			Message:   fmt.Sprintf("%s hook %q: %s", event, h.Name, h.Message),
		})
	}
}
