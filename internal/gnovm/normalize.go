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
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"path"
	"strconv"
	"strings"
)

// maxInput is the largest input gnovm_run takes.
const maxInput = 256 << 10

// op is one step of a journal entry. Inputs are journaled as ops, not as the
// text the model sent, so replay runs exactly what ran, whatever later
// versions of the normalisation below do.
type op struct {
	// Import binds Name to the package at Path, package-wide.
	Import *importOp `json:"import,omitempty"`
	// File holds top-level declarations, run as a new file of the package:
	// declarations in one file may refer to each other in any order.
	File string `json:"file,omitempty"`
	// Stmts holds statements, run in order in the package block.
	Stmts string `json:"stmts,omitempty"`
	// Call names a function to call: an input's main, renamed.
	Call string `json:"call,omitempty"`
	// Eval is an expression whose value is shown.
	Eval string `json:"eval,omitempty"`
	// Line is the input line File or Stmts began on, so errors point at
	// the model's own lines.
	Line int `json:"line,omitempty"`
}

// declBytes is the source an op keeps in memory once it has run: a file's
// declarations, and the function literals in statements or an evaluation (a
// stored closure keeps its syntax tree; other statements keep nothing). The
// budget charges it (vmCharge) and the limits cap it (checkLimits).
func (o op) declBytes() int64 {
	switch {
	case o.File != "":
		return int64(len(o.File))
	case o.Stmts != "":
		return funcLitBytes(o.Stmts, true)
	case o.Eval != "":
		return funcLitBytes(o.Eval, false)
	}
	return 0
}

// funcLitBytes sums the source of the outermost function literals in
// statements (stmts) or an expression.
func funcLitBytes(src string, stmts bool) int64 {
	if !strings.Contains(src, "func") {
		return 0
	}
	fset := token.NewFileSet()
	var root ast.Node
	if stmts {
		f, err := parser.ParseFile(fset, "", "package p;func _(){"+src+"\n}", parser.SkipObjectResolution)
		if err != nil {
			return int64(len(src)) // cannot tell: count it all
		}
		root = f
	} else {
		x, err := parser.ParseExpr(src)
		if err != nil {
			return int64(len(src))
		}
		root = x
	}
	var n int64
	ast.Inspect(root, func(node ast.Node) bool {
		if fl, ok := node.(*ast.FuncLit); ok {
			n += int64(fl.End() - fl.Pos())
			return false
		}
		return true
	})
	return n
}

type importOp struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (i *importOp) source() string { return fmt.Sprintf("import %s %q", i.Name, i.Path) }

// importSpec is an import as written: Name is "" unless it was named.
type importSpec struct {
	Name, Path string
}

// program is an input, normalised.
type program struct {
	// ops are the input's steps in order, without its imports, which the
	// caller turns into import ops once it knows the package names.
	ops     []op
	imports []importSpec
	// pkgClause is set if the input began with a package clause.
	pkgClause bool
	// main is what a declared func main was renamed to, or "".
	main string
	// readOnly marks the ops that only read, aligned with ops: they change
	// nothing, so they need not be journaled. An eval is read-only if it is
	// pure; statements are, if they only print literals. The VM must
	// confirm that no builtin is redeclared.
	readOnly []bool
	// usesFmt is set if a read-only op prints with fmt, which counts as a
	// read only if the VM's fmt is the real package.
	usesFmt bool
	// assigned lists the names an assignment declared (see normalize).
	assigned []string
	// lhs holds every name a top-level = or := assigns.
	lhs map[string]bool
}

