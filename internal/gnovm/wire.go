// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

import "errors"

// toWire and fromWire translate host errors across the worker protocol.
// The frame types and framing live in package api (see PROTOCOL.md).

func toWire(err error) *wireError {
	if err == nil {
		return nil
	}
	var (
		ref    *refusal
		busy   *busyError
		perr   *parseError
		lim    *limitError
		cerr   *compileError
		pan    *panicked
		rerr   *runError
		broken *brokenError
	)
	switch {
	case errors.As(err, &rerr):
		return &wireError{Kind: "run", Msg: rerr.Error(), Stack: rerr.stack, Inner: toWire(rerr.err)}
	case errors.As(err, &ref):
		return &wireError{Kind: "refusal", Msg: ref.msg}
	case errors.As(err, &busy):
		return &wireError{Kind: "busy", Msg: busy.msg, Again: busy.again}
	case errors.As(err, &perr):
		return &wireError{Kind: "parse", Msg: perr.err.Error()}
	case errors.As(err, &lim):
		return &wireError{Kind: "limit", Limit: lim.kind, Msg: lim.Error()}
	case errors.As(err, &cerr):
		return &wireError{Kind: "compile", Msg: cerr.msg}
	case errors.As(err, &pan):
		return &wireError{Kind: "panic", Msg: pan.msg}
	case errors.As(err, &broken):
		return &wireError{Kind: "broken", Msg: broken.Error()}
	}
	return &wireError{Kind: "other", Msg: err.Error()}
}

func fromWire(w *wireError) error {
	if w == nil {
		return nil
	}
	switch w.Kind {
	case "run":
		return &runError{err: fromWire(w.Inner), stack: w.Stack}
	case "refusal":
		return &refusal{w.Msg}
	case "busy":
		return &busyError{msg: w.Msg, again: w.Again}
	case "parse":
		return &parseError{errors.New(w.Msg)}
	case "limit":
		return &limitError{kind: w.Limit}
	case "compile":
		return &compileError{w.Msg}
	case "panic":
		return &panicked{w.Msg}
	case "broken":
		return &brokenError{msg: w.Msg}
	}
	return errors.New(w.Msg)
}
