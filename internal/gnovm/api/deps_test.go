// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package api_test

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// TestAPIPackageDoesNotImportGnoInterpreter runs `go list -deps` on this
// package and fails if the Gno interpreter (or any github.com/gnolang/gno
// package) appears. The worker protocol types must stay free of that
// link, so a later split can put them in the FSL binary.
func TestAPIPackageDoesNotImportGnoInterpreter(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "github.com/charmbracelet/crush/internal/gnovm/api")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	var bad []string
	for _, line := range strings.Split(string(bytes.TrimSpace(out)), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "github.com/gnolang/gno/") || line == "github.com/gnolang/gno" {
			bad = append(bad, line)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("internal/gnovm/api must not import the Gno interpreter or other github.com/gnolang/gno packages; go list -deps found:\n%s", strings.Join(bad, "\n"))
	}
}
