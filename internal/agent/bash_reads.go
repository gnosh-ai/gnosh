package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"mvdan.cc/sh/v3/syntax"
)

// bashViewCommands are the commands Claude Code counts as reading a file
// when one is run on a single file, with no pipe or redirect: "Claude can
// also edit a file without a separate Read after it views the file with
// a Bash command such as cat or grep" (code.claude.com/docs/en/
// tools-reference, Edit tool behavior). sed counts only as sed -n 'X,Yp'.
var bashViewCommands = map[string]bool{
	"bat": true, "batcat": true, "cat": true, "egrep": true, "fgrep": true,
	"grep": true, "head": true, "nl": true, "rg": true, "sed": true, "tail": true,
}

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
// naming exactly one regular file among its arguments; or "".
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
	if !bashViewCommands[args[0]] {
		return ""
	}
	if args[0] == "sed" {
		if len(args) != 4 || args[1] != "-n" || !sedPrintRange.MatchString(args[2]) {
			return ""
		}
		return regularFile(dir, args[3])
	}
	// The one argument that is a file is the file shown: an option, a
	// count (head -n 20) or a pattern (grep -n main a.go) is not one. Two
	// files, or none, as for grep -r over a directory, is not one file.
	var shown string
	for _, arg := range args[1:] {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if path := regularFile(dir, arg); path != "" {
			if shown != "" {
				return ""
			}
			shown = path
		}
	}
	return shown
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
