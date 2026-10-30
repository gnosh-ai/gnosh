// Package testenv gives tests a clean slate with respect to the program's
// own environment variables. It is imported only from _test.go files.
package testenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// prefixes are the environment variable prefixes the program is configured
// through: GNOSH_*, and KLASH_* and CRUSH_* from before the renames.
var prefixes = []string{"GNOSH_", "KLASH_", "CRUSH_"}

// ClearConfigVars unsets every GNOSH_*, KLASH_* and CRUSH_* variable in the
// process environment. Run calls it; a package's TestMain should call Run.
//
// The program is configured through these variables, and everything it runs
// inherits them — including `go test`, run from inside it by an agent
// working on its own code. Tests that assert on default behaviour then fail
// for reasons that have nothing to do with the code: the AI box's wrapper
// exports CRUSH_DISABLE_DEFAULT_PROVIDERS=1, and under it a dozen provider
// tests failed while passing from a clean shell. Tests that need a
// variable set it themselves with t.Setenv, as before.
//
// It then turns off the provider catalog's auto-update, so no test fetches
// the catalog from Charm's servers (catwalk and Hyper): the network is no
// part of a unit test, and Jae runs the program with that fetch off. A test
// of the fetch itself opts back in with
// t.Setenv("GNOSH_DISABLE_PROVIDER_AUTO_UPDATE", "0") and points it at a
// local server.
func ClearConfigVars() {
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		for _, p := range prefixes {
			if strings.HasPrefix(key, p) {
				os.Unsetenv(key)
				break
			}
		}
	}
	os.Setenv("GNOSH_DISABLE_PROVIDER_AUTO_UPDATE", "1")
}

// Run runs a package's tests, as its TestMain, os.Exit(testenv.Run(m)):
// on a clean slate (ClearConfigVars), with the global config and data
// directories in a temp dir of their own, removed after.
//
// Without that, a test reads the user's own config directory: the coder
// prompt template a user keeps there, their tool descriptions, their
// gnosh.json. The AI box's self-improvement runs work as a user with its
// own coder.md.tpl, and under it TestCoderPromptNamesTheAddedDirs failed,
// though it passed as root; the run was then held at its Stop hook by a
// test it never touched.
//
// The directories go in the XDG variables, which every other way of
// naming them wins over: a test's own XDG_CONFIG_HOME or XDG_DATA_HOME
// (the tests that set HOME set these too), and GNOSH_GLOBAL_* or
// CRUSH_GLOBAL_*. Set in CRUSH_GLOBAL_* at first, they won over a test's
// own XDG_DATA_HOME, and tests that isolated that way shared one
// directory.
func Run(m *testing.M) int {
	ClearConfigVars()
	home, err := os.MkdirTemp("", "gnosh-test")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(home)
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	os.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	os.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	return m.Run()
}
