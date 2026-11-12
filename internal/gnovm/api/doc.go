// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

// Package api holds the GnoVM worker protocol and the data types that
// cross the process boundary: length-prefixed JSON frames, the handshake,
// and the request and result structs the client and the worker share.
//
// This package must not import the Gno interpreter
// (github.com/gnolang/gno/gnovm/...). See PROTOCOL.md in the parent
// directory for the on-the-wire contract.
package api