// normalize turns Go-shaped input into ops. Models write whole programs
// (package clause, imports, func main), REPL lines, or a mix; the GnoVM runs
// declarations, statements and expressions through different entry points,
// so the input is split at its top level:
//
//   - a package clause is dropped;
//   - imports are collected, to be bound package-wide before anything runs;
//   - consecutive declarations become one file, so they may refer to each
//     other in any order (main before its helpers, mutual recursion, a
//     method before its type);
//   - consecutive statements become one statement run;
//   - a trailing lone expression becomes an eval, whose value is shown;
//   - a func main is renamed main_<seq> and called after everything else,
//     so one VM can run many programs;
//   - a top-level x, y := ... whose names are all new becomes var x, y = ...,
//     a declaration: a file's var initializer that depends on a name a :=
//     statement declared makes the GnoVM panic, and data := ... followed by
//     var r = f(data) is ordinary code. A := that reuses a name stays a
//     statement, which gno runs as an assignment;
//   - so does a top-level x, y = ... whose names are all new: models write
//     tasks = []Task{...} for a first value as often as :=, and gno refuses
//     an assignment to an undeclared name, which cost a round trip. Not
//     when the right side uses a name (x = append(x, 1) needs an x), nor for
//     a name in elsewhere, which another VM declares: there the model has
//     most likely run the call in the wrong VM, and the failure says which
//     VM has the name, where a declaration here would hide the mistake;
//   - a trailing call to println or fmt.Print* stays a statement: its
//     results (fmt.Println's byte count and nil error) are not worth showing.
//
// seq is the journal sequence number the input will have, and existing the
// names the VM's package already has.
func normalize(code string, seq int64, existing, elsewhere map[string]bool) (*program, error) {
	if len(code) > maxInput {
		return nil, &refusal{fmt.Sprintf("the input is %d bytes; the most a call takes is %d", len(code), maxInput)}
	}
	src := []byte(code)
	fset := token.NewFileSet()
	file := fset.AddFile("input", -1, len(src))
	var errs scanner.ErrorList
	var s scanner.Scanner
	s.Init(file, src, func(pos token.Position, msg string) { errs.Add(pos, msg) }, 0)
	var toks []tok
	for {
		pos, t, lit := s.Scan()
		if t == token.EOF {
			break
		}
		toks = append(toks, tok{off: file.Offset(pos), t: t, lit: lit})
	}
	if errs.Len() > 0 {
		errs.Sort()
		return nil, &parseError{errs.Err()}
	}
	p := &program{}

	// Drop a package clause, keeping every later position where it was.
	if len(toks) >= 2 && toks[0].t == token.PACKAGE && toks[1].t == token.IDENT {
		end := toks[1].off + len(toks[1].lit)
		for i := toks[0].off; i < end; i++ {
			if src[i] != '\n' {
				src[i] = ' '
			}
		}
		toks = toks[2:]
		if len(toks) > 0 && toks[0].t == token.SEMICOLON {
			toks = toks[1:]
		}
		p.pkgClause = true
	}

	stmts := splitTop(toks, len(src))
	// A func main is renamed, with every use of the name in the input.
	for _, st := range stmts {
		if st.kind == kindDecl && st.isMain(toks) {
			p.main = fmt.Sprintf("main_%d", seq)
			break
		}
	}
	text := func(from, to int) string { return string(src[from:to]) }
	if p.main != "" {
		renamed := renameIdent(src, toks, "main", p.main)
		text = func(from, to int) string { return renamed(from, to) }
	}
	line := func(off int) int { return file.Line(file.Pos(off)) }

	// Short declarations of new names become var declarations.
	declared := map[string]bool{}
	for i := range stmts {
		st := &stmts[i]
		if st.kind != kindStmt {
			continue
		}
		names, define, ok := lhsNames(toks, st.first, token.DEFINE)
		if !ok {
			continue
		}
		p.addLHS(names)
		var newNames, oldNames []string
		for _, n := range names {
			switch {
			case n == "_":
			case existing[n] || declared[n]:
				oldNames = append(oldNames, n)
			default:
				newNames = append(newNames, n)
			}
		}
		if len(newNames) > 0 && len(oldNames) > 0 {
			return nil, &refusal{fmt.Sprintf("this := declares %s and assigns %s, which the VM cannot keep apart: declare %s with var first, then assign them all with =",
				strings.Join(newNames, ", "), strings.Join(oldNames, ", "), strings.Join(newNames, ", "))}
		}
		if len(newNames) == 0 {
			continue
		}
		for _, n := range names {
			declared[n] = true
		}
		st.kind = kindDecl
		st.rewrite = "var " + text(st.from, toks[define].off) + "=" + text(toks[define].off+len(":="), st.to)
	}
	// So do assignments to names declared nowhere: not in the VM, not by
	// a := above, not by a declaration anywhere in the input.
	var inputDecls map[string]bool
	for i := range stmts {
		st := &stmts[i]
		if st.kind != kindStmt {
			continue
		}
		names, assign, ok := lhsNames(toks, st.first, token.ASSIGN)
		if !ok {
			continue
		}
		p.addLHS(names)
		if inputDecls == nil {
			inputDecls = map[string]bool{}
			for _, d := range stmts {
				if d.kind == kindDecl && d.rewrite == "" {
					for _, n := range declaredNames(text(d.from, d.to)) {
						inputDecls[n] = true
					}
				}
			}
		}
		fresh, old := 0, false
		for _, n := range names {
			switch {
			case n == "_":
			case existing[n] || declared[n] || inputDecls[n] || elsewhere[n]:
				old = true
			default:
				fresh++
			}
		}
		if old || fresh == 0 || usesAny(toks, assign+1, st.to, names) {
			continue
		}
		for _, n := range names {
			if n != "_" {
				declared[n] = true
				p.assigned = append(p.assigned, n)
			}
		}
		st.kind = kindDecl
		st.rewrite = "var " + text(st.from, toks[assign].off) + "=" + text(toks[assign].off+len("="), st.to)
	}
	// stmtText is a statement's text, as rewritten if it was.
	stmtText := func(st topStmt) string {
		if st.rewrite != "" {
			return st.rewrite
		}
		return text(st.from, st.to)
	}

	for i := 0; i < len(stmts); {
		st := stmts[i]
		switch st.kind {
		case kindImport:
			specs, err := parseImports(string(src[st.from:st.to]))
			if err != nil {
				return nil, err
			}
			p.imports = append(p.imports, specs...)
			i++
		case kindDecl, kindStmt:
			// Take the run of statements of the same kind, keeping
			// what lies between them, so lines stay where they were.
			var body strings.Builder
			j := i
			for ; j < len(stmts) && stmts[j].kind == st.kind; j++ {
				if j > i {
					body.Write(src[stmts[j-1].to:stmts[j].from])
				}
				body.WriteString(stmtText(stmts[j]))
			}
			o := op{Line: line(st.from)}
			if st.kind == kindDecl {
				o.File = body.String()
			} else {
				o.Stmts = body.String()
			}
			p.ops = append(p.ops, o)
			i = j
		}
	}
	if p.main != "" {
		p.ops = append(p.ops, op{Call: p.main})
	}

	// A trailing lone expression is evaluated, its value shown.
	if n := len(p.ops); n > 0 && p.ops[n-1].Stmts != "" {
		last := &p.ops[n-1]
		if before, expr, ok := splitTrailingExpr(last.Stmts); ok {
			if strings.TrimSpace(before) == "" {
				p.ops = p.ops[:n-1]
			} else {
				last.Stmts = before
			}
			p.ops = append(p.ops, op{Eval: expr})
		}
	}
	p.readOnly = make([]bool, len(p.ops))
	for i, o := range p.ops {
		switch {
		case o.Eval != "":
			p.readOnly[i] = p.readOnlyEval(o.Eval)
		case o.Stmts != "":
			p.readOnly[i] = p.printsOnly(o.Stmts)
		}
	}
	if len(p.ops) == 0 && len(p.imports) == 0 {
		return nil, &refusal{"there is no code to run"}
	}
	return p, nil
}

