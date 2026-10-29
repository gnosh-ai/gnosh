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
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	gno "github.com/gnolang/gno/gnovm/pkg/gnolang"
	storetypes "github.com/gnolang/gno/tm2/pkg/store/types"
)

// refusal is an input the host would not run: nothing ran.
type refusal struct{ msg string }

func (e *refusal) Error() string { return e.msg }

// parseError is input that does not parse: nothing ran.
type parseError struct{ err error }

func (e *parseError) Error() string { return "syntax error: " + e.err.Error() }
func (e *parseError) Unwrap() error { return e.err }

// limitError is a run stopped by a limit: gas, memory, the wall clock, or
// cancellation.
type limitError struct {
	kind string // "gas", "memory", "wall", "cancelled"
}

func (e *limitError) Error() string {
	switch e.kind {
	case "gas":
		return "out of gas"
	case "memory":
		return "memory cap exceeded"
	case "wall":
		return errWallClock.Error()
	default:
		return errCancelled.Error()
	}
}

// compileError is code the GnoVM would not compile (preprocess).
type compileError struct{ msg string }

func (e *compileError) Error() string { return e.msg }

// panicked is an unhandled panic in the VM's code.
type panicked struct{ msg string }

func (e *panicked) Error() string { return "panic: " + e.msg }

// runError adds the GnoVM's stack to an error from running code.
type runError struct {
	err   error
	stack string
}

func (e *runError) Error() string { return e.err.Error() }
func (e *runError) Unwrap() error { return e.err }

// brokenError is a VM whose journal cannot be read or replayed: it runs
// nothing until it is repaired or deleted.
type brokenError struct {
	vm    string
	cause error
	msg   string // when it came over the wire
}

func (e *brokenError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("VM %s is broken: %v. Repair it with /goals repair %s (it keeps the entries before the damage), or delete it with gnovm_drop",
		e.vm, e.cause, e.vm)
}

func (e *brokenError) Unwrap() error { return e.cause }

// ErrorStack returns the GnoVM stack an error from running code carries, or
// "".
func ErrorStack(err error) string {
	var rerr *runError
	if errors.As(err, &rerr) {
		return rerr.stack
	}
	return ""
}

// LimitKind names the limit that stopped a run ("gas", "memory", "wall",
// "cancelled"), or "" if it was not one.
func LimitKind(err error) string {
	var lim *limitError
	if errors.As(err, &lim) {
		return lim.kind
	}
	return ""
}

// classifyTrip turns a meter trip into a limitError.
func classifyTrip(err error) error {
	switch {
	case errors.Is(err, errOutOfGas):
		return &limitError{kind: "gas"}
	case errors.Is(err, errWallClock):
		return &limitError{kind: "wall"}
	case errors.Is(err, errCancelled):
		return &limitError{kind: "cancelled"}
	}
	return err
}

// panicError turns what a run panicked with into an error. A meter trip
// wins: the GnoVM sometimes wraps or replaces the panic that carried it.
func panicError(r any, meter *callMeter) error {
	if err := meter.tripped(); err != nil {
		return classifyTrip(err)
	}
	switch r := r.(type) {
	case stopRun:
		return classifyTrip(r.err)
	case storetypes.OutOfGasError:
		return &limitError{kind: "gas"}
	case gno.UnhandledPanicError:
		return &panicked{msg: truncate(r.Descriptor, maxValue)}
	case *gno.PreprocessError:
		msg := r.Unwrap().Error()
		if isAllocLimit(msg) {
			return &limitError{kind: "memory"}
		}
		return &compileError{msg: truncate(msg, maxValue)}
	case string:
		if isAllocLimit(r) {
			return &limitError{kind: "memory"}
		}
		return errors.New(truncate(r, maxValue))
	case error:
		if isAllocLimit(r.Error()) {
			return &limitError{kind: "memory"}
		}
		return fmt.Errorf("%s", truncate(r.Error(), maxValue))
	}
	return fmt.Errorf("%s", truncate(fmt.Sprint(r), maxValue))
}

func isAllocLimit(msg string) bool { return strings.Contains(msg, "allocation limit exceeded") }

// truncate cuts s to at most n bytes, on a rune boundary, saying so.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("… (%d bytes cut)", len(s)-cut)
}
