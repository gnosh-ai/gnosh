package cmd

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/gnovm"
	"github.com/stretchr/testify/require"
)

// gnosh mem prints what /mem shows, with no model: the outline, an item in
// full, a search, the log. It only reads, and a project with no mem is
// told so, rather than given an empty one. It needs no provider: the docs
// session's run with none configured stopped at loading the config.
func TestMemCommandReadsTheProjectsMem(t *testing.T) {
	t.Setenv("GNOSH_GLOBAL_CONFIG", t.TempDir())
	t.Setenv("GNOSH_GLOBAL_DATA", t.TempDir())
	t.Setenv("GNOSH_DISABLE_PROVIDER_AUTO_UPDATE", "1")
	t.Setenv("GNOSH_DISABLE_DEFAULT_PROVIDERS", "1")
	project, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Chdir(project)

	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		memCmd.SetContext(t.Context())
		memCmd.SetOut(&out)
		err := memCmd.RunE(memCmd, args)
		return out.String(), err
	}

	out, err := run()
	require.NoError(t, err)
	require.Equal(t, "no mem here yet: a session makes one when it first keeps something\n", out)
	require.NoDirExists(t, filepath.Dir(gnovm.MemStatusPath(app.GnoVMDir(), project)))

	// A session keeps a goal with a step.
	c, err := gnovm.Acquire(gnovm.ClientOptions{Options: gnovm.Options{Dir: app.GnoVMDir()}, InProcess: true})
	require.NoError(t, err)
	_, err = c.Run(t.Context(), gnovm.RunRequest{Project: project, Session: "s1", Name: gnovm.MemVM, Code: `mem.Todo("ship the release").Todo("tag v2.0.0")`})
	c.Release()
	require.NoError(t, err)

	out, err = run()
	require.NoError(t, err)
	require.Contains(t, out, `ship-release`)
	out, err = run("show", "ship-release")
	require.NoError(t, err)
	require.Contains(t, out, "tag v2.0.0")
	out, err = run("find", "release")
	require.NoError(t, err)
	require.Contains(t, out, "ship-release")
	out, err = run("log")
	require.NoError(t, err)
	require.Contains(t, out, "ship the release")

	// What mem refuses is an error, so a script can tell, named for the
	// shell (QA, 2026-10-30).
	_, err = run("show", "nope")
	require.EqualError(t, err, "no item `nope`; gnosh mem lists them")
	_, err = run("find")
	require.EqualError(t, err, "gnosh mem find takes words: gnosh mem find release key")

	_, err = run("forget", "ship-release")
	require.EqualError(t, err, "gnosh mem only reads mem: type /mem forget in a session")
	_, err = run("drop")
	require.EqualError(t, err, `gnosh mem takes show, find or log, not "drop"`)
	out, err = run("show", "ship-release")
	require.NoError(t, err)
	require.Contains(t, out, "tag v2.0.0", "forget did nothing")
}
