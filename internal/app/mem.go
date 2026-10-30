package app

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"

	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/gnovm"
	"github.com/charmbracelet/crush/internal/pubsub"
)

// errNoGnoVMs is the answer about mem before the GnoVM machines are set up.
var errNoGnoVMs = errors.New("mem is not set up: the agent is not ready")

// GnoVMDir is where gnosh keeps its GnoVM machines and every project's mem,
// beside its global data.
func GnoVMDir() string {
	return filepath.Join(filepath.Dir(config.GlobalConfigData()), "gnovm")
}

// MemCommand runs /mem, the user's command for the project's mem, typed in
// the session sessionID ("" for none), and returns what to show them. The
// work is mem's (gnovm.Host.MemCommand); sessions are named by their
// titles. args never go into a log: /mem redact carries a secret.
func (a *App) MemCommand(ctx context.Context, sessionID, args string) (string, error) {
	if a.gnovms == nil {
		return "", errNoGnoVMs
	}
	client, err := a.gnovms.Get()
	if err != nil {
		return "", err
	}
	titles := map[string]string{}
	if sessions, err := a.Sessions.List(ctx); err == nil {
		for _, s := range sessions {
			titles[s.ID] = s.Title
		}
	}
	text, erased, err := client.MemCommand(ctx, a.config.WorkingDir(), sessionID, args, titles)
	if err == nil && erased != nil {
		if note := a.memErased(ctx, *erased); note != "" {
			text += "\n(" + note + ")"
		}
	}
	return text, err
}

// memRulesNotice tells the user, in session sessionID, of the rules its mem
// call set, changed or removed. Every session is given a rule as the
// user's own words, and only the user's words should become one: a rule
// that a file or a tool's output planted shows here, with how to undo it.
func (a *App) memRulesNotice(_ context.Context, sessionID string, rules []gnovm.MemRule) {
	quote := func(s string) string {
		if r := []rune(s); len(r) > 200 {
			s = string(r[:200]) + "…"
		}
		return strconv.Quote(s)
	}
	for _, r := range rules {
		msg := "mem: this session "
		switch {
		case r.Text == "":
			msg += "removed the rule `" + r.Name + "`, which every session followed as yours. If you did not ask for it, /mem log shows the change and /mem undo reverses it"
		case r.Was == "":
			msg += "set a rule every session will follow as yours: `" + r.Name + "` = " + quote(r.Text) + ". If you did not ask for it, /mem forget " + r.Name + " removes it"
		default:
			msg += "changed the rule `" + r.Name + "`, which every session follows as yours, to " + quote(r.Text) + " (it was " + quote(r.Was) + "). If you did not ask for it, /mem log shows the change and /mem undo reverses it"
		}
		a.agentNotifications.Publish(pubsub.CreatedEvent, notify.Notification{SessionID: sessionID, Type: notify.TypeWarning, Message: msg})
	}
}

// MemNotice is what the user is told of the project's mem as a session
// starts: what is due and what waits on them, in one line; "" when nothing
// is. It reads a file mem keeps, and starts no worker.
func (a *App) MemNotice() string {
	if a.gnovms == nil {
		return ""
	}
	return a.gnovms.MemNotice(a.config.WorkingDir())
}
