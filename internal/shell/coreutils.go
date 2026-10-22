package shell

import (
	"runtime"
	"strconv"

	"github.com/charmbracelet/crush/internal/env"
)

var useGoCoreUtils bool

func init() {
	// If CRASH_CORE_UTILS (or CRUSH_CORE_UTILS) is set to either true or
	// false, respect that.
	// By default, enable on Windows only.
	if v, err := strconv.ParseBool(env.Getenv("CRASH_CORE_UTILS")); err == nil {
		useGoCoreUtils = v
	} else {
		useGoCoreUtils = runtime.GOOS == "windows"
	}
}
