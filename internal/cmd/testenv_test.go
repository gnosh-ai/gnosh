package cmd

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// A test that names its own XDG directories gets them: testenv.Run keeps
// the global config and data in the XDG variables, which a test's own
// setting wins over. It set CRUSH_GLOBAL_* first, which win over XDG, so
// the projects tests, which isolate with XDG_DATA_HOME, shared one file.
func TestATestsOwnXDGDirectoriesWin(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	require.True(t, strings.HasPrefix(config.GlobalConfigData(), data), config.GlobalConfigData())

	conf := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", conf)
	require.True(t, strings.HasPrefix(config.GlobalConfig(), conf), config.GlobalConfig())
}
