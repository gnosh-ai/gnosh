// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

// Package goals is the harness's record of long-standing goals: what each
// goal is, the facts recorded toward it, and the check that says when it is
// met. This file is the check language.
package goals

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
)

// MaxCheck is the longest check, in bytes.
const MaxCheck = 512

// KeyRe is what a fact key (and a goal id) looks like. The "klash." prefix
// is the harness's own.
var KeyRe = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)

// A check is a Go expression over a goal's facts, in a small language that
// is parsed and evaluated here, in Go: no text a model writes ever runs as
// code. It may use
//
//	facts["key"]       the fact's value, a string (a missing fact is an error)
//	num(facts["key"])  the value as a number (an error if it is not one)
//	len(facts["key"])  the value's length in bytes
//	has("key")         whether the fact is recorded (the only way to test absence)
//	"text", 12, 1.5, true, false
//	-x  !x  ==  !=  <  <=  >  >=  &&  ||  ( )
//
// A check must refer to at least one fact, and is refused when it is added if
// it already holds: a goal cannot be met by its own wording.
type Check struct {
	src  string
	expr ast.Expr
}

type kind int

const (
	kindBool kind = iota + 1
	kindString
	kindNumber
)

func (k kind) String() string {
	switch k {
	case kindBool:
		return "a bool"
	case kindString:
		return "a string"
	case kindNumber:
		return "a number"
	}
	return "nothing"
}

// ParseCheck parses and type-checks a check.
func ParseCheck(src string) (*Check, error) {
	src = strings.TrimSpace(src)
	if src == "" {
		return nil, errors.New("the check is empty")
	}
	if len(src) > MaxCheck {
		return nil, fmt.Errorf("the check is %d bytes; at most %d", len(src), MaxCheck)
	}
	x, err := parser.ParseExpr(src)
	if err != nil {
		return nil, fmt.Errorf("the check does not parse: %v", err)
	}
	c := &Check{src: src, expr: x}
	k, facts, err := c.typeOf(x)
	if err != nil {
		return nil, err
	}
	if k != kindBool {
		return nil, fmt.Errorf("the check is %s, not a condition: compare it, as in num(facts[\"passed\"]) >= 10", k)
	}
	if facts == 0 {
		return nil, errors.New(`the check refers to no fact: use facts["key"] or has("key"), so that recording facts can make it true`)
	}
	return c, nil
}

// String is the check as written.
func (c *Check) String() string { return c.src }

// Keys lists the fact keys a check refers to.
func (c *Check) Keys() []string {
	var keys []string
	seen := map[string]bool{}
	ast.Inspect(c.expr, func(n ast.Node) bool {
		if k, ok := factKey(n); ok && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
		return true
	})
	return keys
}

// factKey recognises facts["key"] and has("key"), and returns the key.
func factKey(n ast.Node) (string, bool) {
	switch n := n.(type) {
	case *ast.IndexExpr:
		if id, ok := n.X.(*ast.Ident); ok && id.Name == "facts" {
			if lit, ok := n.Index.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if k, err := strconv.Unquote(lit.Value); err == nil {
					return k, true
				}
			}
		}
	case *ast.CallExpr:
		if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "has" && len(n.Args) == 1 {
			if lit, ok := n.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if k, err := strconv.Unquote(lit.Value); err == nil {
					return k, true
				}
			}
		}
	}
	return "", false
}

// typeOf checks an expression against the language and returns its kind and
// how many fact references it holds.
func (c *Check) typeOf(x ast.Expr) (kind, int, error) {
	switch x := x.(type) {
	case *ast.ParenExpr:
		return c.typeOf(x.X)
	case *ast.BasicLit:
		switch x.Kind {
		case token.STRING:
			if _, err := strconv.Unquote(x.Value); err != nil {
				return 0, 0, fmt.Errorf("bad string %s", x.Value)
			}
			return kindString, 0, nil
		case token.INT, token.FLOAT:
			if _, err := strconv.ParseFloat(x.Value, 64); err != nil {
				return 0, 0, fmt.Errorf("bad number %s", x.Value)
			}
			return kindNumber, 0, nil
		}
	case *ast.Ident:
		if x.Name == "true" || x.Name == "false" {
			return kindBool, 0, nil
		}
		return 0, 0, fmt.Errorf("%s is not part of the check language: use facts[\"key\"], num, len, has, literals and comparisons", x.Name)
	case *ast.IndexExpr:
		if k, ok := factKey(x); ok {
			if !KeyRe.MatchString(k) {
				return 0, 0, fmt.Errorf("fact key %q: lowercase letters, digits, '.', '_' or '-', at most 64", k)
			}
			return kindString, 1, nil
		}
		return 0, 0, errors.New(`only facts["key"], with a quoted key, may be indexed`)
	case *ast.CallExpr:
		id, ok := x.Fun.(*ast.Ident)
		if !ok || len(x.Args) != 1 {
			return 0, 0, errors.New("only num(...), len(...) and has(\"key\") may be called, with one argument")
		}
		switch id.Name {
		case "has":
			k, ok := factKey(x)
			if !ok {
				return 0, 0, errors.New(`has takes a quoted key: has("key")`)
			}
			if !KeyRe.MatchString(k) {
				return 0, 0, fmt.Errorf("fact key %q: lowercase letters, digits, '.', '_' or '-', at most 64", k)
			}
			return kindBool, 1, nil
		case "num", "len":
			k, n, err := c.typeOf(x.Args[0])
			if err != nil {
				return 0, 0, err
			}
			if k != kindString {
				return 0, 0, fmt.Errorf("%s takes a string, not %s", id.Name, k)
			}
			return kindNumber, n, nil
		}
		return 0, 0, fmt.Errorf("%s is not part of the check language", id.Name)
	case *ast.UnaryExpr:
		k, n, err := c.typeOf(x.X)
		if err != nil {
			return 0, 0, err
		}
		switch {
		case x.Op == token.NOT && k == kindBool:
			return kindBool, n, nil
		case x.Op == token.SUB && k == kindNumber:
			return kindNumber, n, nil
		}
		return 0, 0, fmt.Errorf("%s cannot apply to %s", x.Op, k)
	case *ast.BinaryExpr:
		lk, ln, err := c.typeOf(x.X)
		if err != nil {
			return 0, 0, err
		}
		rk, rn, err := c.typeOf(x.Y)
		if err != nil {
			return 0, 0, err
		}
		switch x.Op {
		case token.LAND, token.LOR:
			if lk != kindBool || rk != kindBool {
				return 0, 0, fmt.Errorf("%s joins conditions, not %s and %s", x.Op, lk, rk)
			}
			return kindBool, ln + rn, nil
		case token.EQL, token.NEQ:
			if lk != rk {
				return 0, 0, fmt.Errorf("cannot compare %s with %s; use num(facts[\"key\"]) for a number", lk, rk)
			}
			return kindBool, ln + rn, nil
		case token.LSS, token.LEQ, token.GTR, token.GEQ:
			if lk != rk || lk == kindBool {
				return 0, 0, fmt.Errorf("cannot order %s and %s; use num(facts[\"key\"]) for a number", lk, rk)
			}
			return kindBool, ln + rn, nil
		}
		return 0, 0, fmt.Errorf("%s is not part of the check language", x.Op)
	}
	return 0, 0, fmt.Errorf("%T is not part of the check language", x)
}

