// Copyright (C) 2026 Jae Kwon
//
// This file is part of klash's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

//go:build (darwin && (amd64 || arm64)) || (freebsd && (amd64 || arm64)) || (linux && (386 || amd64 || arm || arm64 || loong64 || ppc64le || riscv64 || s390x)) || (windows && (386 || amd64 || arm64))

package gnovm

import (
	"database/sql"
	"net/url"

	_ "modernc.org/sqlite"
)

// openSQLite opens the SQLite file at path with klash's driver for this
// platform (internal/db picks the same), with the pragmas given.
func openSQLite(path string, pragmas map[string]string) (*sql.DB, error) {
	params := url.Values{}
	for name, value := range pragmas {
		params.Add("_pragma", name+"("+value+")")
	}
	params.Set("_txlock", "immediate")
	return sql.Open("sqlite", "file:"+path+"?"+params.Encode())
}
