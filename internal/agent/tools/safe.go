package tools

import (
	"runtime"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// readOnlyCommand reports whether command only reads, so the bash tool
// may run it without asking permission, as Claude Code runs its read-only
// commands: the shell parses all of it into commands each of which is one
// of readOnlyChecks, joined only by &&, ||, ; , newlines or pipes, with
// no substitution, assignment or background job, no expansion but a plain
// $NAME, and redirections only from a file, from a here-document with no
// substitution, or to /dev/null. Anything else, or anything it cannot parse, asks. It
// used to take any command that started with a safe name and held no
// ";", "|", "&&", "$(" or backquote, so "echo hi" and a second line, "ls &
// rm -rf ~", "echo x > ~/.zshrc" and "timeout 5 rm -rf ~" all ran
// unasked.
func readOnlyCommand(command string) bool {
	// As Claude Code, a command too long to analyze is asked about.
	if len(command) > 10000 {
		return false
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) == 0 {
		return false
	}
	for _, stmt := range file.Stmts {
		if !readOnlyStmt(stmt) {
			return false
		}
	}
	return true
}

// readOnlyStmt reports whether stmt only reads: see readOnlyCommand.
func readOnlyStmt(stmt *syntax.Stmt) bool {
	if stmt.Background || stmt.Coprocess {
		return false
	}
	for _, redir := range stmt.Redirs {
		if !harmlessRedirect(redir) {
			return false
		}
	}
	switch cmd := stmt.Cmd.(type) {
	case *syntax.CallExpr:
		return readOnlyCall(cmd)
	case *syntax.BinaryCmd:
		// &&, ||, | and |&: each side on its own.
		return readOnlyStmt(cmd.X) && readOnlyStmt(cmd.Y)
	default:
		return false
	}
}

// harmlessRedirect reports whether redir only reads a file or a
// here-document, or writes to /dev/null or another descriptor.
func harmlessRedirect(redir *syntax.Redirect) bool {
	switch redir.Op {
	case syntax.RdrIn:
		_, _, ok := plainWord(redir.Word)
		return ok
	case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.RdrClob:
		target, _, ok := plainWord(redir.Word)
		return ok && target == "/dev/null"
	case syntax.DplOut, syntax.DplIn:
		// 2>&1, >&2, 2>&-: another descriptor, not a file.
		target, _, ok := plainWord(redir.Word)
		return ok && (target == "-" || strings.Trim(target, "0123456789") == "")
	case syntax.Hdoc, syntax.DashHdoc:
		// A here-document's body may hold a command substitution,
		// which would run.
		return redir.Hdoc == nil || harmlessParts(redir.Hdoc.Parts)
	case syntax.WordHdoc:
		_, _, ok := plainWord(redir.Word)
		return ok
	}
	return false
}

// harmlessParts reports whether parts are text and plain $NAME
// expansions only, as a here-document's body or a double-quoted word
// that runs nothing.
func harmlessParts(parts []syntax.WordPart) bool {
	for _, part := range parts {
		switch part := part.(type) {
		case *syntax.Lit:
		case *syntax.ParamExp:
			if !plainParam(part) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// plainParam reports whether p is a plain $NAME or ${NAME}, which only
// puts a variable's value in, with no operator that could assign, run a
// substitution or reach another variable.
func plainParam(p *syntax.ParamExp) bool {
	return p.Param != nil && p.Flags == nil && !p.Excl && !p.Length && !p.Width && !p.IsSet &&
		p.Split == syntax.OptUnset && p.GlobSubst == syntax.OptUnset && p.RcExpand == syntax.OptUnset &&
		p.NestedParam == nil && p.Index == nil && len(p.Modifiers) == 0 &&
		p.Slice == nil && p.Repl == nil && p.Names == 0 && p.Exp == nil
}

// plainWord is word's text, if it runs nothing: plain text, single-quoted
// text, double-quoted text, and plain $NAME expansions. uncertain says
// whether the text the command gets can differ from it: an unquoted glob,
// which could expand to an option, or an expansion, whose value it does
// not know.
func plainWord(word *syntax.Word) (text string, uncertain, ok bool) {
	if word == nil {
		return "", false, false
	}
	var b strings.Builder
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			uncertain = uncertain || strings.ContainsAny(part.Value, "*?[")
			b.WriteString(part.Value)
		case *syntax.SglQuoted:
			if part.Dollar {
				return "", false, false
			}
			b.WriteString(part.Value)
		case *syntax.DblQuoted:
			if !harmlessParts(part.Parts) {
				return "", false, false
			}
			for _, inner := range part.Parts {
				if lit, isLit := inner.(*syntax.Lit); isLit {
					b.WriteString(lit.Value)
				} else {
					uncertain = true
				}
			}
		case *syntax.ParamExp:
			if !plainParam(part) {
				return "", false, false
			}
			uncertain = true
		default:
			return "", false, false
		}
	}
	return b.String(), uncertain, true
}

// readOnlyCall reports whether call runs one of readOnlyChecks with
// arguments that keep it reading only.
func readOnlyCall(call *syntax.CallExpr) bool {
	if len(call.Assigns) > 0 || len(call.Args) == 0 {
		// An assignment such as GIT_EXTERNAL_DIFF=... can make a
		// reading command run a program.
		return false
	}
	args := make([]string, len(call.Args))
	uncertain := false
	for i, word := range call.Args {
		text, unsure, ok := plainWord(word)
		if !ok || (i == 0 && unsure) {
			// The command's own name must be known.
			return false
		}
		args[i], uncertain = text, uncertain || unsure
	}
	check, ok := readOnlyChecks[args[0]]
	if !ok {
		return false
	}
	return check(args[1:], uncertain)
}

// readOnlyCheck reports whether a command, given args, only reads.
// uncertain says whether an argument's text may differ from what the
// command gets (a glob, an expansion): a check that reads the arguments
// asks then.
type readOnlyCheck func(args []string, uncertain bool) bool

// anyArgs is the check of a command no argument makes write or run
// anything.
func anyArgs([]string, bool) bool { return true }

// noArgs is the check of a command that reads only when it is given none.
func noArgs(args []string, _ bool) bool { return len(args) == 0 }

// readOnlyChecks are the commands the bash tool runs without asking, as
// Claude Code runs its read-only set, each with the check of its
// arguments. Wrappers that run another command (timeout, nice, nohup,
// time, env with arguments), commands that change something (kill,
// killall) and forms of git that write are not in it.
var readOnlyChecks = map[string]readOnlyCheck{
	"cal":      anyArgs,
	"cat":      anyArgs,
	"df":       anyArgs,
	"diff":     anyArgs,
	"du":       anyArgs,
	"echo":     anyArgs,
	"free":     anyArgs,
	"grep":     anyArgs,
	"groups":   anyArgs,
	"head":     anyArgs,
	"id":       anyArgs,
	"ls":       anyArgs,
	"printenv": anyArgs,
	"pwd":      anyArgs,
	"stat":     anyArgs,
	"tail":     anyArgs,
	"type":     anyArgs,
	"uname":    anyArgs,
	"uptime":   anyArgs,
	"wc":       anyArgs,
	"whatis":   anyArgs,
	"whereis":  anyArgs,
	"which":    anyArgs,
	"whoami":   anyArgs,
	"env":      noArgs,
	"date": func(args []string, uncertain bool) bool {
		// date -s and --set change the clock.
		return !uncertain && !slices.ContainsFunc(args, func(arg string) bool {
			return strings.HasPrefix(arg, "--set") || (strings.HasPrefix(arg, "-s") && !strings.HasPrefix(arg, "--"))
		})
	},
	"hostname": func(args []string, uncertain bool) bool {
		// A name, or -F and --file with a file of one, sets the hostname;
		// these options only show it.
		return !uncertain && !slices.ContainsFunc(args, func(arg string) bool {
			return !slices.Contains([]string{"-f", "-s", "-i", "-I", "-d", "-a", "-A", "--fqdn", "--short", "--domain", "--ip-address", "--all-ip-addresses", "--alias"}, arg)
		})
	},
	"ps": func(args []string, uncertain bool) bool {
		// ps e (as in "ps auxe") prints every process's environment.
		if uncertain {
			return false
		}
		switch strings.Join(args, " ") {
		case "", "aux", "ax", "-ef", "-e", "-A", "-f":
			return true
		}
		return false
	},
	"git": readOnlyGit,
}

// readOnlyGitCommands are git's subcommands that read only, each with the
// check of its arguments.
var readOnlyGitCommands = map[string]readOnlyCheck{
	"blame":     anyArgs,
	"describe":  anyArgs,
	"diff":      anyArgs,
	"grep":      anyArgs,
	"log":       anyArgs,
	"ls-files":  anyArgs,
	"ls-remote": anyArgs,
	"rev-parse": anyArgs,
	"shortlog":  anyArgs,
	"show":      anyArgs,
	"status":    anyArgs,
	// A name creates a branch or a tag, unless -l lists by pattern.
	"branch": gitListing("-a", "-r", "-v", "-vv", "--all", "--remotes", "--verbose", "--show-current", "--color", "--no-color"),
	"tag":    gitListing("-n", "--color", "--no-color"),
	"remote": func(args []string, _ bool) bool {
		switch strings.Join(args, " ") {
		case "", "-v", "--verbose":
			return true
		}
		return len(args) == 2 && args[0] == "get-url"
	},
	"config": func(args []string, _ bool) bool {
		if len(args) == 0 || !slices.Contains([]string{"--get", "--get-all", "--get-regexp", "--list", "-l"}, args[0]) {
			return false
		}
		return !slices.ContainsFunc(args, func(arg string) bool {
			return slices.Contains([]string{"--add", "--unset", "--unset-all", "--replace-all", "--rename-section", "--remove-section", "--edit", "-e"}, arg)
		})
	},
}

// readOnlyGit is git's check: a reading subcommand right after "git",
// with no global option before it (git -c sets a program to run), no
// glob or expansion (either could become an option), and no option that
// writes a file or runs a program.
func readOnlyGit(args []string, uncertain bool) bool {
	if len(args) == 0 || uncertain {
		return false
	}
	check, ok := readOnlyGitCommands[args[0]]
	if !ok {
		return false
	}
	for _, arg := range args[1:] {
		for _, writes := range []string{"--output", "--ext-diff", "--open-files-in-pager", "-O"} {
			if strings.HasPrefix(arg, writes) {
				return false
			}
		}
	}
	return check(args[1:], uncertain)
}

// gitListing is the check of git branch and git tag: they only list when
// given no name, or names after -l or --list, which are patterns; the
// options they may carry besides are flags.
func gitListing(flags ...string) readOnlyCheck {
	return func(args []string, _ bool) bool {
		listing := slices.Contains(args, "-l") || slices.Contains(args, "--list")
		for _, arg := range args {
			switch {
			case arg == "-l" || arg == "--list" || slices.Contains(flags, arg):
			case strings.HasPrefix(arg, "--sort=") || strings.HasPrefix(arg, "--format=") || strings.HasPrefix(arg, "-n"):
			case !strings.HasPrefix(arg, "-") && listing:
			default:
				return false
			}
		}
		return true
	}
}

func init() {
	if runtime.GOOS == "windows" {
		for _, name := range []string{"ipconfig", "nslookup", "ping", "systeminfo", "tasklist", "where"} {
			readOnlyChecks[name] = anyArgs
		}
	}
}
