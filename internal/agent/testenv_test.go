package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The package's tests see a global config directory of their own
// (testenv.Run), not the user's: the AI box's crush user keeps a coder
// prompt template there, and under it TestCoderPromptNamesTheAddedDirs
// failed while passing as root.
func TestTestsSeeNoUserConfig(t *testing.T) {
	require.True(t, strings.HasPrefix(UserTemplateDir(), os.TempDir()), UserTemplateDir())
}
