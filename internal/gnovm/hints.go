// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	undeclaredRe   = regexp.MustCompile(`name (\w+) not declared`)
	missingFieldRe = regexp.MustCompile(`missing field (\w+) in ` + replPath + `\.(\w+)`)
	// The GnoVM's words where Gno is not Go: a builtin's name taken, and
	// an untyped constant shifted by a variable, which it types as an
	// untyped bigint, then refuses an int for or crashes on.
	shadowedBuiltinRe = regexp.MustCompile(`builtin identifiers cannot be shadowed: (\w+)`)
	untypedBigintRe   = regexp.MustCompile(`<untyped> bigint|BigintValue`)
	constShiftRe      = regexp.MustCompile(`\b\d+\s*<<\s*[A-Za-z_(]`)
)

// goHint says where a failed input relied on Go's way and Gno differs, in
// words the GnoVM's own do not give. Jae's lane worked a backoff sum out in
// the scratch VM with `cap := 30000` and `200 * (1 << (i - 1))`, both Go,
// and spent minutes on the two errors (2026-10-30). ops are the failed
// call's.
func goHint(ops []op, err error) string {
	msg := err.Error()
	if m := shadowedBuiltinRe.FindStringSubmatch(msg); m != nil {
		return fmt.Sprintf("Gno does not let a variable take a builtin's name, as Go does: rename %s", m[1])
	}
	if m := undeclaredRe.FindStringSubmatch(msg); m != nil && (m[1] == "min" || m[1] == "max" || m[1] == "clear") {
		return fmt.Sprintf("Gno has no %s builtin, Go 1.21's: compare with if, or write a func", m[1])
	}
	if untypedBigintRe.MatchString(msg) {
		for _, o := range ops {
			if constShiftRe.MatchString(o.Stmts + o.File + o.Eval) {
				return "Gno types a shift of an untyped constant by a variable wrongly (gnolang/gno#6393): give the constant a type, int(1) << n, or the variable one, var w int = …"
			}
		}
	}
	return ""
}

// compileHint says what a compile error most likely means, where the GnoVM's
// own words led models to a second wrong try: a name that is a VM's, or is
// declared in another VM (the call ran in the wrong VM, often the scratch VM
// for want of a name), and a field the struct does not have. ops are the
// failed call's.
func (h *Host) compileHint(s *vmState, ops []op, err error) string {
	var cerr *compileError
	if !errors.As(err, &cerr) {
		return ""
	}
	if m := undeclaredRe.FindStringSubmatch(cerr.msg); m != nil {
		return h.undeclaredHint(s, m[1])
	}
	if m := missingFieldRe.FindStringSubmatch(cerr.msg); m != nil {
		srcs := journalFiles(s)
		for _, o := range ops {
			if o.File != "" {
				srcs = append(srcs, o.File)
			}
		}
		if fields := structFields(srcs, m[2]); fields != nil {
			return fmt.Sprintf("%s has no field %s; its fields are %s", m[2], m[1], strings.Join(fields, ", "))
		}
	}
	return ""
}

func (h *Host) undeclaredHint(s *vmState, name string) string {
	if name == MemVM {
		return `mem is your memory, kept by gnosh: run this with name "mem"`
	}
	if name == s.name {
		return fmt.Sprintf("%s is this VM's name, not a value in it: use the names declared in it directly, without %s.", name, name)
	}
	states, err := h.projectStates(s.project)
	if err != nil {
		return ""
	}
	for _, other := range states {
		if other == s || strings.HasPrefix(other.name, scratchPrefix) {
			continue
		}
		if other.name == name {
			return fmt.Sprintf("%s is another VM, not a value: run code that uses what it holds in it, with name %q", name, name)
		}
		if declares(other, name) {
			return fmt.Sprintf("%s is declared in VM %s, not in this one: run this with name %q", name, other.name, other.name)
		}
	}
	return ""
}

// declaredElsewhere returns those of names that another named VM of s's
// project declares (see normalize's elsewhere).
func (h *Host) declaredElsewhere(s *vmState, names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	states, err := h.projectStates(s.project)
	if err != nil {
		return nil
	}
	var out map[string]bool
	for _, other := range states {
		if other == s || strings.HasPrefix(other.name, scratchPrefix) {
			continue
		}
		for _, n := range names {
			if declares(other, n) {
				if out == nil {
					out = map[string]bool{}
				}
				out[n] = true
			}
		}
	}
	return out
}

// projectStates looks up every VM of a project, on disk or in memory.
func (h *Host) projectStates(project string) ([]*vmState, error) {
	var out []*vmState
	seen := map[string]bool{}
	for _, dir := range []string{filepath.Join(h.projectDir(project), "vms"), h.ephemeralRoot(project)} {
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() || seen[e.Name()] {
				continue
			}
			seen[e.Name()] = true
			s, err := h.lookup(project, e.Name())
			if err != nil || s == nil {
				continue
			}
			out = append(out, s)
		}
	}
	return out, nil
}

// declares reports whether a VM's package has name, without rebuilding the
// VM: a resident one is asked, and otherwise its journal is read.
func declares(s *vmState, name string) bool {
	if s.res != nil {
		return s.res.names()[name]
	}
	tail, _, err := readEntries(filepath.Join(s.dir, "journal.jsonl"), 0, 0)
	if err != nil {
		return false
	}
	for _, t := range tail {
		for _, o := range t.Ops {
			switch {
			case o.Import != nil && o.Import.Name == name:
				return true
			case o.File != "":
				for _, n := range declaredNames(o.File) {
					if n == name {
						return true
					}
				}
			}
		}
	}
	return false
}

// journalFiles returns the declarations a VM's journal holds.
func journalFiles(s *vmState) []string {
	tail, _, err := readEntries(filepath.Join(s.dir, "journal.jsonl"), 0, 0)
	if err != nil {
		return nil
	}
	var out []string
	for _, t := range tail {
		for _, o := range t.Ops {
			if o.File != "" {
				out = append(out, o.File)
			}
		}
	}
	return out
}

// structFields lists the fields of the struct type name declares in srcs,
// the last declaration of it winning, or nil if none does.
func structFields(srcs []string, name string) []string {
	var fields []string
	for _, src := range srcs {
		f, err := parser.ParseFile(token.NewFileSet(), "", "package p;"+src, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != name {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return false
			}
			fields = []string{}
			for _, fl := range st.Fields.List {
				if len(fl.Names) == 0 { // embedded
					fields = append(fields, exprString(fl.Type))
				}
				for _, n := range fl.Names {
					fields = append(fields, n.Name)
				}
			}
			return false
		})
	}
	return fields
}

func exprString(x ast.Expr) string {
	switch x := x.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return exprString(x.X)
	case *ast.SelectorExpr:
		return x.Sel.Name
	}
	return "?"
}
