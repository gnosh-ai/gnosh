package log

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// earlyLimit is how many records are kept from before Setup.
const earlyLimit = 1000

// Early is the handler for the time before Setup decides where logs go.
// It keeps what is logged, and Setup writes it to the log file. Config
// loading runs in that time, since the log file lives in the data
// directory the config names, and every warning it gave (a provider
// skipped for a missing API key, a default model that does not exist, a
// crushrc beside a crush.json) used to be thrown away.
func Early() slog.Handler { return early }

var early = newEarlyHandler(earlyLimit)

// earlyBuffer is shared by the early handler and every handler made from
// it with WithAttrs or WithGroup.
type earlyBuffer struct {
	mu      sync.Mutex
	limit   int
	records []earlyRecord
	dropped int
	// final is where records go once Setup has run, so that a logger
	// made from the early handler before then keeps working.
	final slog.Handler
}

type earlyRecord struct {
	ops    []earlyOp
	record slog.Record
}

// earlyOp is a WithAttrs or WithGroup call made on the early handler,
// made again on the final handler, in the same order.
type earlyOp struct {
	group string
	attrs []slog.Attr
}

type earlyHandler struct {
	buf *earlyBuffer
	ops []earlyOp
}

func newEarlyHandler(limit int) *earlyHandler {
	return &earlyHandler{buf: &earlyBuffer{limit: limit}}
}

func (h *earlyHandler) Enabled(ctx context.Context, level slog.Level) bool {
	h.buf.mu.Lock()
	final := h.buf.final
	h.buf.mu.Unlock()
	if final != nil {
		return final.Enabled(ctx, level)
	}
	// The level is not known before Setup; drainTo filters.
	return true
}

func (h *earlyHandler) Handle(ctx context.Context, r slog.Record) error {
	h.buf.mu.Lock()
	if final := h.buf.final; final != nil {
		h.buf.mu.Unlock()
		return withOps(final, h.ops).Handle(ctx, r)
	}
	defer h.buf.mu.Unlock()
	if len(h.buf.records) >= h.buf.limit {
		h.buf.dropped++
		return nil
	}
	h.buf.records = append(h.buf.records, earlyRecord{ops: h.ops, record: r.Clone()})
	return nil
}

func (h *earlyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	return &earlyHandler{buf: h.buf, ops: append(slices.Clip(h.ops), earlyOp{attrs: attrs})}
}

func (h *earlyHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &earlyHandler{buf: h.buf, ops: append(slices.Clip(h.ops), earlyOp{group: name})}
}

func withOps(h slog.Handler, ops []earlyOp) slog.Handler {
	for _, op := range ops {
		if op.group != "" {
			h = h.WithGroup(op.group)
		} else {
			h = h.WithAttrs(op.attrs)
		}
	}
	return h
}

// drainTo writes the kept records to final in the order they were
// logged, leaving out those below its level, and from then on sends
// everything to final.
func (b *earlyBuffer) drainTo(final slog.Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ctx := context.Background()
	for _, er := range b.records {
		h := withOps(final, er.ops)
		if h.Enabled(ctx, er.record.Level) {
			_ = h.Handle(ctx, er.record)
		}
	}
	if b.dropped > 0 {
		r := slog.NewRecord(time.Now(), slog.LevelWarn, "Log records from before logging was set up were dropped", 0)
		r.AddAttrs(slog.Int("dropped", b.dropped), slog.Int("kept", len(b.records)))
		_ = final.Handle(ctx, r)
	}
	b.records = nil
	b.final = final
}
