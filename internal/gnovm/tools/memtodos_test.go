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
	"sync"
	"testing"

	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// todoSetter keeps what SessionTodos writes.
type todoSetter struct {
	mu    sync.Mutex
	todos map[string][]session.Todo
}

func (s *todoSetter) SetTodos(_ context.Context, sessionID string, todos []session.Todo) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.todos[sessionID] = todos
	return session.Session{ID: sessionID, Todos: todos}, nil
}

// mem is the one task list: a goal a call to it plans or changes is what
// the session's todos show, its next step in progress; a read changes
// nothing, and dropped steps are left out.
func TestMemGoalIsTheSessionsTodos(t *testing.T) {
	t.Parallel()
	setter := &todoSetter{todos: map[string][]session.Todo{}}
	run := NewRunTool(newLazy(t), "/proj", OnMemGoals(SessionTodos(setter)))
	todo := func(content string, status session.TodoStatus) session.Todo {
		return session.Todo{Content: content, Status: status, ActiveForm: content}
	}

	call(t, run, `{"name": "mem", "code": "mem.Goal(\"ship it\").Steps(\"build\", \"test\", \"tag\")"}`)
	require.Equal(t, []session.Todo{
		todo("build", session.TodoStatusInProgress),
		todo("test", session.TodoStatusPending),
		todo("tag", session.TodoStatusPending),
	}, setter.todos["session-1"])

	call(t, run, `{"name": "mem", "code": "mem.Done(\"ship/build\", \"built\")\nmem.Drop(\"ship/tag\", \"no tag this time\")"}`)
	require.Equal(t, []session.Todo{
		todo("build", session.TodoStatusCompleted),
		todo("test", session.TodoStatusInProgress),
	}, setter.todos["session-1"])

	delete(setter.todos, "session-1")
	call(t, run, `{"name": "mem", "code": "mem.Show()"}`)
	require.Empty(t, setter.todos, "a read changes nothing")

	call(t, run, `{"name": "mem", "code": "mem.Done(\"ship/test\", \"ok\")\nmem.Done(\"ship\", \"shipped\")"}`)
	require.Equal(t, []session.Todo{
		todo("build", session.TodoStatusCompleted),
		todo("test", session.TodoStatusCompleted),
	}, setter.todos["session-1"])
}
