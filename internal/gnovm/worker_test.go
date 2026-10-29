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
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// pipeWorker runs serve in a goroutine over in-process pipes, with nothing
// exec'd, and returns the client's ends and serve's result.
func pipeWorker(t *testing.T) (send func(*frame), recv func() *frame, closeReq func(), served <-chan error) {
	t.Helper()
	reqR, reqW := io.Pipe()
	repR, repW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- serve(reqR, repW, nil)
		repW.Close()
	}()
	send = func(f *frame) { require.NoError(t, writeFrame(reqW, f)) }
	recv = func() *frame {
		f, err := readFrame(repR)
		require.NoError(t, err)
		return f
	}
	return send, recv, func() { reqW.Close() }, done
}

func clientHello(dir string) *frame {
	h := myHello()
	h.Dir, h.Client = dir, NewClientID()
	return &frame{Type: msgHello, Body: mustJSON(h)}
}

// recvResult skips heartbeats and acks to the next result.
func recvResult(t *testing.T, recv func() *frame) (f *frame, heartbeats int) {
	t.Helper()
	for {
		f := recv()
		switch f.Type {
		case msgHeartbeat:
			heartbeats++
		case msgAck:
		default:
			return f, heartbeats
		}
	}
}

func TestWorkerServesRequests(t *testing.T) {
	t.Parallel()
	send, recv, closeReq, served := pipeWorker(t)
	send(clientHello(t.TempDir()))
	h := recv()
	require.Equal(t, msgHello, h.Type)
	require.Nil(t, h.Err)

	send(&frame{ID: 1, Type: msgRun, Body: mustJSON(RunRequest{Project: "/p", Session: "s", Code: `x := 20`})})
	f, _ := recvResult(t, recv)
	require.EqualValues(t, 1, f.ID)
	require.Nil(t, f.Err)

	send(&frame{ID: 2, Type: msgRun, Body: mustJSON(RunRequest{Project: "/p", Session: "s", Code: `x * 2`})})
	f, _ = recvResult(t, recv)
	var wr wireRun
	require.NoError(t, json.Unmarshal(f.Body, &wr))
	require.Equal(t, "40", wr.Value)

	// A refusal keeps its kind across the wire.
	send(&frame{ID: 3, Type: msgRun, Body: mustJSON(RunRequest{Project: "/p", Name: GoalsVM, Code: `1`})})
	f, _ = recvResult(t, recv)
	var ref *refusal
	require.ErrorAs(t, fromWire(f.Err), &ref)

	closeReq()
	require.NoError(t, <-served, "the worker quits cleanly when its client goes")
}

// A long run sends heartbeats the whole time, and a cancel stops it at once.
func TestWorkerHeartbeatsAndCancel(t *testing.T) {
	t.Parallel()
	send, recv, closeReq, served := pipeWorker(t)
	send(clientHello(t.TempDir()))
	recv()

	start := time.Now()
	send(&frame{ID: 1, Type: msgRun, Body: mustJSON(RunRequest{Project: "/p", Session: "s", Code: `for {}`, Gas: maxGas})})
	time.Sleep(1600 * time.Millisecond)
	send(&frame{ID: 1, Type: msgCancel})
	f, beats := recvResult(t, recv)
	require.GreaterOrEqual(t, beats, 2, "heartbeats while the run ran")
	require.Less(t, time.Since(start), 5*time.Second, "the cancel stopped it")
	var wr wireRun
	require.NoError(t, json.Unmarshal(f.Body, &wr))
	var lim *limitError
	require.ErrorAs(t, fromWire(wr.Err), &lim)
	require.Equal(t, "cancelled", lim.kind)

	closeReq()
	require.NoError(t, <-served)
}

func TestWorkerRefusesABadHandshake(t *testing.T) {
	t.Parallel()
	send, recv, _, served := pipeWorker(t)
	other := myHello()
	other.Build = "another-build"
	send(&frame{Type: msgHello, Body: mustJSON(other)})
	f := recv()
	require.NotNil(t, f.Err)
	require.Contains(t, f.Err.Msg, "restart klash")
	require.ErrorIs(t, <-served, errHandshake)

	// No hello at all: the worker gives up after handshakeWait.
	_, _, _, served = pipeWorker(t)
	require.ErrorIs(t, <-served, errHandshake)
}

