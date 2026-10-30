package version

import (
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
)

// Name is the product's display name: what the program calls itself on
// screen, in help text and in the agent's own prompt. It is deliberately
// separate from the names on disk (config and data directories, config
// files, environment variables), which keep their established spellings so
// existing setups go on working.
const Name = "Gnosh"

// Command is the command users type to run it. The old "crush" command is
// still installed alongside, so either works; this is the one shown in help
// and in hints such as the resume line printed on exit.
const Command = "gnosh"

// Build-time parameters set via -ldflags.

var (
	Version = "devel"
	Commit  = "unknown"
	// BuildID is a unique identifier for this build. For release builds it
	// equals Commit; for development builds (go run / go build without
	// ldflags) it is derived from the executable's modification time, which
	// changes on every recompilation.
	BuildID = ""
)

// A user may install crush using `go install github.com/charmbracelet/crush@latest`.
// without -ldflags, in which case the version above is unset. As a workaround
// we use the embedded build version that *is* set when using `go install` (and
// is only set for `go install` and not for `go build`).
func init() {
	info, ok := debug.ReadBuildInfo()
	if ok {
		mainVersion := info.Main.Version
		if mainVersion != "" && mainVersion != "(devel)" {
			Version = mainVersion
		}
	}

	// Derive BuildID when not set via ldflags.
	if BuildID == "" {
		BuildID = deriveBuildID()
	}
}

// deriveBuildID uses the running executable's modification time as a unique
// build fingerprint. This changes on every recompilation (including `go run`),
// making it reliable for detecting stale servers during development.
//
// On Linux it is the running image's, through /proc/self/exe, not that of
// whatever file is at the executable's path now: the binary may be replaced
// while this process runs (a deploy), and the gnovm worker it then starts
// from /proc/self/exe is this same image, which must have this same ID.
func deriveBuildID() string {
	fi, err := os.Stat("/proc/self/exe")
	if runtime.GOOS != "linux" || err != nil {
		exe, err := os.Executable()
		if err != nil {
			return "unknown"
		}
		if fi, err = os.Stat(exe); err != nil {
			return "unknown"
		}
	}
	return strconv.FormatInt(fi.ModTime().UnixNano(), 36)
}
