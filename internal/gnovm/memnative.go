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
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"

	gno "github.com/gnolang/gno/gnovm/pkg/gnolang"
	"github.com/gnolang/gno/gnovm/stdlibs"
)

// memContext is the context a mem call runs in: gno's own, which its natives
// read through the interface ExecContext answers, and what klash's mem
// natives read.
type memContext struct {
	stdlibs.ExecContext
	caller string
	ops    *[]memOp
	// pending is the Ops klash hands Apply, and pendingAt each one's call's
	// time.
	pending   []memOp
	pendingAt []int64
	// today is the user's local date, 2026-10-29.
	today string
	// find and holding answer from mem's index and journal; nil finds
	// nothing. holding is also handed the Ops this call has made so far.
	find    func(words string, max int) (names, kinds []string)
	holding func(text string, ops []memOp) (names, kinds []string, statements bool)
}

// memOp is one change to a mem, as gnomem's do hands it to klash and the
// journal keeps it (gnomem's Op).
type memOp struct {
	Kind  string `json:"kind"`
	Name  string `json:"name,omitempty"`
	Field string `json:"field,omitempty"`
	Old   string `json:"old,omitempty"`
	New   string `json:"new,omitempty"`
	By    string `json:"by,omitempty"`
}

// memNativeGas is what a mem native costs: a little bookkeeping in Go.
var memNativeGas = gno.NativeGasInfo{Base: 1000, SlopeIdx: -1, SlopeKind: gno.SizeFlat}

func init() {
	for _, name := range []gno.Name{"caller", "today", "record", "pending", "find", "holding", "quote", "jsonString"} {
		info := memNativeGas
		gno.RegisterNativeGas(memLibPath, name, &info)
	}
}

// memNative resolves gnomem's natives. Each answers only in a mem call,
// inside mem's realm: a Mem the model's code made or copied for itself lives
// outside the realm, and its Ops must never reach the journal. The two that
// only format, quote and jsonString, answer anywhere.
func memNative(name gno.Name) func(*gno.Machine) {
	switch name {
	case "caller":
		return func(m *gno.Machine) {
			memPush(m, memCallContext(m, "caller").caller)
		}
	case "today":
		return func(m *gno.Machine) {
			ctx := memCallContext(m, "today")
			d := ctx.today
			if d == "" {
				d = time.Now().Format(time.DateOnly)
			}
			memPush(m, d)
		}
	case "find":
		return func(m *gno.Machine) {
			ctx := memCallContext(m, "find")
			var names, kinds []string
			if ctx.find != nil {
				max := m.LastBlock().GetPointerTo(nil, gno.NewValuePathBlock(1, 1, "")).TV.GetInt()
				names, kinds = ctx.find(memArg(m, 0), int(max))
			}
			memPush(m, names)
			memPush(m, kinds)
		}
	case "holding":
		return func(m *gno.Machine) {
			ctx := memCallContext(m, "holding")
			var names, kinds []string
			statements := false
			if ctx.holding != nil {
				names, kinds, statements = ctx.holding(memArg(m, 0), *ctx.ops)
			}
			memPush(m, names)
			memPush(m, kinds)
			memPush(m, statements)
		}
	case "record":
		return func(m *gno.Machine) {
			ctx := memCallContext(m, "record")
			*ctx.ops = append(*ctx.ops, memOp{
				Kind:  memArg(m, 0),
				Name:  memArg(m, 1),
				Field: memArg(m, 2),
				Old:   memArg(m, 3),
				New:   memArg(m, 4),
				By:    memArg(m, 5),
			})
		}
	case "pending":
		return func(m *gno.Machine) {
			ctx := memCallContext(m, "pending")
			fields := make([][]string, 6)
			for _, op := range ctx.pending {
				for i, s := range []string{op.Kind, op.Name, op.Field, op.Old, op.New, op.By} {
					fields[i] = append(fields[i], s)
				}
			}
			for i := range fields {
				memPush(m, fields[i])
			}
			memPush(m, ctx.pendingAt)
		}
	// The two that only format answer anywhere.
	case "quote":
		return func(m *gno.Machine) {
			memPush(m, strconv.Quote(memArg(m, 0)))
		}
	case "jsonString":
		return func(m *gno.Machine) {
			var b bytes.Buffer
			enc := json.NewEncoder(&b)
			enc.SetEscapeHTML(false)
			_ = enc.Encode(memArg(m, 0))
			memPush(m, strings.TrimSuffix(b.String(), "\n"))
		}
	}
	return nil
}

// memPush returns v from a native.
func memPush[T any](m *gno.Machine, v T) {
	m.PushValue(gno.Go2GnoValue(m.Alloc, m.Store, reflect.ValueOf(&v).Elem()))
}

func memCallContext(m *gno.Machine, native string) memContext {
	ctx, ok := m.Context.(memContext)
	if !ok || ctx.ops == nil || m.Realm == nil || m.Realm.Path != memRealmPath {
		panic(native + ": only mem's own Mem, in a mem call, may change mem")
	}
	return ctx
}

// memArg is the native's i'th parameter, a string.
func memArg(m *gno.Machine, i int) string {
	return m.LastBlock().GetPointerTo(nil, gno.NewValuePathBlock(1, uint16(i), "")).TV.GetString()
}
