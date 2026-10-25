package tools

import (
	"context"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// renamedOnRead names the session just after each read of it, as the
// title model's title or a /rename can land between a read and a write.
type renamedOnRead struct{ session.Service }

func (s *renamedOnRead) Get(ctx context.Context, id string) (session.Session, error) {
	sess, err := s.Service.Get(ctx, id)
	if err == nil {
		err = s.Service.Rename(ctx, id, "named meanwhile")
	}
	return sess, err
}

// The todos tool writes the todo list alone. Saving the whole session it
// had read put back the name the session had then, over one given just
// after the read.
func TestTodosWritesOnlyTheTodoList(t *testing.T) {
	t.Parallel()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	sessions := session.NewService(db.New(conn), conn)
	sess, err := sessions.Create(t.Context(), "the old name")
	require.NoError(t, err)
	tool := NewTodosTool(&renamedOnRead{Service: sessions})

	ctx := context.WithValue(t.Context(), SessionIDContextKey, sess.ID)
	input := `{"todos":[{"content":"Write the test","status":"in_progress","active_form":"Writing the test"}]}`
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "call-1", Name: TodosToolName, Input: input})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)

	got, err := sessions.Get(t.Context(), sess.ID)
	require.NoError(t, err)
	require.Equal(t, "named meanwhile", got.Title)
	require.Equal(t, []session.Todo{{Content: "Write the test", Status: session.TodoStatusInProgress, ActiveForm: "Writing the test"}}, got.Todos)
}
