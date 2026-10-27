package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateHosts(t *testing.T) {
	t.Parallel()

	good := &Config{Hosts: Hosts{
		"render":  {SSH: "root@203.0.113.10"},
		"vlm.box": {SSH: "render-2", Description: "scores frames"},
		"v6":      {SSH: "admin@[2001:db8::1]"},
	}}
	require.NoError(t, good.ValidateHosts())

	for name, cfg := range map[string]*Config{
		"this machine's name": {Hosts: Hosts{"local": {SSH: "root@203.0.113.10"}}},
		"a name with a space": {Hosts: Hosts{"render box": {SSH: "root@203.0.113.10"}}},
		"no ssh":              {Hosts: Hosts{"render": {}}},
		"an option as ssh":    {Hosts: Hosts{"render": {SSH: "-oProxyCommand=evil"}}},
		"shell syntax as ssh": {Hosts: Hosts{"render": {SSH: "root@host; rm -rf /"}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := cfg.ValidateHosts()
			require.Error(t, err)
			require.Contains(t, err.Error(), `host "`)
		})
	}
}

func TestValidateRefusesABadHost(t *testing.T) {
	t.Parallel()

	cfg := &Config{Hosts: Hosts{"render": {SSH: "-oProxyCommand=evil"}}}
	cfg.setDefaults(t.TempDir(), t.TempDir())
	err := cfg.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid host configuration")
}

func TestHostsSortedAndNames(t *testing.T) {
	t.Parallel()

	h := Hosts{"vlm": {SSH: "b"}, "render": {SSH: "a"}}
	require.Equal(t, []string{"render", "vlm"}, h.Names())
	sorted := h.Sorted()
	require.Equal(t, "render", sorted[0].Name)
	require.Equal(t, "a", sorted[0].Host.SSH)
	require.Equal(t, "vlm", sorted[1].Name)
}

func TestLocalMachineIsUserAtHost(t *testing.T) {
	t.Parallel()

	m := LocalMachine()
	username, hostname, ok := strings.Cut(m, "@")
	require.True(t, ok, m)
	require.NotEmpty(t, username)
	require.NotEmpty(t, hostname)
	require.Equal(t, m, LocalMachine(), "computed once")
}