type tok struct {
	off int
	t   token.Token
	lit string
}

type stmtKind int

const (
	kindStmt stmtKind = iota
	kindDecl
	kindImport
)

// topStmt is one top-level statement or declaration: source bytes [from, to).
type topStmt struct {
	kind     stmtKind
	from, to int
	first    int // index of its first token
	// rewrite replaces the statement's text, if set.
	rewrite string
}

// lhsNames recognises a top-level a, b := ... (op token.DEFINE) or a, b = ...
// (op token.ASSIGN) starting at token i, and returns its names and the index
// of its operator token.
func lhsNames(toks []tok, i int, op token.Token) (names []string, at int, ok bool) {
	for j := i; j < len(toks); j++ {
		switch {
		case (j-i)%2 == 0 && toks[j].t == token.IDENT:
			names = append(names, toks[j].lit)
		case (j-i)%2 == 1 && toks[j].t == token.COMMA:
		case (j-i)%2 == 1 && toks[j].t == op:
			return names, j, len(names) > 0
		default:
			return nil, 0, false
		}
	}
	return nil, 0, false
}

// usesAny reports whether the tokens from index i up to byte offset end use
// one of names (a field or method of that name, after a ".", is not a use).
func usesAny(toks []tok, i, end int, names []string) bool {
	for j := i; j < len(toks) && toks[j].off < end; j++ {
		if toks[j].t != token.IDENT || toks[j-1].t == token.PERIOD {
			continue
		}
		for _, n := range names {
			if toks[j].lit == n {
				return true
			}
		}
	}
	return false
}

