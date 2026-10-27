package shellconfig

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/charmbracelet/crush/internal/tooldefs"

	"github.com/charmbracelet/crush/internal/shell"
)

// handlePermissions implements the `permissions` builtin.
//
// Usage:
//
//	permissions allow <tool|'Bash(pattern)'> [...]
//	permissions deny <tool|'Bash(pattern)'> [...]
//
// "allow" adds tools to the allow-list (tools that skip permission
// prompts), and takes Claude Code's Bash(pattern) allow rules, which go
// into permissions.allow and let the commands they match run unasked.
// "deny" hides tools from the agent entirely (options.disabled_tools) — the
// inverse of allow — and takes Claude Code's Bash(pattern) deny rules,
// which go into permissions.deny and stop the commands they match. Adding
// the same tool or rule twice is a no-op.
//
// Precedence: deny wins. If a tool appears in both allow and deny, it is
// still removed from the agent's effective tool set via disabled_tools.
func handlePermissions(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b := configBuilderFromCtx(ctx)
	if b == nil {
		return shell.ErrNotABuiltinHere
	}
	if len(args) < 2 {
		return usage(stderr, "usage: permissions allow|deny <tool> [<tool> ...]")
	}

	switch args[1] {
	case "allow":
		return permissionsAllow(b, args, stderr)
	case "deny":
		return permissionsDeny(b, args, stderr)
	default:
		return usage(stderr, fmt.Sprintf("permissions: unknown subcommand %q (expected allow or deny)", args[1]))
	}
}

// checkToolNames rejects names the loader will refuse, so a typo fails
// on the crushrc line that is wrong rather than at the next launch.
// allowMCP is set for lists that also accept MCP tool names, which are
// only discovered when a server connects.
func checkToolNames(names []string, allowMCP bool, what string, stderr io.Writer) error {
	for _, name := range names {
		if tooldefs.IsKnown(name) || (allowMCP && tooldefs.IsMCPToolName(name)) {
			continue
		}
		suffix := ""
		if allowMCP {
			suffix = fmt.Sprintf(" and is not an MCP tool (%s...)", tooldefs.MCPPrefix)
		}
		return usage(stderr, fmt.Sprintf(
			"%s: %q is not a built-in tool%s; known tools are %s",
			what, name, suffix, strings.Join(tooldefs.Names(), ", "),
		))
	}
	return nil
}

func permissionsAllow(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: permissions allow <tool|'Bash(pattern)'> [...]")
	}
	tools, rules, err := toolsAndRules(args[2:], "permissions allow", stderr)
	if err != nil {
		return err
	}
	if err := checkToolNames(tools, true, "permissions allow", stderr); err != nil {
		return err
	}
	perms := b.section("permissions")
	if len(tools) > 0 {
		allowed, _ := perms["allowed_tools"].([]any)
		for _, tool := range tools {
			if !containsAny(allowed, tool) {
				allowed = append(allowed, tool)
			}
		}
		perms["allowed_tools"] = allowed
	}
	if len(rules) > 0 {
		allow, _ := perms["allow"].([]any)
		for _, rule := range rules {
			if !containsAny(allow, rule) {
				allow = append(allow, rule)
			}
		}
		perms["allow"] = allow
	}

	slog.Info("Permissions allowed in shell config", "tools", tools, "rules", rules)
	return nil
}

// toolsAndRules splits args into tool names and Bash(pattern) rules.
func toolsAndRules(args []string, what string, stderr io.Writer) (tools, rules []string, err error) {
	for _, arg := range args {
		tool, pattern, isRule := strings.Cut(arg, "(")
		if !isRule || !strings.EqualFold(tool, "bash") || !strings.HasSuffix(pattern, ")") {
			tools = append(tools, arg)
			continue
		}
		if strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(pattern, ")"), ":*")) == "" {
			return nil, nil, usage(stderr, fmt.Sprintf("%s: %q names no command", what, arg))
		}
		rules = append(rules, arg)
	}
	return tools, rules, nil
}

// permissionsDeny hides tools from the agent by adding them to
// options.disabled_tools, the inverse of allow, and adds Bash(pattern)
// deny rules to permissions.deny.
func permissionsDeny(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: permissions deny <tool|'Bash(pattern)'> [...]")
	}
	tools, rules, err := toolsAndRules(args[2:], "permissions deny", stderr)
	if err != nil {
		return err
	}
	// deny writes options.disabled_tools, which filters the built-in set
	// only: an MCP name there would be a no-op, so it is not accepted.
	if err := checkToolNames(tools, false, "permissions deny", stderr); err != nil {
		return err
	}
	if len(tools) > 0 {
		opts := b.section("options")
		disabled, _ := opts["disabled_tools"].([]any)
		for _, tool := range tools {
			if !containsAny(disabled, tool) {
				disabled = append(disabled, tool)
			}
		}
		opts["disabled_tools"] = disabled
	}
	if len(rules) > 0 {
		perms := b.section("permissions")
		deny, _ := perms["deny"].([]any)
		for _, rule := range rules {
			if !containsAny(deny, rule) {
				deny = append(deny, rule)
			}
		}
		perms["deny"] = deny
	}

	slog.Info("Permissions denied in shell config", "tools", tools, "rules", rules)
	return nil
}

// containsAny reports whether the slice already holds the given string value.
func containsAny(s []any, v string) bool {
	for _, item := range s {
		if str, ok := item.(string); ok && str == v {
			return true
		}
	}
	return false
}
