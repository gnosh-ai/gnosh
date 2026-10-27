package shellconfig

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/charmbracelet/crush/internal/shell"
)

// handleHost implements the `host` builtin.
//
// Usage:
//
//	host add <name> --ssh DEST [--description TEXT]
//	host remove <name>   (alias: rm)
//
// "add" declares another machine the bash tool can run a command on, by
// name, through ssh; repeated calls with the same <name> update the same
// entry. "remove" forgets it. Outside a config file the `host` on the PATH
// (the DNS lookup) is meant, so the builtin steps aside.
func handleHost(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b := configBuilderFromCtx(ctx)
	if b == nil {
		return shell.ErrNotABuiltinHere
	}
	if len(args) < 2 {
		return usage(stderr, "usage: host add <name> --ssh DEST [--description TEXT] | host remove <name>")
	}

	switch args[1] {
	case "add":
		return hostAdd(b, args, stderr)
	case "remove", "rm":
		return hostRemove(b, args, stderr)
	default:
		return usage(stderr, fmt.Sprintf("host: unknown subcommand %q (expected add or remove)", args[1]))
	}
}

// hostAddFlags is the declarative flag surface for `host add`.
var hostAddFlags = []flagSpec{
	{name: "--ssh", jsonKey: "ssh", kind: flagString, op: opSet},
	{name: "--description", jsonKey: "description", kind: flagString, op: opSet},
}

func hostAdd(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: host add <name> --ssh DEST [--description TEXT]")
	}
	name := args[2]
	slog.Info("Host declared in shell config", "name", name)
	h := childMap(b.section("hosts"), name)

	if err := applyFlags(hostAddFlags, args, 3, h, "host add", stderr); err != nil {
		return err
	}

	slog.Debug("Host recorded", "name", name)
	return nil
}

func hostRemove(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: host remove <name>")
	}
	name := args[2]
	delete(b.section("hosts"), name)
	slog.Info("Host removed in shell config", "name", name)
	return nil
}
