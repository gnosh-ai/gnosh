package app

import "context"

// licenseNotice is what /license shows: gnosh's copyright, that it comes
// with no warranty, the licence it may be shared under and where to read
// it, and the credits an interface built on gnosh shows. These are the
// "Appropriate Legal Notices" of section 0 of the GNO Network GPL: since
// gnosh shows them, section 5 d asks every interactive interface of a work
// made from it to show them too, and its Additional Terms ask such a work
// to keep the credits (LICENSE.md; DECISIONS item 139).
var licenseNotice = []string{
	"gnosh, Copyright (C) 2026 Jae Kwon. It comes with no warranty.",
	"You may copy, change and share it under the GNO Network General Public License, version 6, with Additional Terms (Strong Attribution): https://github.com/gnosh-ai/gnosh/blob/main/LICENSE.md (the License alone: https://gno.land/license).",
	`An interface built on gnosh shows "built on gnosh" with a link to https://gnosh.ai.`,
	`gnosh includes gno.land's GnoVM (Copyright (C) 2025 NewTendermint, LLC, under the same License with its own Strong Attribution), so it also shows "built on gno.land" with a link to https://gno.land.`,
	"The libraries it is built with keep their own licences (BSD, MIT, ISC, Apache 2.0, MPL 2.0); go.mod names them.",
}

// slashLicense is /license: the legal notices, one paragraph to a line.
func (l *live) slashLicense(_ context.Context, c *SlashContext) error {
	for _, line := range licenseNotice {
		c.Say("%s", line)
	}
	return nil
}
