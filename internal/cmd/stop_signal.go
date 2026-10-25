package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// stoppedBySignal is the cause of a context stopOnSignal cancelled: the
// signal that asked the command to stop.
type stoppedBySignal struct{ sig os.Signal }

func (s stoppedBySignal) Error() string { return "stopped by " + s.sig.String() }

// notifySignal and stopSignal are signal.Notify and signal.Stop, as
// variables so a test can deliver a signal without sending one to the
// test process, where the logout context, among others, listens too.
var (
	notifySignal = signal.Notify
	stopSignal   = signal.Stop
)

// stopOnSignal is parent, cancelled when the process gets SIGINT or
// SIGTERM, with the signal as the cause (stoppedBySignal). Only the first
// is taken: the next acts as if none were caught, so a command that does
// not stop in time can be killed with another. stop stops listening.
func stopOnSignal(parent context.Context) (ctx context.Context, stop func()) {
	ctx, cancel := context.WithCancelCause(parent)
	sigs := make(chan os.Signal, 1)
	notifySignal(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-sigs:
			stopSignal(sigs)
			cancel(stoppedBySignal{sig})
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		stopSignal(sigs)
		cancel(nil)
	}
}

// stopsOnSignal is run as a command's RunE that stops on SIGINT or
// SIGTERM: either cancels run's ctx, and a command stopped by SIGTERM
// exits 143 once run has returned, its deferred cleanup done, as Claude
// Code does. klash run asked for os.Kill, which cannot be caught, rather
// than SIGTERM, so SIGTERM ended it on the spot: its Stop hook's checks,
// reviewers among them, ran on with nothing to stop them.
func stopsOnSignal(run func(ctx context.Context, cmd *cobra.Command, args []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		ctx, stop := stopOnSignal(context.Background())
		defer stop()
		return stoppedStatus(ctx, run(ctx, cmd, args))
	}
}

// exitStatus is an error that only sets the status the process exits
// with: nothing is printed for it.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// stoppedStatus is err, or, when ctx was stopped by SIGTERM, the status
// a process SIGTERM killed has, 128 plus the signal: 143. A command
// stopped with SIGINT keeps its error.
func stoppedStatus(ctx context.Context, err error) error {
	if stopped, ok := errors.AsType[stoppedBySignal](context.Cause(ctx)); ok && stopped.sig == syscall.SIGTERM {
		return exitStatus(128 + int(syscall.SIGTERM))
	}
	return err
}

// exitCode is the status the process exits with for a command's error.
func exitCode(err error) int {
	if status, ok := errors.AsType[exitStatus](err); ok {
		return int(status)
	}
	return 1
}
