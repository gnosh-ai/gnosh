package shellconfig

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
)

// handleHook implements the `hook` builtin.
//
// Usage:
//
//	hook add <event> --command CMD [--name NAME] [--matcher REGEX] [--timeout N]
//	hook remove <event> [--name NAME]   (alias: rm)
//
// "add" appends a hook to the event's list; multiple hooks per event
// accumulate. "remove" drops the named hook(s) from the event, or clears the
// whole event when no --name is given. Only named hooks can be removed
// individually; give a hook a --name if you intend to remove it later.
func handleHook(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	b := configBuilderFromCtx(ctx)
	if b == nil {
		return nil
	}
	if len(args) < 2 {
		return usage(stderr, "usage: hook add <event> --command CMD [flags] | hook remove <event> [--name NAME]")
	}

	switch args[1] {
	case "add":
		return hookAdd(b, args, stderr)
	case "remove", "rm":
		return hookRemove(b, args, stderr)
	default:
		return usage(stderr, fmt.Sprintf("hook: unknown subcommand %q (expected add or remove)", args[1]))
	}
}

// knownHookEvents are the event names `hook add` accepts. They are
// spelled out here because internal/config imports this package and so
// cannot be imported back; TestKnownHookEventsMatchConfig keeps this
// list and config.KnownHookEvents in step.
//
// Checking here matters because the loader rejects an unknown event
// name outright: without this, `hook add pre_tool_use ...` would write a
// crushrc that Crush then refuses to start with — a startup failure
// caused by a command Crush itself offers. Better to fail on the line
// that is wrong.
var knownHookEvents = []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "UserPromptSubmit", "SessionStart", "Stop", "SubagentStop", "StopFailure", "PreCompact", "PostCompact", "SessionEnd"}

// eventsSupportingMatcher are the events whose hooks fire against a
// named thing: a tool name, the PreCompact or PostCompact trigger, the SessionStart
// source, or the SessionEnd reason. A matcher on any other event matches
// nothing, so the loader rejects it; reject it here too.
var eventsSupportingMatcher = []string{"PreToolUse", "PostToolUse", "PostToolUseFailure", "PreCompact", "PostCompact", "StopFailure", "SessionStart", "SessionEnd"}

// hookAddFlags is the declarative flag surface for `hook add`.
var hookAddFlags = []flagSpec{
	{name: "--command", jsonKey: "command", kind: flagString, op: opSet},
	{name: "--matcher", jsonKey: "matcher", kind: flagString, op: opSet},
	{name: "--timeout", jsonKey: "timeout", kind: flagInt, op: opSet},
	{name: "--name", jsonKey: "name", kind: flagString, op: opSet},
}

func hookAdd(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: hook add <event> --command CMD [--name NAME] [--matcher REGEX] [--timeout N]")
	}
	event := args[2]
	if !slices.Contains(knownHookEvents, event) {
		return usage(stderr, fmt.Sprintf(
			"hook add: unknown event %q (expected %s)",
			event, strings.Join(knownHookEvents, " or "),
		))
	}
	slog.Info("Hook defined in shell config", "event", event)
	h := map[string]any{}

	if err := applyFlags(hookAddFlags, args, 3, h, "hook add", stderr); err != nil {
		return err
	}

	if _, ok := h["command"]; !ok {
		return usage(stderr, "hook add: --command is required")
	}

	// "*" matches everything, as no matcher does, and config takes it on any
	// event, as Claude Code does.
	if m, ok := h["matcher"]; ok && m != "*" && !slices.Contains(eventsSupportingMatcher, event) {
		return usage(stderr, fmt.Sprintf(
			"hook add: --matcher is not supported for %s; there is nothing to match",
			event,
		))
	}

	hooks := b.section("hooks")
	arr, _ := hooks[event].([]any)
	hooks[event] = append(arr, h)

	slog.Debug("Hook recorded", "event", event)
	return nil
}

// hookRemoveFlags is the declarative flag surface for `hook remove`.
var hookRemoveFlags = []flagSpec{
	{name: "--name", jsonKey: "name", kind: flagString, op: opSet},
}

func hookRemove(b *ConfigBuilder, args []string, stderr io.Writer) error {
	if len(args) < 3 {
		return usage(stderr, "usage: hook remove <event> [--name NAME]")
	}
	event := args[2]

	flags := map[string]any{}
	if err := applyFlags(hookRemoveFlags, args, 3, flags, "hook remove", stderr); err != nil {
		return err
	}
	name, _ := flags["name"].(string)

	hooks := b.section("hooks")

	// No name: clear every hook for the event.
	if name == "" {
		delete(hooks, event)
		slog.Info("Hooks cleared in shell config", "event", event)
		return nil
	}

	// Name given: drop matching hooks, keeping the rest.
	arr, _ := hooks[event].([]any)
	kept := make([]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok && m["name"] == name {
			continue
		}
		kept = append(kept, item)
	}
	hooks[event] = kept

	slog.Info("Hook removed in shell config", "event", event, "name", name)
	return nil
}

// KnownHookEventsForTest exposes the accepted event names so a test in
// the external test package can check them against config's list; see
// TestKnownHookEventsMatchConfig.
func KnownHookEventsForTest() []string { return slices.Clone(knownHookEvents) }

// EventsSupportingMatcherForTest exposes the events that accept a
// matcher, for the same cross-package check.
func EventsSupportingMatcherForTest() []string { return slices.Clone(eventsSupportingMatcher) }
