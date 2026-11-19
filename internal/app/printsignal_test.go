package app

import (
	"context"
	"io"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// A signal ends a run that waits on what is piped in, as it ends one that
// waits on a turn, with the signal's exit code: it was read only while a
// turn ran, and a producer that stalled held the run until SIGKILL. (The
// print mode review of 2026-11-19.)
func TestSignalWhileReadingPipedInput(t *testing.T) {
	w := apptest.NewWorld(t)
	o := testOptions(w, "-p")
	pr, pw := io.Pipe()
	defer pw.Close()
	o.Stdin, o.StdinIsTerminal = pr, false
	signals := make(chan os.Signal, 4)
	o.Signals = signals
	go func() {
		time.Sleep(300 * time.Millisecond)
		signals <- syscall.SIGTERM
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	start := time.Now()
	r := runWith(t, ctx, o)
	if took := time.Since(start); r.code != 143 || took > 10*time.Second {
		t.Errorf("exit code %d after %v, want 143 at once (stderr %q)", r.code, took.Round(time.Millisecond), r.err)
	}
	if len(w.Model.Requests()) != 0 {
		t.Error("the model was asked")
	}
}
