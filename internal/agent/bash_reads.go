package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"mvdan.cc/sh/v3/syntax"
)

// viewOptions is what one of the bash commands Claude Code counts as
// reading a file may carry on a view of one file: flags that take no
// value, single-letter options whose value is attached (-n20, -A3) or the
// next argument (-n 20), and long options, those with a value written
// "--name=". Any other option could search more than the file (grep -r),
// show something else than its content (grep -l, grep -c, rg --files) or
// take a value this does not know to skip, so the command is no read.
// numeric takes head's and tail's -20; pattern says the first operand is
// the pattern, not a file, unless -e or --regexp gives it.
type viewOptions struct {
	flags, valued string
	long          []string
	numeric       bool
	pattern       bool
}

var (
	batOptions = viewOptions{flags: "pPnA", valued: "rl", long: []string{
		"--plain", "--number", "--show-all", "--style=", "--line-range=", "--language=",
		"--paging=", "--color=", "--decorations=", "--wrap=", "--theme=",
	}}
	grepOptions = viewOptions{flags: "nHhiywxFEGPsaIbvoU", valued: "ABCme", pattern: true, long: []string{
		"--line-number", "--with-filename", "--no-filename", "--ignore-case", "--no-ignore-case",
		"--word-regexp", "--line-regexp", "--fixed-strings", "--extended-regexp", "--basic-regexp",
		"--perl-regexp", "--no-messages", "--text", "--byte-offset", "--invert-match",
		"--only-matching", "--color", "--colour", "--color=", "--colour=", "--after-context=",
		"--before-context=", "--context=", "--max-count=", "--regexp=",
	}}
	// bashViewCommands are the commands Claude Code counts as reading a
	// file when one is run on a single file, with no pipe or redirect:
	// "Claude can also edit a file without a separate Read after it views
	// the file with a Bash command such as cat or grep" (code.claude.com/
	// docs/en/tools-reference, Edit tool behavior), and what each may
	// carry. sed counts only as sed -n 'X,Yp' FILE.
	bashViewCommands = map[string]viewOptions{
		"cat":    {flags: "nbsvAeEtTu"},
		"nl":     {valued: "bw"},
		"bat":    batOptions,
		"batcat": batOptions,
		"head":   {flags: "qv", valued: "nc", numeric: true},
		"tail":   {flags: "qv", valued: "nc", numeric: true},
		"grep":   grepOptions,
		"egrep":  grepOptions,
		"fgrep":  grepOptions,
		"rg": {flags: "nNiSswxFHIav", valued: "ABCme", pattern: true, long: []string{
			"--line-number", "--no-line-number", "--ignore-case", "--smart-case", "--case-sensitive",
			"--word-regexp", "--line-regexp", "--fixed-strings", "--with-filename", "--no-filename",
			"--no-heading", "--heading", "--text", "--invert-match", "--color=", "--after-context=",
			"--before-context=", "--context=", "--max-count=", "--regexp=",
		}},
	}
)

// sedPrintRange is the script of sed -n 'X,Yp': print lines X to Y, or
// line X alone.
var sedPrintRange = regexp.MustCompile(`^[0-9]+(,([0-9]+|\$))?p$`)

// fileViewedByBash is the file a finished bash call showed the agent, as
// Claude Code counts one, or "": the call printed something and exited 0,
// and its command is one of bashViewCommands run on one regular file. A
// model used to Claude Code reads with sed -n and grep and then edits;
// Klash refused each such edit as of a file never read (run 17, four in
// its first seven minutes).
func fileViewedByBash(call fantasy.ToolCall, resp fantasy.ToolResponse) string {
	var meta tools.BashResponseMetadata
	if err := json.Unmarshal([]byte(resp.Metadata), &meta); err != nil ||
		meta.Background || meta.ExitCode != 0 || strings.TrimSpace(meta.Output) == "" {
		return ""
	}
	var params tools.BashParams
	if err := json.Unmarshal([]byte(call.Input), &params); err != nil {
		return ""
	}
	return fileViewedBy(params.Command, meta.WorkingDirectory)
}

// fileViewedBy is the file command shows when run in dir, if it is one
// of bashViewCommands with no pipe, redirect, assignment or expansion,
// with only the options that command may carry on a view, and exactly
// one file operand, a regular file; or "". Taking whichever argument
// named a file let a search pattern pass for one: rg README.md searches
// every file for that text, and counted as reading README.md.
func fileViewedBy(command, dir string) string {
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) != 1 {
		return ""
	}
	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(stmt.Redirs) > 0 || stmt.Background || stmt.Coprocess || stmt.Negated ||
		len(call.Assigns) > 0 || len(call.Args) < 2 {
		return ""
	}
	args := make([]string, len(call.Args))
	for i, word := range call.Args {
		if args[i], ok = literalWord(word); !ok {
			return ""
		}
	}
	if args[0] == "sed" {
		if len(args) != 4 || args[1] != "-n" || !sedPrintRange.MatchString(args[2]) {
			return ""
		}
		return regularFile(dir, args[3])
	}
	opts, ok := bashViewCommands[args[0]]
	if !ok {
		return ""
	}
	operands, ok := viewOperands(opts, args[1:])
	if !ok || len(operands) != 1 {
		return ""
	}
	return regularFile(dir, operands[0])
}

// viewOperands is the files a command with opts names in args, its
// arguments: what is left of them once its options, their values and,
// for a search, its pattern are taken out. ok is false for an option
// opts does not allow.
func viewOperands(opts viewOptions, args []string) (operands []string, ok bool) {
	patternGiven := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			operands = append(operands, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(arg, "--"):
			name, _, valued := strings.Cut(arg, "=")
			if valued {
				name += "="
			}
			if !slices.Contains(opts.long, name) {
				return nil, false
			}
			patternGiven = patternGiven || name == "--regexp="
		case len(arg) > 1 && arg[0] == '-':
			if opts.numeric && strings.Trim(arg[1:], "0123456789") == "" {
				continue
			}
			for j := 1; j < len(arg); j++ {
				c := arg[j]
				if strings.IndexByte(opts.flags, c) >= 0 {
					continue
				}
				if strings.IndexByte(opts.valued, c) < 0 {
					return nil, false
				}
				patternGiven = patternGiven || c == 'e'
				if j == len(arg)-1 {
					// The value is the next argument.
					if i++; i >= len(args) {
						return nil, false
					}
				}
				break
			}
		default:
			operands = append(operands, arg)
		}
	}
	if opts.pattern && !patternGiven {
		if len(operands) == 0 {
			return nil, false
		}
		operands = operands[1:]
	}
	return operands, true
}

// literalWord is a shell word's text when the shell would pass it as
// written: plain text, or single- or double-quoted text, with no
// expansion, glob, brace, tilde or escape. Any other word is not one.
func literalWord(word *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, part := range word.Parts {
		switch part := part.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(part.Value, `*?[{\~`) {
				return "", false
			}
			b.WriteString(part.Value)
		case *syntax.SglQuoted:
			if part.Dollar {
				return "", false
			}
			b.WriteString(part.Value)
		case *syntax.DblQuoted:
			for _, inner := range part.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok || strings.Contains(lit.Value, `\`) {
					return "", false
				}
				b.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// regularFile is arg, resolved against dir, if it names a regular file.
func regularFile(dir, arg string) string {
	path := arg
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return filepath.Clean(path)
}
