package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/fang/v2"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// fakeSignals stands in for signal.Notify and signal.Stop until the test
// ends, and gives what stopOnSignal registered for and a way to deliver a
// signal on its channel. No signal is sent to the test process: there the
// logout context, among others, listens too, and exits on one.
type fakeSignals struct {
	registered chan []os.Signal
	channels   chan chan<- os.Signal
	stopped    chan struct{}
}

func useFakeSignals(t *testing.T) *fakeSignals {
	t.Helper()
	f := &fakeSignals{
		registered: make(chan []os.Signal, 8),
		channels:   make(chan chan<- os.Signal, 8),
		stopped:    make(chan struct{}, 8),
	}
	notifySignal = func(c chan<- os.Signal, sigs ...os.Signal) {
		f.registered <- sigs
		f.channels <- c
	}
	stopSignal = func(chan<- os.Signal) { f.stopped <- struct{}{} }
	t.Cleanup(func() {
		notifySignal = signal.Notify
		stopSignal = signal.Stop
	})
	return f
}

// deliver sends sig on the channel stopOnSignal registered, as the
// runtime would, and waits for ctx to be stopped by it.
func (f *fakeSignals) deliver(t *testing.T, ctx context.Context, sig os.Signal) {
	t.Helper()
	require.Equal(t, []os.Signal{os.Interrupt, syscall.SIGTERM}, <-f.registered, "SIGINT and SIGTERM, which can be caught")
	(<-f.channels) <- sig
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatalf("%v did not stop the context", sig)
	}
	select {
	case <-f.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("after the first signal, the next is not caught")
	}
}

// SIGTERM and SIGINT each stop the context with the signal as the cause.
// A command SIGTERM stopped exits 143, as a process SIGTERM killed would
// and as Claude Code does; one stopped with SIGINT keeps its error.
func TestStopOnSignal(t *testing.T) {
	f := useFakeSignals(t)

	ctx, stop := stopOnSignal(context.Background())
	defer stop()
	f.deliver(t, ctx, syscall.SIGTERM)
	require.Equal(t, stoppedBySignal{syscall.SIGTERM}, context.Cause(ctx))
	err := stoppedStatus(ctx, context.Canceled)
	require.Equal(t, exitStatus(143), err)
	require.Equal(t, 143, exitCode(err))
	// The TUI quits on SIGTERM by itself (Bubble Tea's handler), with no
	// error: it exits 143 all the same.
	require.Equal(t, exitStatus(143), stoppedStatus(ctx, nil))

	ctx, stop = stopOnSignal(context.Background())
	defer stop()
	f.deliver(t, ctx, os.Interrupt)
	require.Equal(t, stoppedBySignal{os.Interrupt}, context.Cause(ctx))
	require.ErrorIs(t, stoppedStatus(ctx, context.Canceled), context.Canceled)
	require.Equal(t, 1, exitCode(context.Canceled))

	// Not stopped by a signal: the error stands.
	ctx, stop = stopOnSignal(context.Background())
	stop()
	require.ErrorIs(t, stoppedStatus(ctx, context.Canceled), context.Canceled)
}

// klash run's RunE: SIGTERM cancels the run, the run's deferred cleanup
// (the app's shutdown, which stops its hooks, tools and jobs) is done
// before the command returns, and the command then exits 143, printing
// nothing. klash run listened for os.Kill, which cannot be caught, so
// SIGTERM ended it on the spot and its Stop hook ran on.
func TestRunStopsOnSIGTERMAfterItsCleanup(t *testing.T) {
	f := useFakeSignals(t)
	cleaned := false
	started := make(chan context.Context, 1)
	runE := stopsOnSignal(func(ctx context.Context, _ *cobra.Command, _ []string) error {
		defer func() { cleaned = true }()
		started <- ctx
		<-ctx.Done()
		return ctx.Err()
	})
	done := make(chan error, 1)
	go func() { done <- runE(&cobra.Command{}, nil) }()
	f.deliver(t, <-started, syscall.SIGTERM)

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not return")
	}
	require.True(t, cleaned, "the run's cleanup is done first")
	require.Equal(t, 143, exitCode(err))

	var printed bytes.Buffer
	errorHandler(&printed, fang.Styles{}, err)
	require.Empty(t, printed.String(), "an exit status prints nothing")
	errorHandler(&printed, fang.Styles{}, errors.New("some failure"))
	require.Contains(t, printed.String(), "Some failure")
}

// idleModel is a TUI program's model that waits for nothing.
type idleModel struct{}

func (idleModel) Init() tea.Cmd                         { return nil }
func (m idleModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (idleModel) View() tea.View                        { return tea.NewView("") }

// A TUI program killed because a signal cancelled its context, as SIGINT
// cancels fang's, hit no internal error: the command returns the
// context's error, where it asked for a bug report.
func TestTUIKilledThroughItsContextIsNoInternalError(t *testing.T) {
	f := useFakeSignals(t)
	ctx, stop := stopOnSignal(context.Background())
	defer stop()
	program := tea.NewProgram(idleModel{}, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	done := make(chan error, 1)
	go func() {
		_, err := program.Run()
		done <- err
	}()
	f.deliver(t, ctx, os.Interrupt)

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the program did not stop")
	}
	require.ErrorIs(t, err, tea.ErrProgramKilled)
	require.ErrorIs(t, tuiStopped(ctx, err), context.Canceled)

	// Killed with its context live, by a panic say: an internal error.
	require.NoError(t, tuiStopped(context.Background(), fmt.Errorf("%w: %w", tea.ErrProgramKilled, tea.ErrProgramPanic)))
}
