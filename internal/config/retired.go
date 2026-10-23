package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/home"
)

// retiredAppName is the name the fork went by for a day, Crash, before it
// was renamed Klash. Jae chose to replace it rather than keep it working
// beside klash and crush, so nothing under it is read. What is found under
// it is reported instead (RetiredNames), since settings that quietly stop
// applying look like settings that still apply: the self-improvement
// reviewers' crash.json was dropped that way the night of the rename, and
// a CRASH_DISABLE_METRICS no longer read would let the program phone home.
const retiredAppName = "crash"

// RetiredNames lists what lies about or is set under the name from before
// the rename, each with the name read in its place: config files and the
// data directory in the project, the global config and data directories,
// and CRASH_ environment variables.
func RetiredNames(workingDir string) []string {
	var found []string
	names := []string{"." + retiredAppName + "rc", retiredAppName + "rc", "." + retiredAppName + ".json", retiredAppName + ".json", "." + retiredAppName}
	paths, _ := fsext.LookupBounded(workingDir, projectBoundary(workingDir), names...)
	for _, p := range paths {
		renamed := strings.Replace(filepath.Base(p), retiredAppName, appName, 1)
		found = append(found, home.Short(p)+" (now "+renamed+")")
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home.Dir(), ".local", "share")
	}
	for _, base := range []string{home.Config(), dataHome} {
		dir := filepath.Join(base, retiredAppName)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			found = append(found, home.Short(dir)+" (now "+home.Short(filepath.Join(base, appName))+")")
		}
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if rest, ok := strings.CutPrefix(name, "CRASH_"); ok && rest != "" {
			found = append(found, name+" (now KLASH_"+rest+")")
		}
	}
	return found
}