// Tests run VMs in-process: a client under go test never spawns its own
// binary, the cause of the October fork bomb on the AI box. (Not parallel:
// the client is process-wide.)
func TestTestsCannotSpawnAWorker(t *testing.T) {
	c, err := Acquire(ClientOptions{Options: Options{Dir: t.TempDir()}})
	require.NoError(t, err)
	_, err = c.Run(context.Background(), RunRequest{Project: "/p", Session: "s", Code: `1`})
	require.ErrorContains(t, err, "in-process")
	c.Release()

	self, err := os.Executable()
	require.NoError(t, err)
	c, err = Acquire(ClientOptions{Options: Options{Dir: t.TempDir()}, Binary: self})
	require.NoError(t, err)
	_, err = c.Run(context.Background(), RunRequest{Project: "/p", Session: "s", Code: `1`})
	require.ErrorContains(t, err, "own binary")
	c.Release()
}

// The in-process client shares one host by refcount; the last Release
// deletes its memory-only VMs. (Not parallel: the client is process-wide.)
func TestInProcessClient(t *testing.T) {
	dir := t.TempDir()
	c1, err := Acquire(ClientOptions{Options: Options{Dir: dir}, InProcess: true})
	require.NoError(t, err)
	c2, err := Acquire(ClientOptions{Options: Options{Dir: dir}, InProcess: true})
	require.NoError(t, err)
	require.Same(t, c1, c2)

	res, err := c1.Run(context.Background(), RunRequest{Project: "/p", Session: "s", Code: `v := 7`})
	require.NoError(t, err)
	require.NoError(t, res.Err)
	c1.Release() // one App closes; the other still uses it
	res, err = c2.Run(context.Background(), RunRequest{Project: "/p", Session: "s", Code: `v`})
	require.NoError(t, err)
	require.Equal(t, "7", res.Value)

	ephemeral, _ := filepath.Glob(filepath.Join(dir, "*", ".ephemeral", "*"))
	require.NotEmpty(t, ephemeral)
	c2.Release()
	ephemeral, _ = filepath.Glob(filepath.Join(dir, "*", ".ephemeral", "*"))
	require.Empty(t, ephemeral, "the last Release deletes memory-only VMs")
}

// The client kills a worker that is running a request and silent, or
// overdue; an idle worker is left alone.
func TestWorkerKillVerdict(t *testing.T) {
	t.Parallel()
	c := &workerConn{pending: map[uint64]*pendingReq{}}
	now := time.Now()
	c.last.Store(now.Add(-time.Hour).UnixNano())
	require.Empty(t, c.verdict(now), "idle")

	c.pending[1] = &pendingReq{deadline: now.Add(time.Minute)}
	c.last.Store(now.Add(-2 * time.Second).UnixNano())
	require.Empty(t, c.verdict(now), "busy and heard from recently")
	c.last.Store(now.Add(-11 * time.Second).UnixNano())
	require.Contains(t, c.verdict(now), "no heartbeat")

	c.last.Store(now.UnixNano())
	c.pending[1].deadline = now.Add(-overdueKill - time.Second)
	require.Contains(t, c.verdict(now), "deadline")
}

// Errors keep their kinds across the wire.
func TestWireErrors(t *testing.T) {
	t.Parallel()
	for _, err := range []error{
		&refusal{"r"}, &busyError{"b"}, &parseError{errors.New("p")}, &limitError{kind: "memory"},
		&compileError{"c"}, &panicked{"x"}, &brokenError{vm: "v", cause: errBrokenJournal},
		&runError{err: &panicked{"y"}, stack: "at main"},
	} {
		got := fromWire(toWire(err))
		require.IsType(t, err, got)
		require.Equal(t, err.Error(), got.Error())
	}
}
