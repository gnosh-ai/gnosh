// Package testenv gives tests a clean slate with respect to the program's
// own environment variables. It is imported only from _test.go files.
package testenv

import (
	"os"
	"strings"
)

// prefixes are the environment variable prefixes the program is configured
// through: CRUSH_*, and KLASH_* for the name it is being renamed to.
var prefixes = []string{"CRUSH_", "KLASH_"}

// ClearConfigVars unsets every CRUSH_* and KLASH_* variable in the process
// environment. Call it first thing in a package's TestMain.
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
// t.Setenv("KLASH_DISABLE_PROVIDER_AUTO_UPDATE", "0") and points it at a
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
	os.Setenv("KLASH_DISABLE_PROVIDER_AUTO_UPDATE", "1")
}
