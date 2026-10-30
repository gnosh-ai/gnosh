package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/hooks"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/stretchr/testify/require"
)

// sessionEndApp builds an App with the given SessionEnd hooks
// configured, in its own directory, so the hooks run where the test can
// see their output.
func sessionEndApp(t *testing.T, hookCfgs ...config.HookConfig) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{hooks.EventSessionEnd: hookCfgs}}
	require.NoError(t, cfg.ValidateHooks())
	app := NewForTest(t.Context())
	app.config = config.NewTestStoreIn(cfg, dir)
	// The LSP manager is the one nil the full Shutdown touches; give it
	// an empty one.
	app.LSPManager = lsp.NewManager(app.config)
	t.Cleanup(app.ShutdownForTest)
	return app, dir
}

// waitSessionEndHooks blocks until the SessionEnd hooks the test has
// triggered have finished writing their output. The tests wait on an
// unbounded context, not the Shutdown's budget, so a hook's own timeout
// is what bounds them, as it is in the TUI's session switch.
func waitSessionEndHooks(t *testing.T, app *App) {
	t.Helper()
	app.waitForSessionEndHooks(context.Background())
}

// A session that is reported and then "" ends with reason "clear": the
// TUI reports the landing screen after a New Session.
func TestReportCurrentSessionClearEndsTheSession(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	app, _ := sessionEndApp(t, config.HookConfig{Command: `echo "$GNOSH_SESSION_END_REASON" > ` + filepath.Join(dir, "reason")})
	app.ReportCurrentSession("s1")
	app.ReportCurrentSession("")
	waitSessionEndHooks(t, app)

	data, err := os.ReadFile(filepath.Join(dir, "reason"))
	require.NoError(t, err)
	require.Equal(t, "clear\n", string(data))
}

// A session that is reported and then another ends with reason
// "other": the user switched.
func TestReportCurrentSessionSwitchEndsTheSession(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	app, _ := sessionEndApp(t, config.HookConfig{Command: `echo "$GNOSH_SESSION_ID $GNOSH_SESSION_END_REASON" > ` + filepath.Join(dir, "ended")})
	app.ReportCurrentSession("s1")
	app.ReportCurrentSession("s2")
	waitSessionEndHooks(t, app)

	data, err := os.ReadFile(filepath.Join(dir, "ended"))
	require.NoError(t, err)
	require.Equal(t, "s1 other\n", string(data))
}

// Shutdown with a session open ends it: "prompt_input_exit" in the
// TUI's case, "other" after a gnosh run.
func TestShutdownEndsTheOpenSession(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	app, _ := sessionEndApp(t, config.HookConfig{Command: `echo "$GNOSH_SESSION_END_REASON" >> ` + filepath.Join(dir, "reasons")})
	app.ReportCurrentSession("s1")
	app.Shutdown()

	data, err := os.ReadFile(filepath.Join(dir, "reasons"))
	require.NoError(t, err)
	require.Equal(t, "prompt_input_exit\n", string(data))
}

// A session ends with its hooks run even when the process's context was
// cancelled first, as SIGINT cancels it: gnosh ending on SIGINT is an end
// the hook is there for (Claude Code runs SessionEnd on SIGINT too), and
// under the cancelled context the hook never ran.
func TestShutdownAfterSIGINTStillEndsTheSession(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ctx, cancel := context.WithCancel(t.Context())
	cfg := &config.Config{Hooks: map[string][]config.HookConfig{hooks.EventSessionEnd: {
		{Command: `echo "$GNOSH_SESSION_END_REASON" >> ` + filepath.Join(dir, "reasons")},
	}}}
	require.NoError(t, cfg.ValidateHooks())
	app := NewForTest(ctx)
	app.config = config.NewTestStoreIn(cfg, dir)
	app.LSPManager = lsp.NewManager(app.config)

	app.ReportCurrentSession("s1")
	cancel()
	app.Shutdown()

	data, err := os.ReadFile(filepath.Join(dir, "reasons"))
	require.NoError(t, err, "the SessionEnd hook ran")
	require.Equal(t, "prompt_input_exit\n", string(data))
}

// A session open when a signal stopped gnosh ends with reason "other",
// as Claude Code's does on SIGINT and SIGTERM; prompt_input_exit says the
// user left from the prompt.
func TestShutdownAfterASignalEndsWithOther(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	app, _ := sessionEndApp(t, config.HookConfig{Command: `echo "$GNOSH_SESSION_END_REASON" >> ` + filepath.Join(dir, "reasons")})
	app.ReportCurrentSession("s1")
	app.StoppedBySignal()
	app.Shutdown()

	data, err := os.ReadFile(filepath.Join(dir, "reasons"))
	require.NoError(t, err)
	require.Equal(t, "other\n", string(data))
}

func TestShutdownAfterRunEndsWithOther(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	app, _ := sessionEndApp(t, config.HookConfig{Command: `echo "$GNOSH_SESSION_END_REASON" >> ` + filepath.Join(dir, "reasons")})
	app.nonInteractive = true
	app.ReportCurrentSession("s1")
	app.Shutdown()

	data, err := os.ReadFile(filepath.Join(dir, "reasons"))
	require.NoError(t, err)
	require.Equal(t, "other\n", string(data))
}