// declaredNames lists the names top-level declarations declare (methods
// declare none), or none if they do not parse.
func declaredNames(decls string) []string {
	f, err := parser.ParseFile(token.NewFileSet(), "", "package p;"+decls, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				out = append(out, d.Name.Name)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch spec := spec.(type) {
				case *ast.ValueSpec:
					for _, n := range spec.Names {
						out = append(out, n.Name)
					}
				case *ast.TypeSpec:
					out = append(out, spec.Name.Name)
				}
			}
		}
	}
	return out
}

// isMain reports whether a declaration is func main().
func (st topStmt) isMain(toks []tok) bool {
	i := st.first
	return i+2 < len(toks) && toks[i].t == token.FUNC &&
		toks[i+1].t == token.IDENT && toks[i+1].lit == "main" && toks[i+2].t == token.LPAREN
}

// splitTop splits tokens into top-level statements at semicolons outside
// any brackets, and classifies each.
func splitTop(toks []tok, srcLen int) []topStmt {
	var out []topStmt
	depth := 0
	start := -1
	for i, t := range toks {
		if start < 0 {
			if t.t == token.SEMICOLON {
				continue
			}
			start = i
		}
		switch t.t {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		}
		if depth == 0 && (t.t == token.SEMICOLON || i == len(toks)-1) {
			end := t.off
			if t.t != token.SEMICOLON {
				end = srcLen
			}
			out = append(out, topStmt{kind: classify(toks, start), from: toks[start].off, to: end, first: start})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, topStmt{kind: classify(toks, start), from: toks[start].off, to: srcLen, first: start})
	}
	return out
}

// classify tells a declaration from a statement by its first tokens. A
// "func (" starts a method declaration only if a name follows its receiver;
// otherwise it is a statement, like func() { ... }().
func classify(toks []tok, i int) stmtKind {
	switch toks[i].t {
	case token.IMPORT:
		return kindImport
	case token.TYPE, token.VAR, token.CONST:
		return kindDecl
	case token.FUNC:
		if i+1 >= len(toks) {
			return kindStmt
		}
		switch toks[i+1].t {
		case token.IDENT:
			return kindDecl
		case token.LPAREN:
			depth := 0
			for j := i + 1; j < len(toks); j++ {
				switch toks[j].t {
				case token.LPAREN:
					depth++
				case token.RPAREN:
					depth--
				}
				if depth == 0 {
					if j+2 < len(toks) && toks[j+1].t == token.IDENT &&
						(toks[j+2].t == token.LPAREN || toks[j+2].t == token.LBRACK) {
						return kindDecl
					}
					return kindStmt
				}
			}
		}
	}
	return kindStmt
}

// renameIdent returns a function that extracts source text with every
// identifier old renamed to new, except after a "." (a field or method of
// that name keeps it).
func renameIdent(src []byte, toks []tok, old, new string) func(from, to int) string {
	var at []int
	for i, t := range toks {
		if t.t == token.IDENT && t.lit == old && (i == 0 || toks[i-1].t != token.PERIOD) {
			at = append(at, t.off)
		}
	}
	return func(from, to int) string {
		var b strings.Builder
		prev := from
		for _, off := range at {
			if off < from || off >= to {
				continue
			}
			b.Write(src[prev:off])
			b.WriteString(new)
			prev = off + len(old)
		}
		b.Write(src[prev:to])
		return b.String()
	}
}

