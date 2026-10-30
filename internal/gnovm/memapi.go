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
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
)

// memMethod is one of mem's calls, or of a Goal's, as the model writes it:
// its name and its parameters, "(name, result...)".
type memMethod struct {
	name, params string
}

// memCalls is what the model may call on mem and on a Goal, in the order
// gnomem declares them. It is read from gnomem's own source, so that what a
// call that does not fit is told never disagrees with the library.
type memCalls struct {
	mem, goal []memMethod
}

var memAPI = readMemAPI()

// memNotCalls are exported for gnosh, not for the model: Bind is /goal's,
// Apply the journal's, Check the tests', and String prints.
var memNotCalls = map[string]bool{"Bind": true, "Apply": true, "Check": true, "String": true}

func readMemAPI() memCalls {
	var api memCalls
	files, err := fs.Glob(klashlibFS, "klashlibs/klash/gnomem/v2/*.gno")
	if err != nil {
		panic(err)
	}
	fset := token.NewFileSet()
	for _, path := range files {
		src, err := klashlibFS.ReadFile(path)
		if err != nil {
			panic(err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			panic(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() || memNotCalls[fn.Name.Name] {
				continue
			}
			call := memMethod{fn.Name.Name, memParams(fn.Type.Params)}
			switch t := fn.Recv.List[0].Type.(type) {
			case *ast.StarExpr:
				if id, ok := t.X.(*ast.Ident); ok && id.Name == "Mem" {
					api.mem = append(api.mem, call)
				}
			case *ast.Ident:
				if t.Name == "Goal" {
					api.goal = append(api.goal, call)
				}
			}
		}
	}
	return api
}

// memParams is a parameter list as the model writes the call: names only,
// with "..." after one that takes any number.
func memParams(list *ast.FieldList) string {
	var names []string
	for _, field := range list.List {
		_, many := field.Type.(*ast.Ellipsis)
		for _, n := range field.Names {
			if many {
				names = append(names, n.Name+"...")
			} else {
				names = append(names, n.Name)
			}
		}
	}
	return "(" + strings.Join(names, ", ") + ")"
}

// find is the call named name, of mem's or of a Goal's.
func (calls memCalls) find(onGoal bool, name string) (memMethod, bool) {
	list := calls.mem
	if onGoal {
		list = calls.goal
	}
	for _, c := range list {
		if c.name == name {
			return c, true
		}
	}
	return memMethod{}, false
}

// names lists the calls of mem's, or of a Goal's, as a sentence does: "a,
// b and c".
func (calls memCalls) names(onGoal bool) string {
	list := calls.mem
	if onGoal {
		list = calls.goal
	}
	var names []string
	for _, c := range list {
		names = append(names, c.name)
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// memArgCount is what gno's "wrong argument count in call to <on>.<name>;
// want <want> got <got>" says in mem's words, from what the call takes; ""
// when name is no call of mem's or of a Goal's, and gno's words stand.
func memArgCount(on, name, want, got string) string {
	args := want + " arguments"
	if want == "1" {
		args = "1 argument"
	}
	if on == "mem" {
		c, ok := memAPI.find(false, name)
		if !ok {
			return ""
		}
		msg := "mem." + name + " takes " + args + ", not " + got
		if (name == "Fact" || name == "Rule") && got == "1" {
			return msg + ": mem." + name + "(name, text) sets a " + strings.ToLower(name) + ", and mem.Show(name) shows one"
		}
		return msg + ": mem." + name + c.params
	}
	// Anything else called on is a Goal: mem's only other type.
	c, ok := memAPI.find(true, name)
	if !ok {
		return ""
	}
	msg := "a Goal's " + name + " takes " + args + ", not " + got + ": mem.Goal(…)." + name + c.params
	if mc, ok := memAPI.find(false, name); ok {
		msg += ", or mem." + name + mc.params + " for an item by its name"
	}
	return msg
}

// memMissing is what to tell the model of gno's "missing field <name> in
// <Mem or Goal>": a call mem's older versions had, where a Goal's call is
// mem's, or what there is to call.
func memMissing(name string, onGoal bool) string {
	if !onGoal {
		if hint, ok := memMoved[name]; ok {
			return hint
		}
		return "mem's calls are " + memAPI.names(false)
	}
	has := "a Goal, which mem.Goal(…) gives, has " + memAPI.names(true)
	if c, ok := memAPI.find(false, name); ok {
		return has + "; " + name + " is mem's: mem." + name + c.params
	}
	return has
}
