package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// --fork-session copies the session resumed, so with no session named,
// none to pick (a bare -r) and no --continue there is nothing to copy:
// klash and klash run say so instead of starting a new session.
func TestForkAlone(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		args  []string
		alone bool
	}{
		{[]string{"--fork-session"}, true},
		{[]string{"--fork-session", "-C"}, false},
		{[]string{"--fork-session", "-s", "abc"}, false},
		{[]string{"--fork-session", "-r", "abc"}, false},
		{[]string{"--fork-session", "--resume=" + pickSession}, false},
		{nil, false},
		{[]string{"-C"}, false},
	} {
		for _, add := range []func(*cobra.Command){addSessionFlags, addRunFlags} {
			cmd := &cobra.Command{Use: "klash"}
			add(cmd)
			require.NoError(t, cmd.ParseFlags(tc.args))
			if tc.alone {
				require.ErrorIs(t, forkAlone(cmd), errForkAlone, tc.args)
			} else {
				require.NoError(t, forkAlone(cmd), tc.args)
			}
		}
	}
}

// `klash run --fork-session`, as Claude Code's `claude -p --resume ID
// --fork-session`: the run goes on in a copy of the session it resumes,
// the most recent with -C or one by ID, and the session is left as it
// was. The copy's first prompt runs the SessionStart hooks with source
// "fork", and going back to the copy later is a resume.
func TestRunForkSession(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("KLASH_GLOBAL_CONFIG", filepath.Join(isolated, ".config", "klash"))
	t.Setenv("KLASH_GLOBAL_DATA", filepath.Join(isolated, ".local", "share", "klash"))
	for _, name := range []string{"KLASH_DISABLE_PROVIDER_AUTO_UPDATE", "KLASH_DISABLE_DEFAULT_PROVIDERS", "KLASH_DISABLE_METRICS", "KLASH_DISABLE_UPDATE_CHECK"} {
		t.Setenv(name, "1")
	}
	srv := standInModel(t)
	proj := t.TempDir()
	sources := filepath.Join(t.TempDir(), "sources")
	cfg := `{"providers": {"standin": {"type": "llamacpp", "base_url": "` + srv.URL + `"}},
		"models": {"large": {"model": "stand-in", "provider": "standin"}, "small": {"model": "stand-in", "provider": "standin"}},
		"hooks": {"SessionStart": [{"command": "echo \"$KLASH_SESSION_START_SOURCE\" >> ` + sources + `"}]}}`
	require.NoError(t, os.WriteFile(filepath.Join(proj, "klash.json"), []byte(cfg), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(proj, "a.txt"), []byte("alpha\n"), 0o644))
	t.Chdir(proj)

	run := func(args ...string) string {
		t.Helper()
		out, err := runForTest(t, append([]string{"--output-format", "json"}, args...)...)
		require.NoError(t, err)
		var res map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &res), out)
		return res["session_id"].(string)
	}
	original := run("list the files here")
	fork := run("--fork-session", "-C", "list the files here")
	byID := run("--fork-session", "-s", original, "list the files here")
	require.NotEqual(t, original, fork)
	require.NotContains(t, []string{original, fork}, byID)
	require.Equal(t, fork, run("-s", fork, "list the files here"))
	data, err := os.ReadFile(sources)
	require.NoError(t, err)
	require.Equal(t, "startup\nfork\nfork\nresume\n", string(data))

	_, err = runForTest(t, "--fork-session", "list the files here")
	require.ErrorIs(t, err, errForkAlone)

	conn, err := db.Connect(t.Context(), filepath.Join(proj, ".klash"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	messages := message.NewService(db.New(conn))
	conversation := func(sessionID string) []message.Message {
		t.Helper()
		msgs, err := messages.List(t.Context(), sessionID)
		require.NoError(t, err)
		return msgs
	}
	// The stand-in answers a turn with a call to ls and then the answer,
	// and a turn whose conversation has ls's result at once.
	turn := conversation(original)
	require.Len(t, turn, 4, "the session forked is left as it was: its one turn")
	for id, later := range map[string]int{byID: 1, fork: 2} {
		copied := conversation(id)
		require.Len(t, copied, len(turn)+2*later, "the session's turn, then the copy's own")
		for i, msg := range turn {
			require.Equal(t, msg.Parts, copied[i].Parts)
		}
	}
}
