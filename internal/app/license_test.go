package app

import (
	"testing"

	"github.com/gnosh-ai/gnosh/internal/app/apptest"
)

// /license shows the legal notices: the copyright, no warranty, the
// licence and where it is, and both credits; /licence is the same.
func TestLicense(t *testing.T) {
	w := apptest.NewWorld(t)
	for _, cmd := range []string{"/license", "/licence"} {
		r := run(t, w, "-p", cmd).wantCode(t, 0)
		has(t, cmd, r.out, "Copyright (C) 2026 Jae Kwon", "no warranty",
			"GNO Network General Public License, version 6", "https://github.com/gnosh-ai/gnosh/blob/main/LICENSE.md",
			`"built on gnosh" with a link to https://gnosh.ai`, `"built on gno.land" with a link to https://gno.land`)
	}
}
