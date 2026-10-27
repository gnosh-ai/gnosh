package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShellConfigHostAdd(t *testing.T) {
	store := loadCrushSh(t, `host add render --ssh root@203.0.113.10 --description "renders the videos"
host add vlm --ssh vlm-box`)

	hosts := store.Config().Hosts
	require.Equal(t, "root@203.0.113.10", hosts["render"].SSH)
	require.Equal(t, "renders the videos", hosts["render"].Description)
	require.Equal(t, "vlm-box", hosts["vlm"].SSH)
}

func TestShellConfigHostRemove(t *testing.T) {
	store := loadCrushSh(t, `host add keep --ssh root@203.0.113.10
host add drop --ssh root@203.0.113.11
host remove drop`)

	hosts := store.Config().Hosts
	require.Contains(t, hosts, "keep")
	require.NotContains(t, hosts, "drop")
}

func TestShellConfigHostIsValidated(t *testing.T) {
	_, err := loadCrushShErr(t, `host add render --ssh "-oProxyCommand=evil"`)
	require.Error(t, err)
	require.Contains(t, err.Error(), `host "render"`)
}

func TestShellConfigHostNeedsSSH(t *testing.T) {
	_, err := loadCrushShErr(t, `host add render --description "no way in"`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ssh is required")
}