// parseImports parses one import declaration.
func parseImports(decl string) ([]importSpec, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "", "package p;"+decl, parser.ImportsOnly)
	if err != nil {
		return nil, &parseError{err}
	}
	var out []importSpec
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return nil, &parseError{err}
		}
		s := importSpec{Path: path}
		if spec.Name != nil {
			s.Name = spec.Name.Name
			if s.Name == "." {
				return nil, &refusal{fmt.Sprintf("dot imports are not supported; import %q and use its name", path)}
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// splitTrailingExpr splits statements whose last one is a lone expression
// into the statements before it and the expression.
func splitTrailingExpr(stmts string) (before, expr string, ok bool) {
	body, base := parseStmts(stmts)
	if len(body) == 0 {
		return "", "", false
	}
	es, isExpr := body[len(body)-1].(*ast.ExprStmt)
	if !isExpr || printCall(es.X) {
		return "", "", false
	}
	from, to := int(es.Pos())-base, int(es.End())-base
	return stmts[:from], stmts[from:to], true
}

// parseStmts parses statements as a function body, and returns them with
// the offset to subtract from a position to index stmts.
func parseStmts(stmts string) ([]ast.Stmt, int) {
	const prefix = "package p;func _(){"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", prefix+stmts+"\n}", parser.SkipObjectResolution)
	if err != nil || len(f.Decls) != 1 {
		return nil, 0
	}
	return f.Decls[0].(*ast.FuncDecl).Body.List, fset.File(f.Pos()).Base() + len(prefix)
}

func (p *program) addLHS(names []string) {
	if p.lhs == nil {
		p.lhs = map[string]bool{}
	}
	for _, n := range names {
		p.lhs[n] = true
	}
}

// readOnlyEval reports whether an eval only reads: it is pure.
func (p *program) readOnlyEval(expr string) bool {
	x, err := parser.ParseExpr(expr)
	return err == nil && pureExpr(x)
}

// printsOnly reports whether statements only print literals: each is a
// call of print, println or fmt.Print* whose operands are literals. (A
// value printed may have a String method that changes state.)
func (p *program) printsOnly(stmts string) bool {
	body, _ := parseStmts(stmts)
	if len(body) == 0 {
		return false
	}
	for _, st := range body {
		es, ok := st.(*ast.ExprStmt)
		if !ok || !printCall(es.X) {
			return false
		}
		if _, isSel := es.X.(*ast.CallExpr).Fun.(*ast.SelectorExpr); isSel {
			p.usesFmt = true
		}
		for _, a := range es.X.(*ast.CallExpr).Args {
			if _, lit := a.(*ast.BasicLit); !lit {
				return false
			}
		}
	}
	return true
}

// printCall reports whether x calls print, println or fmt.Print*.
func printCall(x ast.Expr) bool {
	call, ok := x.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name == "print" || fn.Name == "println"
	case *ast.SelectorExpr:
		pkg, ok := fn.X.(*ast.Ident)
		return ok && pkg.Name == "fmt" && strings.HasPrefix(fn.Sel.Name, "Print")
	}
	return false
}

// autoImports returns imports for the packages an input uses without
// importing them: a name used as name.X that nothing declares (neither the
// input, nor the VM, whose names are have) and that importByName knows.
// Models leave out imports often (fmt above all), and each time it cost a
// failed call and a round trip.
func autoImports(p *program, have map[string]bool) []importSpec {
	// One Go file holds the input's declarations, then its statements and
	// evaluation in a function body, so the parser resolves every name the
	// input itself declares, at any depth.
	var b strings.Builder
	b.WriteString("package p\n")
	for _, o := range p.ops {
		if o.File != "" {
			b.WriteString(o.File + "\n")
		}
	}
	b.WriteString("func _() {\n")
	for _, o := range p.ops {
		switch {
		case o.Stmts != "":
			b.WriteString(o.Stmts + "\n")
		case o.Eval != "":
			b.WriteString("_ = " + o.Eval + "\n")
		}
	}
	b.WriteString("}\n")
	f, err := parser.ParseFile(token.NewFileSet(), "", b.String(), 0)
	if err != nil {
		return nil // the VM reports the error
	}
	bound := map[string]bool{}
	for _, spec := range p.imports {
		name := spec.Name
		if name == "" {
			name = cmp.Or(libraryPackageName(spec.Path), path.Base(spec.Path))
		}
		bound[name] = true
	}
	unresolved := map[*ast.Ident]bool{}
	for _, id := range f.Unresolved {
		unresolved[id] = true
	}
	var out []importSpec
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || !unresolved[id] || have[id.Name] || bound[id.Name] {
			return true
		}
		if pkgPath, ok := importByName(id.Name); ok {
			out = append(out, importSpec{Path: pkgPath})
		}
		bound[id.Name] = true
		return true
	})
	return out
}

// pureBuiltins are the builtins and builtin-type conversions that cannot
// change state.
var pureBuiltins = map[string]bool{
	"len": true, "cap": true, "min": true, "max": true, "real": true, "imag": true, "complex": true,
	"new": true, "make": true, "string": true, "bool": true, "byte": true, "rune": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"float32": true, "float64": true, "complex64": true, "complex128": true,
}

func pureExpr(x ast.Expr) bool {
	pure := true
	ast.Inspect(x, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); !ok || !pureBuiltins[id.Name] {
				pure = false
			}
		case *ast.FuncLit:
			pure = false
		case *ast.UnaryExpr:
			if n.Op == token.ARROW {
				pure = false
			}
		}
		return pure
	})
	return pure
}
