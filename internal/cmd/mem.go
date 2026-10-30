package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/crush/internal/app"
	"github.com/charmbracelet/crush/internal/gnovm"
	"github.com/charmbracelet/crush/internal/version"
	"github.com/spf13/cobra"
)

var memCmd = &cobra.Command{
	Use:   "mem [show [name...] | find <words> | log [n]]",
	Short: "Show the project's mem",
	Long: "Show what the project's mem holds, as /mem does in a session, with no model: its outline, items in full, " +
		"a search, or its last changes. It only reads; /mem in a session also undoes, forgets, redacts and repairs.",
	Example: `
# The outline of this project's mem
gnosh mem

# A goal in full, with its steps and notes
gnosh mem show release-v2

# Search it, by words, and by meaning with mem_embeddings_url set
gnosh mem find staging database

# The last 50 changes, with who made each
gnosh mem log 50
  `,
	RunE: runMem,
}

func runMem(cmd *cobra.Command, args []string) error {
	switch {
	case len(args) == 0, args[0] == "show", args[0] == "find", args[0] == "log":
	case args[0] == "undo", args[0] == "forget", args[0] == "redact", args[0] == "repair":
		return fmt.Errorf("%s mem only reads mem: type /mem %s in a session", version.Command, args[0])
	default:
		return fmt.Errorf("%s mem takes show, find or log, not %q", version.Command, args[0])
	}
	ctx, svc, done, err := sessionSetup(cmd)
	if err != nil {
		return err
	}
	defer done()

	out := cmd.OutOrStdout()
	opts := gnovm.Options{Dir: app.GnoVMDir(), Embeddings: svc.cfg.Config().Options.MemEmbeddingsURL}
	project := svc.cfg.WorkingDir()
	if _, err := os.Stat(filepath.Dir(gnovm.MemStatusPath(opts.Dir, project))); os.IsNotExist(err) {
		fmt.Fprintln(out, "no mem here yet: a session makes one when it first keeps something")
		return nil
	}
	titles := map[string]string{}
	if sessions, err := svc.sessions.List(ctx); err == nil {
		for _, s := range sessions {
			titles[s.ID] = s.Title
		}
	}
	// One read needs no worker: the host runs here, and takes mem's lock
	// as a worker does, beside any gnosh with the project open.
	client, err := gnovm.Acquire(gnovm.ClientOptions{Options: opts, InProcess: true})
	if err != nil {
		return err
	}
	defer client.Release()
	text, _, err := client.MemCommand(ctx, project, "", strings.Join(args, " "), titles)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, strings.TrimRight(text, "\n"))
	return nil
}
