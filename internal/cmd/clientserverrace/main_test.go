package clientserverrace_test

import (
	"os"
	"testing"

	"github.com/charmbracelet/crush/internal/testenv"
)

// TestMain clears inherited CRUSH_* and CRASH_* settings, so these tests
// pass or fail on the code alone and not on the environment of whoever runs
// them; see testenv.ClearConfigVars.
func TestMain(m *testing.M) {
	testenv.ClearConfigVars()
	os.Exit(m.Run())
}
