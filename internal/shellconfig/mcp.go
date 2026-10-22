package shellconfig

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
)

// handleMCP implements the `mcp` builtin.
//
// Usage:
//
//	mcp add <name> --type stdio|sse|http [--command CMD] [--args ARG ...]
//	    [--env KEY VALUE ...] [--url URL] [--header KEY VALUE ...]
//	    [--timeout N] [--disabled true|false]
//	    [--disabled-tools TOOL ...] [--enabled-tools TOOL ...]
//	    [--sessionless true|false]
//	    [--oauth true|false] [--oauth-client-id ID]
//	    [--oauth-client-secret SECRET] [--oauth-callback-port PORT]
//	mcp remove <name>   (alias: rm)
//
// "add" defines or updates an MCP server; repeated calls with the same <name>
// update the same entry. "remove" deletes it.
func handleMCP(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b := configBuilderFromCtx(ctx)
	if b == nil {
		return nil
	}
	if len(args) < 2 {
		return usage(stderr, "usage: mcp add <name> --type stdio|sse|http [flags] | mcp remove <name>")
	}

	switch args[1] {
	case "add":
		return mcpAdd(b, args, stderr)
	case "remove", "rm":
		return mcpRemove(b, args, stderr)
	default:
		return usage(stderr, fmt.Sprintf("mcp: unknown subcommand %q (expected add or remove)", args[1]))
	}
}

// mcpAddFlags is the declarative flag surface for `mcp add`.
var mcpAddFlags = []flagSpec{
	{name: "--type", jsonKey: "type", kind: flagString, op: opSet},
	{name: "--command", jsonKey: "command", kind: flagString, op: opSet},
	{name: "--args", jsonKey: "args", kind: flagString, op: opAppend},
	{name: "--env", child: "env", kind: flagKeyValue, op: opSetChild},
	{name: "--url", jsonKey: "url", kind: flagString, op: opSet},
	{name: "--header", child: "headers", kind: flagKeyValue, op: opSetChild},
	{name: "--timeout", jsonKey: "timeout", kind: flagInt, op: opSet},
	{name: "--disabled", jsonKey: "disabled", kind: flagBool, op: opSet},
	{name: "--disabled-tools", jsonKey: "disabled_tools", kind: flagString, op: opAppend},
	{name: "--enabled-tools", jsonKey: "enabled_tools", kind: flagString, op: opAppend},
	{name: "--sessionless", jsonKey: "sessionless", kind: flagBool, op: opSet},
	{name: "--oauth", jsonKey: "oauth", kind: flagBool, op: opSet},
	{name: "--oauth-client-id", jsonKey: "oauth_client_id", kind: flagString, op: opSet},
	{name: "--oauth-client-secret", jsonKey: "oauth_client_secret", kind: flagString, op: opSet},
	{name: "--oauth-callback-port", jsonKey: "oauth_callback_port", kind: flagInt, op: opSet},
}

// knownMCPTypes are the connection types `mcp add` accepts. Spelled out
// because internal/config imports this package and cannot be imported
// back; TestKnownMCPTypesMatchConfig keeps the lists in step.
var knownMCPTypes = []string{"stdio", "sse", "http"}

// KnownMCPTypesForTest exposes the accepted types for that cross-package
// check.
func KnownMCPTypesForTest() []string { return slices.Clone(knownMCPTypes) }

func mcpAdd(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: mcp add <name> --type stdio|sse|http [--command CMD] [--args ARG ...] [--env KEY VALUE ...] [--url URL] [--header KEY VALUE ...] [--timeout N] [--disabled true|false] [--disabled-tools TOOL ...] [--enabled-tools TOOL ...] [--sessionless true|false] [--oauth true|false] [--oauth-client-id ID] [--oauth-client-secret SECRET] [--oauth-callback-port PORT]")
	}
	name := args[2]
	slog.Info("MCP server defined in shell config", "name", name)
	m := childMap(b.section("mcp"), name)

	// Default type is stdio.
	if _, ok := m["type"]; !ok {
		m["type"] = "stdio"
	}

	if err := applyFlags(mcpAddFlags, args, 3, m, "mcp add", stderr); err != nil {
		return err
	}

	// The loader refuses to start on a type it cannot open, so catch it
	// on the line that is wrong rather than at the next launch.
	if t, ok := m["type"].(string); ok && !slices.Contains(knownMCPTypes, t) {
		return usage(stderr, fmt.Sprintf(
			"mcp add: unknown type %q (expected %s)",
			t, strings.Join(knownMCPTypes, ", "),
		))
	}

	slog.Debug("MCP recorded", "name", name)
	return nil
}

func mcpRemove(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: mcp remove <name>")
	}
	name := args[2]
	delete(b.section("mcp"), name)
	slog.Info("MCP server removed in shell config", "name", name)
	return nil
}
