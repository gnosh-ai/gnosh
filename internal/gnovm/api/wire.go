// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package api

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// The worker protocol: frames of a 4-byte big-endian length and a JSON
// message, on two pipes the client hands the worker as fds 3 (requests) and
// 4 (replies), so nothing the worker's code prints can corrupt it.

const (
	ProtocolMagic   = "gnosh-gnovm"
	ProtocolVersion = 1
	// MaxFrame bounds a frame; results are capped well below it.
	MaxFrame = 16 << 20
)

// Message types.
const (
	MsgHello     = "hello"
	MsgRun       = "run"
	MsgNew       = "new"
	MsgList      = "list"
	MsgDrop      = "drop"
	MsgKept      = "kept"
	MsgRepair    = "repair"
	MsgMem       = "mem"
	MsgGoal      = "goal"
	MsgCancel    = "cancel"
	MsgResult    = "result"
	MsgAck       = "ack"
	MsgHeartbeat = "heartbeat"
)

// Frame is one message either way.
type Frame struct {
	ID   uint64          `json:"id"`
	Type string          `json:"type"`
	Body json.RawMessage `json:"body,omitempty"`
	Err  *WireError      `json:"err,omitempty"`
	// Progress counts heartbeats within a request, so a stuck worker and
	// a slow one look different in the log.
	Progress uint64 `json:"progress,omitempty"`
	// Waiting marks a heartbeat sent while the request waits for mem's
	// lock, which another gnosh process holds: the client starts the
	// request's deadline again when the wait ends.
	Waiting bool `json:"waiting,omitempty"`
}

// Hello is the handshake, both ways: the worker answers with its own, and
// either side quits on a mismatch.
type Hello struct {
	Magic    string `json:"magic"`
	Protocol int    `json:"protocol"`
	Build    string `json:"build"`
	Gno      string `json:"gno"`
	// Set by the client only.
	Dir             string  `json:"dir,omitempty"`
	Client          string  `json:"client,omitempty"`
	BudgetBytes     int64   `json:"budget_bytes,omitempty"`
	DefaultMemoryMB int     `json:"default_memory_mb,omitempty"`
	DefaultGas      int64   `json:"default_gas,omitempty"`
	NsPerGas        float64 `json:"ns_per_gas,omitempty"`
	Embeddings      string  `json:"embeddings,omitempty"`
}

// WriteFrame writes one length-prefixed JSON frame to w.
func WriteFrame(w io.Writer, f *Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(b) > MaxFrame {
		return fmt.Errorf("frame of %d bytes is over the %d limit", len(b), MaxFrame)
	}
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	if _, err := w.Write(n[:]); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// ReadFrame reads one length-prefixed JSON frame from r.
func ReadFrame(r io.Reader) (*Frame, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(n[:])
	if size > MaxFrame {
		return nil, fmt.Errorf("frame of %d bytes is over the %d limit", size, MaxFrame)
	}
	b := make([]byte, size)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// WireError carries an error across the process boundary with its kind, so
// the client gets back the same types the host returns.
type WireError struct {
	Kind  string     `json:"kind"`
	Msg   string     `json:"msg"`
	Again bool       `json:"again,omitempty"`
	Limit string     `json:"limit,omitempty"`
	Stack string     `json:"stack,omitempty"`
	Inner *WireError `json:"inner,omitempty"`
}

// WireRun is RunResult on the wire, its error carried as a WireError.
type WireRun struct {
	RunResult
	Err *WireError `json:"run_err,omitempty"`
}

// The other requests' bodies.
type (
	ListReq struct {
		Project string `json:"project"`
	}
	DropReq struct {
		Project string `json:"project"`
		Name    string `json:"name"`
		Action  string `json:"action"`
	}
	// KeptReq asks whether a call was kept; Frames, for mem, asks for the
	// frames it pushed and popped too, answered as a KeptResp (a bare bool
	// otherwise).
	KeptReq struct {
		Project string `json:"project"`
		Name    string `json:"name"`
		Req     string `json:"req"`
		Frames  bool   `json:"frames,omitempty"`
	}
	KeptResp struct {
		Kept   bool       `json:"kept"`
		Frames []MemFrame `json:"frames,omitempty"`
	}
	RepairReq struct {
		Project string `json:"project"`
		Name    string `json:"name"`
	}
	MemReq struct {
		Project string            `json:"project"`
		Session string            `json:"session"`
		Args    string            `json:"args"`
		Titles  map[string]string `json:"titles,omitempty"`
	}
	MemResp struct {
		Text   string      `json:"text"`
		Erased *MemErasure `json:"erased,omitempty"`
	}
	// GoalReq is one of the goal calls: Op is "bind", "turn", "reopen",
	// "closed" or "clear".
	GoalReq struct {
		Op        string       `json:"op"`
		Project   string       `json:"project"`
		Session   string       `json:"session,omitempty"`
		Condition string       `json:"condition,omitempty"`
		Binding   GoalBinding  `json:"binding"`
		Prev      *GoalBinding `json:"prev,omitempty"`
		Reason    string       `json:"reason,omitempty"`
		Marker    string       `json:"marker,omitempty"`
		Since     int64        `json:"since,omitempty"`
		Except    string       `json:"except,omitempty"`
	}
	GoalResp struct {
		Binding GoalBinding  `json:"binding"`
		Turn    GoalTurn     `json:"turn"`
		Text    string       `json:"text,omitempty"`
		Closed  []ClosedGoal `json:"closed,omitempty"`
	}
	NewResp struct {
		Info    VMInfo `json:"info"`
		Created bool   `json:"created"`
	}
	RepairResp struct {
		Kept int `json:"kept"`
		Cut  int `json:"cut"`
	}
)

// MustJSON marshals v as JSON. The values on this protocol are built by
// gnosh and are always encodable; it panics if they are not.
func MustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
