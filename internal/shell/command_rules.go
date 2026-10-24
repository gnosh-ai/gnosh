package shell

import (
	"regexp"
	"strings"
)

// CommandRuleBlocker returns a BlockFunc for one of Claude Code's Bash deny
// rules: pattern is what Bash(...) holds, such as "git push *". As in
// Claude Code, the rule matches a command's text, its words joined by
// spaces, with "*" standing for any text, spaces included; a trailing
// " *" that is the rule's only wildcard also matches the bare command
// ("git log *" matches "git log"), and a trailing ":*" is the same as
// " *". A command matches too with the wrappers Claude Code strips taken
// off it first: timeout, nice, nohup, stdbuf, command, builtin, noglob
// and xargs, so "timeout 30 git push" is a "git push" (Claude Code takes
// xargs off only when it has no options; see unwrapCommand). The shell
// checks every command it runs, so a rule also stops a command in a
// pipeline, a subshell, a loop or a command substitution, as Claude
// Code's deny rules do; like them it does not see into sh -c or a program
// named by its path.
func CommandRuleBlocker(pattern string) BlockFunc {
	re := commandRuleRegexp(pattern)
	return func(args []string) bool {
		for _, words := range commandForms(args) {
			if re.MatchString(strings.Join(words, " ")) {
				return true
			}
		}
		return false
	}
}

// commandRuleRegexp is the regular expression a Bash rule's pattern
// matches whole command texts with.
func commandRuleRegexp(pattern string) *regexp.Regexp {
	if base, ok := strings.CutSuffix(pattern, ":*"); ok {
		pattern = base + " *"
	}
	if base, ok := strings.CutSuffix(pattern, " *"); ok && !strings.Contains(base, "*") {
		return regexp.MustCompile(`(?s)^` + regexp.QuoteMeta(base) + `(?: .*)?$`)
	}
	parts := strings.Split(pattern, "*")
	for i, part := range parts {
		parts[i] = regexp.QuoteMeta(part)
	}
	return regexp.MustCompile(`(?s)^` + strings.Join(parts, ".*") + `$`)
}

// commandForms is args, and args with each wrapper Claude Code strips
// before matching taken off in turn: the command a wrapper runs is the
// one a rule is about.
func commandForms(args []string) [][]string {
	forms := [][]string{args}
	for {
		inner := unwrapCommand(args)
		if inner == nil {
			return forms
		}
		forms = append(forms, inner)
		args = inner
	}
}

// unwrapCommand is the command a wrapper in args runs, or nil when args
// is no wrapper Claude Code strips, or runs nothing.
func unwrapCommand(args []string) []string {
	if len(args) < 2 {
		return nil
	}
	rest := args[1:]
	switch args[0] {
	case "nohup", "builtin", "noglob", "time":
		return rest
	case "command":
		// command -v and -V look a command up; they run nothing.
		if rest[0] == "-v" || rest[0] == "-V" {
			return nil
		}
		return skipOptions(rest, nil)
	case "xargs":
		// Claude Code strips only a bare xargs and matches one with
		// options as xargs itself, so its Bash(git push *) does not stop
		// "xargs -n1 git push". The command xargs runs is the one after
		// its options either way, and a rule here only refuses, so the
		// options are skipped: refusing more is the safe side.
		return skipOptions(rest, xargsValued)
	case "nice":
		return skipOptions(rest, map[string]bool{"-n": true, "--adjustment": true})
	case "stdbuf":
		return skipOptions(rest, map[string]bool{"-i": true, "-o": true, "-e": true, "--input": true, "--output": true, "--error": true})
	case "timeout":
		rest = skipOptions(rest, map[string]bool{"-s": true, "-k": true, "--signal": true, "--kill-after": true})
		if len(rest) < 2 {
			return nil
		}
		// The duration.
		return rest[1:]
	}
	return nil
}

// xargsValued are xargs's options that take the next argument as their
// value.
var xargsValued = map[string]bool{
	"-n": true, "-L": true, "-P": true, "-s": true, "-I": true, "-d": true, "-E": true, "-a": true,
	"--max-args": true, "--max-lines": true, "--max-procs": true, "--max-chars": true,
	"--replace": true, "--delimiter": true, "--eof": true, "--arg-file": true,
}

// skipOptions is args past its leading options, each of valued taking
// the next argument as its value unless it is written "--name=value" or
// with the value attached; nil when nothing is left.
func skipOptions(args []string, valued map[string]bool) []string {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		if valued[args[0]] {
			args = args[1:]
		}
		args = args[1:]
	}
	if len(args) == 0 {
		return nil
	}
	return args
}