// A session ends once: switching away and then exiting does not end it
// again, and reporting the same session twice does not.
func TestSessionEndsOnce(t *testing.T) {
	t.Parallel()

	// The switch ends s1; the exit ends s2, the session open then. Each
	// ends exactly once, so the file has exactly two lines.
	dir := t.TempDir()
	app, _ := sessionEndApp(t, config.HookConfig{Command: `echo "$GNOSH_SESSION_ID" > ` + filepath.Join(dir, "$GNOSH_SESSION_ID")})
	app.ReportCurrentSession("s1")
	app.ReportCurrentSession("s2")
	app.Shutdown()

	// The shell's ">> file" write is buffered; require.Eventually gives
	// it a moment to land. Two concurrent appends can interleave their
	// newlines, so the test writes to separate files instead of one
	// shared one, and waits for both.
	require.Eventually(t, func() bool {
		for _, id := range []string{"s1", "s2"} {
			if _, err := os.Stat(filepath.Join(dir, id)); err != nil {
				return false
			}
		}
		return true
	}, 2*time.Second, 10*time.Millisecond, "both sessions ended")
	for _, id := range []string{"s1", "s2"} {
		data, err := os.ReadFile(filepath.Join(dir, id))
		require.NoError(t, err)
		require.Equal(t, id+"\n", string(data), id+" ended exactly once")
	}

	dir2 := t.TempDir()
	app2, _ := sessionEndApp(t, config.HookConfig{Command: `echo x >> ` + filepath.Join(dir2, "count")})
	app2.ReportCurrentSession("s1")
	app2.ReportCurrentSession("s1")
	app2.ReportCurrentSession("")
	// Shutdown waits for the hooks internally, so the read below is
	// safe without a separate wait.
	app2.Shutdown()

	data2, err := os.ReadFile(filepath.Join(dir2, "count"))
	require.NoError(t, err)
	require.Equal(t, "x\n", string(data2), "reporting the same session twice does not end it, and the empty report ends it once, not again on exit")
}

// A session that never had one — the new, still empty one — does not
// end at all: reporting "" without a reported session ends nothing, and
// so does exiting without one.
func TestNeverReportedSessionDoesNotEnd(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	app, _ := sessionEndApp(t, config.HookConfig{Command: `echo x >> ` + filepath.Join(dir, "count")})
	app.ReportCurrentSession("")
	app.Shutdown()

	_, err := os.Stat(filepath.Join(dir, "count"))
	require.True(t, os.IsNotExist(err), "no hooks ran for a session that was never open")
}

// The payload carries hook_event_name "SessionEnd", session_id, cwd and
// reason, and the environment carries the event and the session as
// other hooks' do.
func TestSessionEndPayloadAndEnv(t *testing.T) {
	t.Parallel()

	app, appDir := sessionEndApp(t)
	// The hook's command writes the payload to a file the test can read.
	app.config.Config().Hooks[hooks.EventSessionEnd] = []config.HookConfig{
		{Command: `cat > ` + filepath.Join(appDir, "payload.json")},
	}
	app.ReportCurrentSession("s1")
	app.ReportCurrentSession("")
	waitSessionEndHooks(t, app)

	data, err := os.ReadFile(filepath.Join(appDir, "payload.json"))
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	require.Equal(t, "SessionEnd", payload["hook_event_name"])
	require.Equal(t, "s1", payload["session_id"])
	require.Equal(t, appDir, payload["cwd"], "the project directory, as SessionStart's hooks run there")
	require.Equal(t, "clear", payload["reason"])
}

// A matcher such as "clear|other" is accepted on a SessionEnd hook:
// the config loads, and the hook fires only on the reasons it names.
func TestConfigAcceptsSessionEndWithMatcher(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Hooks: map[string][]config.HookConfig{
		hooks.EventSessionEnd: {{Command: "true", Matcher: "clear|other"}},
	}}
	require.NoError(t, cfg.ValidateHooks(), "the config accepts SessionEnd with a matcher")
	require.True(t, config.IsKnownHookEvent(hooks.EventSessionEnd))
	require.True(t, config.HookEventSupportsMatcher(hooks.EventSessionEnd))

	dir := t.TempDir()
	app, _ := sessionEndApp(t, config.HookConfig{Command: `echo x >> ` + filepath.Join(dir, "count"), Matcher: "clear|other"})
	app.ReportCurrentSession("s1")
	app.Shutdown()

	_, err := os.Stat(filepath.Join(dir, "count"))
	require.True(t, os.IsNotExist(err), "a prompt_input_exit exit does not match clear|other")
}

