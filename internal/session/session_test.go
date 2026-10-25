package session

import (
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/stretchr/testify/require"
)

func TestEstimatedUsageStateSurvivesFetchModifySave(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "test")
	require.NoError(t, err)
	created.PromptTokens = 100
	created.CompletionTokens = 50
	created.EstimatedUsage = true

	saved, err := sessions.Save(t.Context(), created)
	require.NoError(t, err)
	require.True(t, saved.EstimatedUsage)

	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, fetched.EstimatedUsage)

	fetched.Todos = []Todo{{
		Content:    "Check estimate state",
		Status:     TodoStatusInProgress,
		ActiveForm: "Checking estimate state",
	}}

	updated, err := sessions.Save(t.Context(), fetched)
	require.NoError(t, err)
	require.True(t, updated.EstimatedUsage)

	refetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, refetched.EstimatedUsage)
}

func TestSessionChannelPersists(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "channel")
	require.NoError(t, err)
	updated, err := sessions.SetChannel(t.Context(), created.ID, "signal")
	require.NoError(t, err)
	require.Equal(t, "signal", updated.Channel)

	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, "signal", fetched.Channel)
}

func TestMCPServerDisabledRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	disabled, err := sessions.MCPDisabledServers(t.Context())
	require.NoError(t, err)
	require.Empty(t, disabled, "a new repository must default to the config")

	require.NoError(t, sessions.SetMCPServerDisabled(t.Context(), "docker", true))
	require.NoError(t, sessions.SetMCPServerDisabled(t.Context(), "serena", true))
	require.NoError(t, sessions.SetMCPServerDisabled(t.Context(), "docker", true), "disabling twice must be idempotent")

	disabled, err = sessions.MCPDisabledServers(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"docker", "serena"}, disabled)

	require.NoError(t, sessions.SetMCPServerDisabled(t.Context(), "docker", false))
	disabled, err = sessions.MCPDisabledServers(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"serena"}, disabled)

	// Enabling records an enabled override so a config-disabled server
	// stays enabled across restarts; disabling removes it again.
	enabled, err := sessions.MCPServersEnabled(t.Context())
	require.NoError(t, err)
	require.Equal(t, []string{"docker"}, enabled)

	require.NoError(t, sessions.SetMCPServerDisabled(t.Context(), "docker", true))
	enabled, err = sessions.MCPServersEnabled(t.Context())
	require.NoError(t, err)
	require.Empty(t, enabled)
}

func TestEstimatedUsageStateCanBeClearedByExplicitSave(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})

	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)

	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "test")
	require.NoError(t, err)
	created.PromptTokens = 100
	created.CompletionTokens = 50
	created.EstimatedUsage = true

	saved, err := sessions.Save(t.Context(), created)
	require.NoError(t, err)
	require.True(t, saved.EstimatedUsage)

	saved.EstimatedUsage = false
	updated, err := sessions.Save(t.Context(), saved)
	require.NoError(t, err)
	require.False(t, updated.EstimatedUsage)

	refetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.False(t, refetched.EstimatedUsage)
}

// A step's usage is written alone: a title written after the step read
// the session (the title model's, or a /rename) is kept, where saving
// the session the step had read put its old title back. AddCost, for a
// sub-agent's run, adds to the cost and touches nothing else.
func TestUpdateUsageKeepsATitleWrittenMeanwhile(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "New Session")
	require.NoError(t, err)
	stale, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.NoError(t, sessions.Rename(t.Context(), created.ID, "Fix the flaky test"))

	events := sessions.Subscribe(t.Context())
	updated, err := sessions.UpdateUsage(t.Context(), stale.ID, 1200, 80, 0.5, true)
	require.NoError(t, err)
	select {
	case ev := <-events:
		require.Equal(t, updated, ev.Payload, "published for the UI")
	case <-time.After(5 * time.Second):
		t.Fatal("no update published")
	}
	require.Equal(t, "Fix the flaky test", updated.Title)
	require.Equal(t, [2]int64{1200, 80}, [2]int64{updated.PromptTokens, updated.CompletionTokens})
	require.InDelta(t, 0.5, updated.Cost, 1e-9)
	require.True(t, updated.EstimatedUsage)
	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, updated, fetched)

	updated, err = sessions.UpdateUsage(t.Context(), created.ID, 1300, 90, 0.25, false)
	require.NoError(t, err)
	require.InDelta(t, 0.75, updated.Cost, 1e-9, "cost is added")
	require.False(t, updated.EstimatedUsage)

	require.NoError(t, sessions.AddCost(t.Context(), created.ID, 1))
	fetched, err = sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.InDelta(t, 1.75, fetched.Cost, 1e-9)
	require.Equal(t, "Fix the flaky test", fetched.Title)
	require.Equal(t, [2]int64{1300, 90}, [2]int64{fetched.PromptTokens, fetched.CompletionTokens})
}

// The todos tool and a summary write their own columns alone, so a
// title or cost written after they read the session is kept.
func TestSetTodosAndUpdateSummaryKeepWhatTheyDoNotWrite(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() {
		require.NoError(t, db.Release(dataDir))
		db.ResetPool()
	})
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	sessions := NewService(db.New(conn), conn)

	created, err := sessions.Create(t.Context(), "New Session")
	require.NoError(t, err)
	_, err = sessions.UpdateUsage(t.Context(), created.ID, 5000, 300, 1, false)
	require.NoError(t, err)
	stale, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.NoError(t, sessions.Rename(t.Context(), created.ID, "Fix the flaky test"))
	require.NoError(t, sessions.AddCost(t.Context(), created.ID, 0.5))

	events := sessions.Subscribe(t.Context())
	todos := []Todo{{Content: "Write the test", Status: TodoStatusInProgress, ActiveForm: "Writing the test"}}
	updated, err := sessions.SetTodos(t.Context(), stale.ID, todos)
	require.NoError(t, err)
	select {
	case ev := <-events:
		require.Equal(t, updated, ev.Payload, "published for the UI")
	case <-time.After(5 * time.Second):
		t.Fatal("no update published")
	}
	require.Equal(t, todos, updated.Todos)
	require.Equal(t, "Fix the flaky test", updated.Title)
	require.InDelta(t, 1.5, updated.Cost, 1e-9)
	require.Equal(t, [2]int64{5000, 300}, [2]int64{updated.PromptTokens, updated.CompletionTokens})
	fetched, err := sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, updated, fetched)

	updated, err = sessions.UpdateSummary(t.Context(), stale.ID, "summary-message", 42, 0.25, true)
	require.NoError(t, err)
	select {
	case ev := <-events:
		require.Equal(t, updated, ev.Payload, "published for the UI")
	case <-time.After(5 * time.Second):
		t.Fatal("no update published")
	}
	require.Equal(t, "summary-message", updated.SummaryMessageID)
	require.Equal(t, [2]int64{0, 42}, [2]int64{updated.PromptTokens, updated.CompletionTokens}, "the counters start again from the summary")
	require.InDelta(t, 1.75, updated.Cost, 1e-9, "the summary's cost is added")
	require.True(t, updated.EstimatedUsage)
	require.Equal(t, "Fix the flaky test", updated.Title)
	require.Equal(t, todos, updated.Todos)
	fetched, err = sessions.Get(t.Context(), created.ID)
	require.NoError(t, err)
	require.Equal(t, updated, fetched)

	updated, err = sessions.SetTodos(t.Context(), created.ID, nil)
	require.NoError(t, err)
	require.Empty(t, updated.Todos)
	require.True(t, updated.EstimatedUsage, "an estimate stands until usage is written")
	require.Equal(t, "summary-message", updated.SummaryMessageID)
}
