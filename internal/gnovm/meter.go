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
	"context"
	"errors"
	"math"
	"time"

	storetypes "github.com/gnolang/gno/tm2/pkg/store/types"
)

// pollEvery is how much gas runs between checks of the wall-clock limit and
// of cancellation: about a millisecond of work at 1 ns per gas.
const pollEvery = 1_000_000

var (
	errWallClock = errors.New("wall-clock limit reached")
	errCancelled = errors.New("cancelled")
	errOutOfGas  = errors.New("out of gas")
)

// stopRun is the panic value callMeter uses to stop a run that hit its
// wall-clock limit or was cancelled; the run recovers it.
type stopRun struct{ err error }

// callMeter is the gas meter of one resident VM. NewMachineWithOptions wires
// one meter into both the machine (CPU gas) and its allocator (allocation
// gas) for the VM's whole life, so per-call limits are enforced here, against
// the gas consumed since begin, rather than by swapping meters between calls.
//
// It follows tm2's basicGasMeter: negative amounts and overflow panic, gas is
// recorded before an out-of-gas panic, and a refund is capped at what was
// consumed (here: since begin, so a refund never reaches an earlier call).
//
// A trip (out of gas, the wall clock, cancellation) is sticky until the next
// begin: every later ConsumeGas panics again with the same value, and tripped
// reports it. The GnoVM recovers some panics itself (tryEvalStatic while
// preprocessing, boundedUserSprint while rendering), and a trip it swallowed
// must still fail the call.
type callMeter struct {
	consumed  storetypes.Gas // over the VM's life
	callStart storetypes.Gas
	limit     storetypes.Gas
	nextPoll  storetypes.Gas
	deadline  time.Time
	done      <-chan struct{}
	trip      any // the panic value of the call's first trip, or nil
}

var _ storetypes.GasMeter = (*callMeter)(nil)

// begin starts a call with the given gas limit, wall-clock deadline (zero for
// none) and context (its cancellation stops the call).
func (g *callMeter) begin(ctx context.Context, limit storetypes.Gas, deadline time.Time) {
	g.callStart = g.consumed
	g.limit = limit
	g.nextPoll = g.consumed + pollEvery
	g.deadline = deadline
	g.done = nil
	g.trip = nil
	if ctx != nil {
		g.done = ctx.Done()
	}
}

// end drops the call's context so a stale one is never polled.
func (g *callMeter) end() { g.done, g.deadline = nil, time.Time{} }

// tripped returns the call's trip as an error, or nil if it had none.
func (g *callMeter) tripped() error {
	switch t := g.trip.(type) {
	case nil:
		return nil
	case stopRun:
		return t.err
	default:
		return errOutOfGas
	}
}

// used is the gas consumed since begin.
func (g *callMeter) used() storetypes.Gas { return g.consumed - g.callStart }

func (g *callMeter) GasConsumed() storetypes.Gas { return g.used() }
func (g *callMeter) Limit() storetypes.Gas       { return g.limit }
func (g *callMeter) IsPastLimit() bool           { return g.used() > g.limit }
func (g *callMeter) IsOutOfGas() bool            { return g.used() >= g.limit }

func (g *callMeter) GasConsumedToLimit() storetypes.Gas {
	return min(g.used(), g.limit)
}

func (g *callMeter) Remaining() storetypes.Gas {
	return max(g.limit-g.used(), 0)
}

func (g *callMeter) ConsumeGas(amount storetypes.Gas, descriptor string) {
	if g.trip != nil {
		panic(g.trip)
	}
	if amount < 0 {
		panic("gas must not be negative")
	}
	if g.consumed > math.MaxInt64-amount {
		panic(storetypes.GasOverflowError{Descriptor: descriptor})
	}
	g.consumed += amount
	if g.used() > g.limit {
		g.trip = storetypes.OutOfGasError{Descriptor: descriptor}
		panic(g.trip)
	}
	if g.consumed >= g.nextPoll {
		g.nextPoll = g.consumed + pollEvery
		g.poll()
	}
}

func (g *callMeter) RefundGas(amount storetypes.Gas, _ string) {
	if amount < 0 {
		panic("gas must not be negative")
	}
	g.consumed -= min(amount, g.used())
}

// poll stops the run if its context is done or its wall-clock deadline has
// passed.
func (g *callMeter) poll() {
	if g.done != nil {
		select {
		case <-g.done:
			g.trip = stopRun{errCancelled}
			panic(g.trip)
		default:
		}
	}
	if !g.deadline.IsZero() && time.Now().After(g.deadline) {
		g.trip = stopRun{errWallClock}
		panic(g.trip)
	}
}
