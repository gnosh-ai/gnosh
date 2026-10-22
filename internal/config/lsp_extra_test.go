package config

import (
	"testing"

	powernapConfig "github.com/charmbracelet/x/powernap/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestExtraLSPServersFillGnoplsDefaults(t *testing.T) {
	t.Parallel()
	config := &Config{LSP: map[string]LSPConfig{"gnopls": {}}}
	config.applyLSPDefaults()
	got := config.LSP["gnopls"]
	require.Equal(t, "gnopls", got.Command)
	require.Equal(t, []string{"gno"}, got.FileTypes)
	require.Equal(t, []string{"gnomod.toml", "gnowork.toml", "gno.mod"}, got.RootMarkers)
}

func TestAddExtraLSPServersKeepsAnExistingEntry(t *testing.T) {
	t.Parallel()
	m := powernapConfig.NewManager()
	m.AddServer("gnopls", &powernapConfig.ServerConfig{Command: "my-gnopls"})
	AddExtraLSPServers(m)
	got, ok := m.GetServer("gnopls")
	require.True(t, ok)
	require.Equal(t, "my-gnopls", got.Command)
}

func TestAddExtraLSPServersGivesEachManagerItsOwnCopy(t *testing.T) {
	t.Parallel()
	a, b := powernapConfig.NewManager(), powernapConfig.NewManager()
	AddExtraLSPServers(a)
	AddExtraLSPServers(b)
	ga, _ := a.GetServer("gnopls")
	gb, _ := b.GetServer("gnopls")
	ga.FileTypes[0] = "changed"
	require.Equal(t, "gno", gb.FileTypes[0])
	require.Equal(t, "gno", extraLSPServers["gnopls"].FileTypes[0])
}
