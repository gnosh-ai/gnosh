// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, or any later version
// that the Licensor has publicly accepted as the proxy under section 14 of
// that License, with the Additional Terms (Strong Attribution); see
// internal/gnovm/LICENSE.md.

//go:build unix

package gnovm

import (
	"bytes"
	"log"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"syscall"
	"time"
)

// WorkerMain is `klash gnovm-worker`: the child process that runs the VMs of
// one klash process, speaking the protocol on fds 3 and 4. It does nothing
// without a valid handshake from a klash client of the same build, so a
// stray run (or a test binary given the argument) exits at once. It returns
// the exit code.
func WorkerMain() int {
	// Ctrl-C in klash must not kill the worker in the middle of an append;
	// klash stops it by closing its request pipe.
	signal.Ignore(syscall.SIGINT, syscall.SIGHUP)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetPrefix("gnovm-worker: ")
	in := os.NewFile(3, "gnovm-requests")
	out := os.NewFile(4, "gnovm-replies")
	if err := serve(in, out, limitWorker); err != nil {
		log.Print(err)
		return 2
	}
	return 0
}

// limitWorker protects the host from the worker: the OOM killer takes it
// before anything else (llama-server on the AI box), the Go runtime collects
// harder as it nears the budget, and a watchdog ends it, saying why, if it
// grows well past it.
func limitWorker(h hello) {
	_ = os.WriteFile("/proc/self/oom_score_adj", []byte("1000"), 0)
	budget := h.BudgetBytes
	if budget <= 0 {
		budget = autoBudgetMax
	}
	debug.SetMemoryLimit(budget + 512<<20)
	limit := 2*budget + 1<<30
	go func() {
		for range time.Tick(time.Second) {
			if rss := residentBytes(); rss > limit {
				log.Printf("resident memory %d MB is past the watchdog's %d MB: exiting", rss>>20, limit>>20)
				os.Exit(3)
			}
		}
	}()
}

// residentBytes is the process's resident set, or 0 where /proc has no
// statm (macOS: the memory limit alone guards it there).
func residentBytes() int64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := bytes.Fields(b)
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(string(fields[1]), 10, 64)
	if err != nil {
		return 0
	}
	return pages * int64(os.Getpagesize())
}
