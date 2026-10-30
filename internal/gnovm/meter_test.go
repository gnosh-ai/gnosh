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
	"context"
	"math"
	"testing"
	"time"

	storetypes "github.com/gnolang/gno/tm2/pkg/store/types"
	"github.com/stretchr/testify/require"
)

// panicValue runs f and returns what it panicked with, or nil.
func panicValue(f func()) (v any) {
	defer func() { v = recover() }()
	f()
	return nil
}

func TestCallMeterPerCallLimit(t *testing.T) {
	t.Parallel()
	var g callMeter
	g.begin(context.Background(), 100, time.Time{})
	g.ConsumeGas(60, "a")
	require.EqualValues(t, 60, g.used())
	require.EqualValues(t, 40, g.Remaining())

	v := panicValue(func() { g.ConsumeGas(41, "b") })
	require.Equal(t, storetypes.OutOfGasError{Descriptor: "b"}, v)
	require.EqualValues(t, 101, g.used(), "gas is recorded before the panic, as in basicGasMeter")

	// A new call measures from its own start; the VM's total keeps counting.
	g.begin(context.Background(), 100, time.Time{})
	require.Zero(t, g.used())
	g.ConsumeGas(10, "c")
	require.EqualValues(t, 10, g.used())
	require.EqualValues(t, 111, g.consumed)
}

func TestCallMeterRejectsNegativeAndOverflow(t *testing.T) {
	t.Parallel()
	var g callMeter
	g.begin(nil, math.MaxInt64, time.Time{})
	require.NotNil(t, panicValue(func() { g.ConsumeGas(-1, "neg") }))
	g.ConsumeGas(math.MaxInt64-5, "big")
	require.IsType(t, storetypes.GasOverflowError{}, panicValue(func() { g.ConsumeGas(10, "over") }))
}

func TestCallMeterRefund(t *testing.T) {
	t.Parallel()
	var g callMeter
	g.begin(nil, 1000, time.Time{})
	g.ConsumeGas(500, "earlier call")
	g.begin(nil, 1000, time.Time{})
	g.ConsumeGas(100, "a")
	g.RefundGas(30, "a")
	require.EqualValues(t, 70, g.used())
	require.NotNil(t, panicValue(func() { g.RefundGas(-5, "neg") }))

	// A refund is capped at what this call used: it never reaches the
	// earlier call's gas, so used() cannot go negative.
	g.RefundGas(1_000_000, "big")
	require.Zero(t, g.used())
	require.EqualValues(t, 500, g.consumed)
	require.False(t, g.IsPastLimit())
}

// A trip stays tripped until the next call: the GnoVM recovers some panics
// itself, and a swallowed out-of-gas must not let the run go on.
func TestCallMeterTripIsSticky(t *testing.T) {
	t.Parallel()
	var g callMeter
	g.begin(context.Background(), 100, time.Time{})
	require.NoError(t, g.tripped())
	first := panicValue(func() { g.ConsumeGas(101, "a") })
	require.Equal(t, storetypes.OutOfGasError{Descriptor: "a"}, first)

	// Even a zero charge panics again once tripped, with the same value.
	require.Equal(t, first, panicValue(func() { g.ConsumeGas(0, "b") }))
	require.ErrorIs(t, g.tripped(), errOutOfGas)

	// A wall-clock or cancellation trip is sticky too.
	ctx, cancel := context.WithCancel(context.Background())
	g.begin(ctx, math.MaxInt64, time.Time{})
	cancel()
	require.Equal(t, stopRun{errCancelled}, panicValue(func() { g.ConsumeGas(pollEvery, "x") }))
	require.Equal(t, stopRun{errCancelled}, panicValue(func() { g.ConsumeGas(1, "y") }))
	require.ErrorIs(t, g.tripped(), errCancelled)

	// The next call starts clean.
	g.begin(context.Background(), 100, time.Time{})
	require.NoError(t, g.tripped())
	require.Nil(t, panicValue(func() { g.ConsumeGas(10, "z") }))
}

func TestCallMeterWallClock(t *testing.T) {
	t.Parallel()
	var g callMeter
	g.begin(context.Background(), math.MaxInt64, time.Now().Add(-time.Millisecond))
	// The deadline is only polled every pollEvery gas.
	require.Nil(t, panicValue(func() { g.ConsumeGas(pollEvery-1, "x") }))
	require.Equal(t, stopRun{errWallClock}, panicValue(func() { g.ConsumeGas(1, "x") }))
}

func TestCallMeterCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var g callMeter
	g.begin(ctx, math.MaxInt64, time.Time{})
	g.ConsumeGas(pollEvery, "x") // polled: not cancelled yet
	cancel()
	require.Equal(t, stopRun{errCancelled}, panicValue(func() { g.ConsumeGas(pollEvery, "x") }))

	// After end, a cancelled context no longer stops anything.
	g.end()
	g.begin(nil, math.MaxInt64, time.Time{})
	require.Nil(t, panicValue(func() { g.ConsumeGas(3*pollEvery, "x") }))
}
