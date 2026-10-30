// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
)

// memSource is a mem input made ready to run as a program's main: its
// statements in order, the declarations that must stand at the top level,
// and the imports it names or uses.
type memSource struct {
	main    string
	decls   []string
	imports []string // specs: `"strings"`, `gnomem "klash/gnomem/v2"`
}

// shapeMemInput makes a mem input runnable, taking what models write:
// statements, a whole program (its package clause dropped, its main called
// last), imports or none (a package used without one is imported), and
// declarations. A declaration of a func stands at the top level; others run
// where they are, in order with the statements. A trailing read of mem
// (mem.Show(), mem.Find(…), mem.Next(…), mem.JSON(…), or mem alone) prints
// what it says.
func shapeMemInput(code string) (memSource, error) {
	prog, err := normalize(code, 1, nil, nil)
	if err != nil {
		return memSource{}, err
	}
	var src memSource
	var main strings.Builder
	for _, o := range prog.ops {
		switch {
		case o.Stmts != "":
			main.WriteString(o.Stmts + "\n")
		case o.File != "" && declaresFunc(o.File):
			src.decls = append(src.decls, o.File)
		case o.File != "":
			main.WriteString(o.File + "\n")
		case o.Eval != "":
			main.WriteString(memShowExpr(o.Eval) + "\n")
		case o.Call != "":
			main.WriteString(o.Call + "()\n")
		}
	}
	src.main = main.String()
	for _, imp := range append(prog.imports, autoImports(prog, nil)...) {
		spec := strconv.Quote(imp.Path)
		if imp.Name != "" {
			spec = imp.Name + " " + spec
		}
		src.imports = append(src.imports, spec)
	}
	return src, nil
}

// declaresFunc reports whether declarations hold a func, which cannot be
// declared inside main.
func declaresFunc(decls string) bool {
	f, err := parser.ParseFile(token.NewFileSet(), "", "package p\n"+decls, parser.SkipObjectResolution)
	if err != nil {
		return true // let gno say what is wrong, at the top level
	}
	for _, d := range f.Decls {
		if _, ok := d.(*ast.FuncDecl); ok {
			return true
		}
	}
	return false
}

// memReads are mem's calls that return text to show.
var memReads = map[string]bool{"Show": true, "Find": true, "Next": true, "JSON": true, "Check": true, "String": true}

// memShowExpr is the statement a mem input's trailing expression becomes:
// a read of mem prints its text; any other call runs as it is, since it may
// return nothing (mem.Done), or what it printed already (mem.Goal); any
// other expression prints its value.
func memShowExpr(expr string) string {
	x, err := parser.ParseExpr(expr)
	if err != nil {
		return expr
	}
	switch x := x.(type) {
	case *ast.Ident:
		if x.Name == "mem" {
			return "showMem_(mem.Show())"
		}
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "mem" && memReads[sel.Sel.Name] {
				return "showMem_(" + expr + ")"
			}
		}
		return expr
	}
	return "println(" + expr + ")"
}

// memHint is what to tell the model about a mem input that failed with
// msg, for the habits of older mems; "" for none.
func memHint(code, msg string) string {
	if m := memMissingRe.FindStringSubmatch(msg); m != nil {
		if hint, ok := memMoved[m[1]]; ok {
			return hint
		}
		return "mem's calls are Goal, Steps, Ask, Start, Done, Drop, Reopen, Note, Wait, Fact, Rule, Forget, Redact, Find, Show, Next and JSON"
	}
	if strings.Contains(msg, "untyped Bigint as StringKind") {
		return `items are named, not numbered: type the name mem printed in backticks, as mem.Done("release-v2/2", "…")`
	}
	if m := memUndeclaredRe.FindStringSubmatch(msg); m != nil {
		if regexp.MustCompile(`mem\.\w+\(\s*` + regexp.QuoteMeta(m[1]) + `[-/]`).MatchString(code) {
			return `a name is a string: quote it, as mem.Done("release-v2/1", "…")`
		}
	}
	if memAssignRe.MatchString(code) {
		return "mem is your memory, kept by klash: change it with its calls (mem.Goal, mem.Done, mem.Fact, …), never by setting it"
	}
	return ""
}

var (
	memMissingRe    = regexp.MustCompile(`missing field (\w+) in \*klash/gnomem/v2\.Mem`)
	memUndeclaredRe = regexp.MustCompile(`name (\w+) not declared`)
	memAssignRe     = regexp.MustCompile(`(^|[^.\w])mem\s*:?=[^=]`)
)

// memMoved says where an older mem's call or field went.
var memMoved = map[string]string{
	"Add":      `mem.Goal("what to get done").Steps("a step", "another") plans a task, and names its steps after it`,
	"Step":     `mem.Steps("goal-name", "a step") adds a step to an item`,
	"Remind":   `mem.Goal("what to do").Wait("2026-12-01") waits for a day; mem.Ask("", "…") is something for the user`,
	"Remember": `mem.Fact("name", "text") keeps a fact, and mem.Show("name") reads it back`,
	"Facts":    `mem.Fact("name", "text") keeps a fact, and mem.Show("name") reads it back`,
	"Items":    `mem.Show("name") shows an item, and mem.JSON("name") gives it as JSON`,
	"Erase":    `mem.Forget("name") erases a closed item and its steps; mem.Redact("text") takes a secret out everywhere`,
}