// An App that serves a gnosh run and then a TUI session must end the
// TUI's exit with reason "prompt_input_exit", not "other": the
// nonInteractive flag set by RunNonInteractive is reset when the
// interactive agent is reinitialized, so a hook matched on
// prompt_input_exit fires for the TUI exit.
func TestNonInteractiveFlagResetForTUI(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	app, _ := sessionEndApp(t, config.HookConfig{Command: `echo "$GNOSH_SESSION_END_REASON" > ` + filepath.Join(dir, "reason")})
	app.setNonInteractive(true)
	app.ReportCurrentSession("s1")
	// Simulate the TUI reinitializing the interactive agent, which
	// resets nonInteractive.
	app.setNonInteractive(false)
	app.Shutdown()

	data, err := os.ReadFile(filepath.Join(dir, "reason"))
	require.NoError(t, err)
	require.Equal(t, "prompt_input_exit\n", string(data),
		"the TUI exit is prompt_input_exit, not other")
}

// A SessionEnd hook that sets its own 60-second timeout does not block
// the caller: endSession runs the hook asynchronously, and the session
// switch or the Shutdown's early cleanup is not held up by it. The
// hook is still run to completion by waitForSessionEndHooks, which
// Shutdown calls before the process exits.
func TestSessionEndHookDoesNotBlockCaller(t *testing.T) {
	t.Parallel()

	app, _ := sessionEndApp(t, config.HookConfig{
		Command: `sleep 0.2`,
		Timeout: 60,
	})
	start := time.Now()
	app.ReportCurrentSession("s1")
	app.ReportCurrentSession("")
	elapsed := time.Since(start)
	require.Less(t, elapsed, 150*time.Millisecond,
		"the caller is not held up by the hook's own timeout")

	// The hook did run, eventually; Shutdown waits for it.
	app.Shutdown()
}

// A Shutdown that ends the open session and has in-flight SessionEnd
// hooks does not exceed its 5-second budget: the wait is released at
// the deadline, instead of a slow hook holding the exit open. The hook
// sleeps for 7 seconds, past the budget, so without the fix the wait
// held Shutdown for ~7 seconds; with it, the wait is released at the
// deadline and the rest of the cleanup runs in the budget's remainder.
func TestShutdownDoesNotExceedBudget(t *testing.T) {
	t.Parallel()

	app, _ := sessionEndApp(t, config.HookConfig{
		Command: `sleep 7`,
		Timeout: 60,
	})
	app.ReportCurrentSession("s1")
	start := time.Now()
	app.Shutdown()
	elapsed := time.Since(start)
	// The budget is 5 seconds; a hook at its own 60-second cap must not
	// hold the exit past it. Allow generous slack for the rest of the
	// cleanup, but not for the hook itself.
	require.Less(t, elapsed, 7*time.Second,
		"Shutdown is bounded by its 5-second budget, not the hook's 60-second cap")
}

// A session the user leaves, reopens and leaves again ends twice, even
// while its first end's hook is still running: each end is an event a
// SessionEnd hook may count, and skipping the second, as an earlier
// version did, dropped it. Leaving it once and exiting still ends it once
// (TestSessionEndsOnce).
func TestSessionEndsEachTimeItIsLeft(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	app, _ := sessionEndApp(t, config.HookConfig{
		Command: `echo "$GNOSH_SESSION_ID" >> ` + filepath.Join(dir, "ended") + ` && sleep 0.3`,
		Timeout: 60,
	})
	app.ReportCurrentSession("s1")
	app.ReportCurrentSession("") // s1 ends; its hook runs for 0.3 s
	app.ReportCurrentSession("s1")
	app.ReportCurrentSession("") // s1 ends again while that hook runs
	app.waitForSessionEndHooks(context.Background())

	data, err := os.ReadFile(filepath.Join(dir, "ended"))
	require.NoError(t, err)
	// Count the ids, not lines: two hooks appending at once can write
	// "s1s1\n\n", as the shell writes an echo's text and its newline apart.
	require.Equal(t, 2, strings.Count(string(data), "s1"), "s1 was left twice: %q", data)
}

// A session that is reported open again after it ended ends again, and
// its hooks run again: the ended set is pruned by the hook goroutine
// once the hook has finished, not only by waitForSessionEndHooks, so a
// session switch (which does not wait) does not leave a stale entry
// that skips the second end.
func TestSessionEndsAgainAfterReopen(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	app, _ := sessionEndApp(t, config.HookConfig{Command: `echo "$GNOSH_SESSION_ID" >> ` + filepath.Join(dir, "ended")})
	// Report s1, then s2: s1 ends, s2 is open.
	app.ReportCurrentSession("s1")
	app.ReportCurrentSession("s2")
	// Wait for s1's hook to finish, so its entry is pruned.
	app.waitForSessionEndHooks(context.Background())
	// Re-open s1 (the TUI's session list re-opens a session, say): s2
	// ends, s1 is open again.
	app.ReportCurrentSession("s1")
	// Exit with s1 open: s1 ends again.
	app.Shutdown()

	// Both ends of s1 and the one end of s2 must have run: three lines,
	// two of them s1.
	var data []byte
	require.Eventually(t, func() bool {
		var err error
		data, err = os.ReadFile(filepath.Join(dir, "ended"))
		// Ids, not lines: s2's hook and s1's second can run at once.
		return err == nil && strings.Count(string(data), "s1") == 2 && strings.Count(string(data), "s2") == 1
	}, 2*time.Second, 10*time.Millisecond, "s1 ended twice and s2 once")
}
