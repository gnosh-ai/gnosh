package hooks

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Every hook is told where the project's mem keeps its status.json, under
// each of a hook variable's names, while gnosh's GnoVM machines are on;
// nothing, while they are off. (Not parallel: it sets what BuildEnv reads.)
func TestBuildEnvNamesMemStatus(t *testing.T) {
	SetMemStatusPath(func(projectDir string) string { return projectDir + "/mem/status.json" })
	t.Cleanup(func() { SetMemStatusPath(nil) })
	env := BuildEnv(EventStop, "", "s1", "/proj/sub", "/proj", "")
	for _, prefix := range []string{"GNOSH_", "KLASH_", "CRUSH_"} {
		require.Contains(t, env, prefix+"MEM_STATUS=/proj/mem/status.json")
	}
	SetMemStatusPath(nil)
	for _, v := range BuildEnv(EventStop, "", "s1", "/proj/sub", "/proj", "") {
		require.NotContains(t, v, "MEM_STATUS=")
	}
}
