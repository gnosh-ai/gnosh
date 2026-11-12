// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovm

import (
	"encoding/json"
	"io"

	"github.com/charmbracelet/crush/internal/gnovm/api"
)

const (
	MemVM = api.MemVM

	protocolMagic   = api.ProtocolMagic
	protocolVersion = api.ProtocolVersion
	maxFrame        = api.MaxFrame

	msgHello     = api.MsgHello
	msgRun       = api.MsgRun
	msgNew       = api.MsgNew
	msgList      = api.MsgList
	msgDrop      = api.MsgDrop
	msgKept      = api.MsgKept
	msgRepair    = api.MsgRepair
	msgMem       = api.MsgMem
	msgGoal      = api.MsgGoal
	msgCancel    = api.MsgCancel
	msgResult    = api.MsgResult
	msgAck       = api.MsgAck
	msgHeartbeat = api.MsgHeartbeat

	memRedacted  = api.RedactedText
	memMinErased = api.MinErased
)

type (
	RunRequest  = api.RunRequest
	RunResult   = api.RunResult
	NewRequest  = api.NewRequest
	VMOptions   = api.VMOptions
	VMInfo      = api.VMInfo
	ListResult  = api.ListResult
	HostMemory  = api.HostMemory
	MemFrame    = api.MemFrame
	MemClose    = api.MemClose
	MemGoal     = api.MemGoal
	MemStep     = api.MemStep
	MemRule     = api.MemRule
	MemWait     = api.MemWait
	MemErasure  = api.MemErasure
	GoalBinding = api.GoalBinding
	GoalTurn    = api.GoalTurn
	ClosedGoal  = api.ClosedGoal

	frame      = api.Frame
	hello      = api.Hello
	wireError  = api.WireError
	wireRun    = api.WireRun
	listReq    = api.ListReq
	dropReq    = api.DropReq
	keptReq    = api.KeptReq
	keptResp   = api.KeptResp
	repairReq  = api.RepairReq
	memReq     = api.MemReq
	memResp    = api.MemResp
	goalReq    = api.GoalReq
	goalResp   = api.GoalResp
	newResp    = api.NewResp
	repairResp = api.RepairResp
)

func writeFrame(w io.Writer, f *frame) error { return api.WriteFrame(w, f) }
func readFrame(r io.Reader) (*frame, error)  { return api.ReadFrame(r) }
func mustJSON(v any) json.RawMessage         { return api.MustJSON(v) }

func quotedInner(s string) string { return api.QuotedInner(s) }
