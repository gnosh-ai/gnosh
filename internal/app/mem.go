package app

import (
	"context"
	"errors"
)

// errNoGnoVMs is the answer about mem before the GnoVM machines are set up.
var errNoGnoVMs = errors.New("mem is not set up: the agent is not ready")

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
	return client.MemCommand(ctx, a.config.WorkingDir(), sessionID, args, titles)
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
