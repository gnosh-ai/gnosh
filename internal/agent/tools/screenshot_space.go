package tools

import (
	"path/filepath"
	"regexp"
)

// narrowNoBreakSpace is what macOS puts before AM or PM in a screenshot's
// name, where a model typing the name puts a space.
var narrowNoBreakSpace = string(rune(0x202F))

// screenshotNameRE is a macOS screenshot's name, "... 10.47.48 AM.png",
// with either space before AM or PM.
var screenshotNameRE = regexp.MustCompile(`^(.+)([ \x{202F}])(AM|PM)(\.png)$`)

// otherScreenshotSpace is path with the other of the two spaces before AM
// or PM in its name, as Claude Code's Read tries it for a screenshot that
// is not there as named. "" for a name that is no screenshot's.
func otherScreenshotSpace(path string) string {
	dir, name := filepath.Split(path)
	m := screenshotNameRE.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	space := narrowNoBreakSpace
	if m[2] == narrowNoBreakSpace {
		space = " "
	}
	return dir + m[1] + space + m[3] + m[4]
}
