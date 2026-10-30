// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovmtools

import (
	"context"
	"log/slog"
	"strings"

	"github.com/charmbracelet/crush/internal/gnovm"
	"github.com/charmbracelet/crush/internal/session"
)

// TodoSetter is what SessionTodos writes a session's task list with.
type TodoSetter interface {
	SetTodos(ctx context.Context, sessionID string, todos []session.Todo) (session.Session, error)
}

// SessionTodos shows the goal a mem call changed as the session's task
// list, the pills under its prompt: mem is the one task list, and a
// session's todos are only ever written from it. Of several goals one call
// changed, the last is shown.
func SessionTodos(sessions TodoSetter) MemGoalsFunc {
	return func(ctx context.Context, sessionID string, goals []gnovm.MemGoal) {
		if sessionID == "" || len(goals) == 0 {
			return
		}
		if _, err := sessions.SetTodos(ctx, sessionID, MemTodos(goals[len(goals)-1], sessionID)); err != nil {
			slog.Warn("mem's goal could not be shown as the session's todos", "error", err)
		}
	}
}

// MemTodos is goal g as a session's todos: its steps, the one this session
// started, or else the one its next is in, in progress; the other open ones
// pending; the done ones completed. Steps dropped or forgotten are left
// out. A goal with no steps is one todo.
func MemTodos(g gnovm.MemGoal, sessionID string) []session.Todo {
	if len(g.Steps) == 0 {
		status := session.TodoStatusInProgress
		if g.Status == "done" {
			status = session.TodoStatusCompleted
		}
		return []session.Todo{{Content: g.Title, Status: status, ActiveForm: g.Title}}
	}
	started := false
	for _, s := range g.Steps {
		started = started || s.Status == "" && s.Doing == sessionID
	}
	var todos []session.Todo
	for _, s := range g.Steps {
		status := session.TodoStatusPending
		switch {
		case s.Status == "done":
			status = session.TodoStatusCompleted
		case s.Status != "":
			continue
		case started && s.Doing == sessionID,
			!started && (g.Next == s.Name || strings.HasPrefix(g.Next, s.Name+"/")):
			status = session.TodoStatusInProgress
		}
		todos = append(todos, session.Todo{Content: s.Title, Status: status, ActiveForm: s.Title})
	}
	return todos
}
