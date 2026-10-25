package tools

import (
	"regexp"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// blockedSleep is the sleep, in seconds, that a command may not start
// with in the foreground where the agent is told when a background
// command ends, as Claude Code has it.
const blockedSleep = 25

// sleepBlocked is Claude Code's refusal of such a command, with klash's
// way to wait on a condition in place of its Monitor tool: an until-loop
// in the background, whose end the agent is told of.
const sleepBlocked = "Blocked: %s. To wait for a condition, run an until-loop with run_in_background: true (e.g. `until <check>; do sleep 2; done`); you will be told when it ends. To wait for a command you started, use run_in_background: true. Do not chain shorter sleeps to work around this block."

// sleepSeconds is a sleep's argument Claude Code reads as seconds.
var sleepSeconds = regexp.MustCompile(`^\d+(\.\d*)?$`)

// foregroundSleep says, in Claude Code's words, how command starts with a
// wait it blocks: "standalone sleep 30", or "sleep 30 followed by: tail
// log", when its first command sleeps blockedSleep seconds or more. It is
// "" for any other command, and for one the shell cannot parse.
func foregroundSleep(command string) string {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return ""
	}
	// The commands of the lists and pipelines, in order; a compound one,
	// such as an until-loop, stays whole.
	var parts []*syntax.Stmt
	var flatten func(*syntax.Stmt)
	flatten = func(stmt *syntax.Stmt) {
		if bin, ok := stmt.Cmd.(*syntax.BinaryCmd); ok {
			flatten(bin.X)
			flatten(bin.Y)
			return
		}
		parts = append(parts, stmt)
	}
	for _, stmt := range file.Stmts {
		flatten(stmt)
	}
	if len(parts) == 0 || parts[0].Background {
		return ""
	}
	call, ok := parts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) > 0 || len(call.Args) != 2 {
		return ""
	}
	words := plainWords(call)
	if len(words) != 2 || words[0] != "sleep" || !sleepSeconds.MatchString(words[1]) {
		return ""
	}
	seconds, err := strconv.ParseFloat(words[1], 64)
	if err != nil || seconds < blockedSleep {
		return ""
	}
	sleep := "sleep " + strconv.FormatFloat(seconds, 'f', -1, 64)
	var rest []string
	for _, part := range parts[1:] {
		var b strings.Builder
		if err := syntax.NewPrinter().Print(&b, part); err == nil {
			rest = append(rest, strings.TrimSpace(b.String()))
		}
	}
	if len(rest) == 0 {
		return "standalone " + sleep
	}
	return sleep + " followed by: " + strings.Join(rest, " ")
}
