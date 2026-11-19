package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// stdioTransport is a server gnosh starts, spoken to over its standard
// input and output, one message to a line.
type stdioTransport struct {
	c    *Client
	name string
	cmd  *exec.Cmd
	// in is the server's standard input and out its output, pipes of the
	// client's own: a write to in can be given up, and out closed to end
	// the reader.
	in     *os.File
	out    *os.File
	mu     sync.Mutex
	broken error // a write gave up partway: nothing more is sent
	errs   *capped
	done   chan struct{} // closed when the reader stops
	exited chan struct{} // closed when the server has exited and its standard error is in errs
	maxLen int
}

// stderrWait is how long the reader waits, once the server's output has
// ended, for the server to exit and what it wrote on its standard error
// to come in, since that is what says why it ended; and how long, once
// the server has exited, for the last of its output, which a process it
// left outside its group could hold open for ever.
const stderrWait = 2 * time.Second

// writeWait is the longest one message may wait to be taken by a server
// that does not read its input (64 KiB of it already wait in the pipe):
// the write gives up then, or sooner when the call ends.
const writeWait = 2 * time.Minute

// startStdio starts the server.
func startStdio(c *Client, srv Server, opt Options) (*stdioTransport, error) {
	cmd := exec.Command(srv.Command, srv.Args...)
	cmd.Dir = srv.Dir
	env := append([]string(nil), srv.Environ...)
	for k, v := range srv.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	// A group of its own, so that what it starts is stopped with it.
	ownGroup(cmd)
	t := &stdioTransport{c: c, name: srv.Name, cmd: cmd, errs: &capped{max: opt.MaxStderr},
		done: make(chan struct{}), exited: make(chan struct{}), maxLen: opt.MaxLine}
	cmd.Stderr = t.errs
	// A process of the server's own that keeps its standard error open
	// does not hold up Wait for long.
	cmd.WaitDelay = time.Second
	// Both pipes are the client's own, not ones exec makes: a write to the
	// server's input can then be given up (a deadline), and Wait, which
	// closes exec's own pipes once the server exits and runs here beside
	// the reader, does not close the output before the last of it is read.
	inR, in, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	out, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		in.Close()
		return nil, err
	}
	cmd.Stdin, cmd.Stdout = inR, outW
	err = cmd.Start()
	inR.Close()
	outW.Close()
	if err != nil {
		in.Close()
		out.Close()
		return nil, fmt.Errorf("mcp: %s could not be started: %w", srv.Command, err)
	}
	t.in, t.out = in, out
	go func() {
		_ = cmd.Wait()
		close(t.exited)
	}()
	go t.read(out)
	return t, nil
}

func (t *stdioTransport) stderr() string { return t.errs.String() }

// send writes one message. A server that has stopped reading its input
// does not hold the caller, and every send after it, for ever: the write
// gives up when ctx ends or after writeWait. One that gave up partway
// leaves half a line in the pipe, after which nothing the server reads
// would make sense: the connection ends there.
func (t *stdioTransport) send(ctx context.Context, m message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.broken != nil {
		return t.broken
	}
	deadline := time.Now().Add(writeWait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = t.in.SetWriteDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = t.in.SetWriteDeadline(time.Now()) })
	n, err := t.in.Write(append(data, '\n'))
	stop()
	switch {
	case err == nil:
		return nil
	case n == 0 && ctx.Err() != nil:
		// Nothing of it went: the call was given up, and the connection
		// is as it was.
		return ctx.Err()
	case errors.Is(err, os.ErrDeadlineExceeded):
		t.broken = fmt.Errorf("%w: it stopped reading its input", ErrClosed)
		go t.c.Close()
		return t.broken
	}
	return fmt.Errorf("%w (%v)", ErrClosed, err)
}

// read takes the server's messages until its output ends, or until a
// little after the server has exited: a process it started and left
// outside its group may hold the output open for ever, and the server is
// gone all the same.
func (t *stdioTransport) read(out *os.File) {
	defer close(t.done)
	defer out.Close()
	go func() {
		select {
		case <-t.exited:
			select {
			case <-t.done:
			case <-time.After(stderrWait):
				_ = out.Close()
			}
		case <-t.done:
		}
	}()
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), t.maxLen)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m message
		if json.Unmarshal(line, &m) != nil {
			// A server that writes other text on its output is broken, but
			// one line of it is not worth the connection.
			continue
		}
		t.c.dispatch(m)
	}
	err := sc.Err()
	select {
	case <-t.exited:
	case <-time.After(stderrWait):
	}
	if err != nil {
		t.c.lost(fmt.Errorf("mcp: the server's output could not be read: %w", err))
		return
	}
	t.c.lost(nil)
}

// close closes the server's input, and then sends its group SIGINT,
// SIGTERM 100 ms later and SIGKILL 400 ms after that, as long as the
// server runs; what it started in its group is sent them too, even once
// the server itself has exited. It waits for the reader, whose end it
// forces when the output stays open (a process outside the group holds
// it), so that it never waits for ever.
func (t *stdioTransport) close() error {
	_ = t.in.Close()
	gone := func() bool {
		select {
		case <-t.exited:
			return true
		default:
			return false
		}
	}
	for i, wait := range []time.Duration{100 * time.Millisecond, 400 * time.Millisecond, 2 * time.Second} {
		if gone() {
			// What it left running in its group is ended at once.
			stop(t.cmd, 2, true)
			break
		}
		stop(t.cmd, i, false)
		select {
		case <-t.exited:
		case <-time.After(wait):
		}
	}
	select {
	case <-t.done:
	case <-time.After(stderrWait):
		_ = t.out.Close()
		<-t.done
	}
	if !gone() {
		return fmt.Errorf("mcp: %s did not stop", t.name)
	}
	return nil
}
