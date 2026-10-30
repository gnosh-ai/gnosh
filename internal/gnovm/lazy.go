// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

import "sync"

// AttributionNotice is the notice gnosh shows wherever it names itself
// (--version, gnovm_list): NewTendermint's for the GnoVM, which gnosh
// includes, and this subsystem's own Designation (LICENSE.md).
const AttributionNotice = "Built on gno.land (https://gno.land) · GnoVM machines built on gnosh (https://gnosh.ai)"

// Lazy is an App's hold on the process's client: it acquires it on first use,
// so a gnosh that never runs a VM never starts a worker, and releases it on
// Close.
type Lazy struct {
	opts ClientOptions
	mu   sync.Mutex
	c    *Client
}

// NewLazy returns a Lazy that acquires a client with opts when first used.
func NewLazy(opts ClientOptions) *Lazy { return &Lazy{opts: opts} }

// Get returns the client, acquiring it on the first call. A failure is not
// remembered: the next call tries again.
func (l *Lazy) Get() (*Client, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.c != nil {
		return l.c, nil
	}
	c, err := Acquire(l.opts)
	if err != nil {
		return nil, err
	}
	l.c = c
	return c, nil
}

// Close releases the client if it was acquired. The App calls it in its
// Shutdown, after messages are flushed.
func (l *Lazy) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.c != nil {
		l.c.Release()
		l.c = nil
	}
}
