// Copyright (C) 2026 Jae Kwon
//
// This file is part of gnosh's GnoVM machine subsystem. It is licensed under
// the GNO Network General Public License, version 6, as published by
// NewTendermint, LLC or its successor, or any later version of that License
// that the Licensor accepts as the proxy under its section 14, by a statement
// published in the canonical copy of internal/gnovm/LICENSE.md; together with
// the Additional Terms (Strong Attribution) in that file.

package gnovmtools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// unnamedTexts finds the texts a call keeps without naming them: one
// string to Think or Fact, each once, nothing else.
func TestUnnamedTexts(t *testing.T) {
	t.Parallel()
	code := "mem.Think(\"the shared port is 8080\")\nmem.Think(\"port\", \"named\")\nmem.Fact(\"cmd/test\", \"go test\")\nmem.Fact(\"the staging db is db-stg-7\")\nmem.Think(\"the shared port is 8080\")\nx := mem.Frame()\n_ = x"
	require.Equal(t, []string{"the shared port is 8080", "the staging db is db-stg-7"}, unnamedTexts(code))
	require.Nil(t, unnamedTexts("package main\nfunc main() { mem.Think(\"a program\") }"), "a whole program is not statements")
	require.Nil(t, unnamedTexts("mem.Think(someVar)"))
}