// value is a check's value as it is evaluated.
type value struct {
	k kind
	b bool
	s string
	n float64
}

// Eval evaluates a check against a goal's facts. A missing fact, or a value
// num cannot read as a number, is an error: the check cannot tell yet.
func (c *Check) Eval(facts map[string]string) (bool, error) {
	v, err := eval(c.expr, facts)
	if err != nil {
		return false, err
	}
	return v.b, nil
}

func eval(x ast.Expr, facts map[string]string) (value, error) {
	switch x := x.(type) {
	case *ast.ParenExpr:
		return eval(x.X, facts)
	case *ast.BasicLit:
		if x.Kind == token.STRING {
			s, _ := strconv.Unquote(x.Value)
			return value{k: kindString, s: s}, nil
		}
		n, _ := strconv.ParseFloat(x.Value, 64)
		return value{k: kindNumber, n: n}, nil
	case *ast.Ident:
		return value{k: kindBool, b: x.Name == "true"}, nil
	case *ast.IndexExpr:
		k, _ := factKey(x)
		v, ok := facts[k]
		if !ok {
			return value{}, fmt.Errorf("fact %q is not recorded yet", k)
		}
		return value{k: kindString, s: v}, nil
	case *ast.CallExpr:
		name := x.Fun.(*ast.Ident).Name
		if name == "has" {
			k, _ := factKey(x)
			_, ok := facts[k]
			return value{k: kindBool, b: ok}, nil
		}
		arg, err := eval(x.Args[0], facts)
		if err != nil {
			return value{}, err
		}
		if name == "len" {
			return value{k: kindNumber, n: float64(len(arg.s))}, nil
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(arg.s), 64)
		if err != nil {
			return value{}, fmt.Errorf("num: %q is not a number", arg.s)
		}
		return value{k: kindNumber, n: n}, nil
	case *ast.UnaryExpr:
		v, err := eval(x.X, facts)
		if err != nil {
			return value{}, err
		}
		if x.Op == token.NOT {
			return value{k: kindBool, b: !v.b}, nil
		}
		return value{k: kindNumber, n: -v.n}, nil
	case *ast.BinaryExpr:
		l, err := eval(x.X, facts)
		if err != nil {
			return value{}, err
		}
		// && and || short-circuit, as in Go: has("x") && facts["x"] == "y"
		// is false, not an error, when x is not recorded.
		switch x.Op {
		case token.LAND:
			if !l.b {
				return value{k: kindBool}, nil
			}
		case token.LOR:
			if l.b {
				return value{k: kindBool, b: true}, nil
			}
		}
		r, err := eval(x.Y, facts)
		if err != nil {
			return value{}, err
		}
		return compare(x.Op, l, r), nil
	}
	return value{}, fmt.Errorf("%T is not part of the check language", x)
}

func compare(op token.Token, l, r value) value {
	var res bool
	switch op {
	case token.LAND, token.LOR:
		res = r.b
	case token.EQL:
		res = l == r
	case token.NEQ:
		res = l != r
	default:
		c := 0
		switch l.k {
		case kindNumber:
			c = cmpFloat(l.n, r.n)
		case kindString:
			c = strings.Compare(l.s, r.s)
		}
		switch op {
		case token.LSS:
			res = c < 0
		case token.LEQ:
			res = c <= 0
		case token.GTR:
			res = c > 0
		case token.GEQ:
			res = c >= 0
		}
	}
	return value{k: kindBool, b: res}
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
