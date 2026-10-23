package lsp

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/home"
	powernapconfig "github.com/charmbracelet/x/powernap/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestUnavailableBackoff(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 3, 26, 0, 0, 0, 0, time.UTC)
	now := base

	manager := &Manager{
		unavailable: csync.NewMap[string, time.Time](),
		now:         func() time.Time { return now },
	}

	require.False(t, manager.recentlyUnavailable("gopls"))

	manager.markUnavailable("gopls")
	require.True(t, manager.recentlyUnavailable("gopls"))

	now = now.Add(unavailableRetryDelay + time.Second)
	require.False(t, manager.recentlyUnavailable("gopls"))
	_, exists := manager.unavailable.Get("gopls")
	require.False(t, exists)

	manager.markUnavailable("gopls")
	manager.clearUnavailable("gopls")
	require.False(t, manager.recentlyUnavailable("gopls"))
}

func TestCanAutoStartFiltersBeforeLookingUpCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		server  *powernapconfig.ServerConfig
		want    bool
		lookups int
	}{
		{
			name: "unhandled file type",
			server: &powernapconfig.ServerConfig{
				Command:   "typescript-language-server",
				FileTypes: []string{"typescript"},
			},
		},
		{
			name: "generic command",
			server: &powernapconfig.ServerConfig{
				Command:   "node",
				FileTypes: []string{"go"},
			},
		},
		{
			name: "handled file type",
			server: &powernapconfig.ServerConfig{
				Command:   "gopls",
				FileTypes: []string{"go"},
			},
			want:    true,
			lookups: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lookups := 0
			manager := &Manager{
				unavailable: csync.NewMap[string, time.Time](),
				now:         time.Now,
				lookPath: func(string) (string, error) {
					lookups++
					return "/usr/local/bin/gopls", nil
				},
			}

			got := manager.canAutoStart("test", "main.go", t.TempDir(), tt.server)

			require.Equal(t, tt.want, got)
			require.Equal(t, tt.lookups, lookups)
		})
	}
}

func TestCanAutoStartCachesMissingCommand(t *testing.T) {
	t.Parallel()

	lookups := 0
	manager := &Manager{
		unavailable: csync.NewMap[string, time.Time](),
		now:         time.Now,
		lookPath: func(string) (string, error) {
			lookups++
			return "", errors.New("not found")
		},
	}
	server := &powernapconfig.ServerConfig{
		Command:   "gopls",
		FileTypes: []string{"go"},
	}

	require.False(t, manager.canAutoStart("gopls", "main.go", t.TempDir(), server))
	require.False(t, manager.canAutoStart("gopls", "main.go", t.TempDir(), server))
	require.Equal(t, 1, lookups)
}

// A server that fails to start must back off rather than be relaunched on
// every file event. Configured servers used to skip the back-off entirely,
// so working inside a repo whose crush.json configures gopls, on a machine
// without gopls, retried and failed on nearly every step.
func TestStartServerBacksOffAfterFailedStart(t *testing.T) {
	t.Parallel()

	const missing = "klash-test-no-such-lsp-binary"
	base := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	now := base
	cfg := &config.Config{
		Options: &config.Options{},
		LSP:     config.LSPs{"gopls": {Command: missing}},
	}
	m := &Manager{
		clients:     csync.NewMap[string, *Client](),
		unavailable: csync.NewMap[string, time.Time](),
		cfg:         config.NewTestStore(cfg),
		callback:    func(string, *Client) {},
		now:         func() time.Time { return now },
		lookPath:    exec.LookPath,
	}
	server := &powernapconfig.ServerConfig{Command: missing, FileTypes: []string{"go"}}
	require.True(t, m.isUserConfigured("gopls"), "this test is about the configured path")

	m.startServer("gopls", "main.go", server)
	first, ok := m.unavailable.Get("gopls")
	require.True(t, ok, "a server that failed to start must be marked unavailable")
	require.Equal(t, base, first)

	// Inside the back-off window, the next file event must not relaunch it.
	now = base.Add(time.Second)
	m.startServer("gopls", "main.go", server)
	again, _ := m.unavailable.Get("gopls")
	require.Equal(t, base, again, "no new attempt inside the back-off window")

	// After the window it is tried again and, still missing, marked again.
	now = base.Add(unavailableRetryDelay + time.Second)
	m.startServer("gopls", "main.go", server)
	later, ok := m.unavailable.Get("gopls")
	require.True(t, ok)
	require.Equal(t, now, later, "after the window the server is retried")

	_, stored := m.clients.Get("gopls")
	require.False(t, stored, "a server that never started must not be stored as a client")
}

// The auto-start path found the binary on PATH, so canAutoStart cleared
// the back-off; if it then failed to start, nothing marked it again. It
// must back off too.
func TestStartServerBacksOffWhenAutoStartedServerFails(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	m := &Manager{
		clients:     csync.NewMap[string, *Client](),
		unavailable: csync.NewMap[string, time.Time](),
		cfg:         config.NewTestStore(&config.Config{Options: &config.Options{}}),
		callback:    func(string, *Client) {},
		now:         func() time.Time { return base },
		// Claim it is installed, so the failure comes from the start itself.
		lookPath: func(string) (string, error) { return "/usr/bin/klash-test-no-such-lsp-binary", nil },
	}
	server := &powernapconfig.ServerConfig{Command: "klash-test-no-such-lsp-binary", FileTypes: []string{"go"}}

	m.startServer("fakels", "main.go", server)
	require.True(t, m.recentlyUnavailable("fakels"),
		"a server that passed the PATH check but failed to start must still back off")
}

// A configured server whose program is not installed is reported and not
// launched; after the back-off its program is looked for again, and once
// it is found, a later disappearance would be reported again.
func TestStartServerReportsAMissingConfiguredProgram(t *testing.T) {
	t.Parallel()

	const command = "klash-test-no-such-lsp-binary"
	base := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	now := base
	var installed bool
	var lookups int
	cfg := &config.Config{
		Options: &config.Options{},
		LSP:     config.LSPs{"gopls": {Command: command}},
	}
	m := &Manager{
		clients:     csync.NewMap[string, *Client](),
		unavailable: csync.NewMap[string, time.Time](),
		cfg:         config.NewTestStore(cfg),
		callback:    func(string, *Client) {},
		now:         func() time.Time { return now },
		lookPath: func(string) (string, error) {
			lookups++
			if installed {
				return "/usr/bin/" + command, nil
			}
			return "", exec.ErrNotFound
		},
	}
	server := &powernapconfig.ServerConfig{Command: command, FileTypes: []string{"go"}}

	m.startServer("gopls", "main.go", server)
	_, warned := m.missingWarned.Load("gopls")
	require.True(t, warned, "the missing program is reported")
	require.True(t, m.recentlyUnavailable("gopls"))
	_, stored := m.clients.Get("gopls")
	require.False(t, stored)

	now = base.Add(time.Second)
	m.startServer("gopls", "main.go", server)
	require.Equal(t, 1, lookups, "inside the back-off nothing is looked up or launched")

	now = base.Add(unavailableRetryDelay + time.Second)
	installed = true
	m.startServer("gopls", "main.go", server)
	require.Equal(t, 2, lookups, "after the back-off the program is looked for again")
	_, warned = m.missingWarned.Load("gopls")
	require.False(t, warned, "found, so a later disappearance is reported again")
}

// A configured command is looked up as the client will run it: with its
// variables expanded and a leading ~ for the home directory. Looked up as
// written, "~/go/bin/gopls" was reported missing and never launched.
func TestStartServerLooksUpTheConfiguredCommandAsItWillRun(t *testing.T) {
	t.Parallel()

	for command, want := range map[string]string{
		"~/go/bin/gopls":     filepath.Join(home.Dir(), "go", "bin", "gopls"),
		"$HOME/go/bin/gopls": filepath.Join(os.Getenv("HOME"), "go", "bin", "gopls"),
		"gopls":              "gopls",
	} {
		var looked string
		cfg := &config.Config{
			Options: &config.Options{},
			LSP:     config.LSPs{"gopls": {Command: command}},
		}
		m := &Manager{
			clients:     csync.NewMap[string, *Client](),
			unavailable: csync.NewMap[string, time.Time](),
			cfg:         config.NewTestStore(cfg),
			callback:    func(string, *Client) {},
			now:         time.Now,
			lookPath: func(name string) (string, error) {
				looked = name
				return "", exec.ErrNotFound
			},
		}
		m.startServer("gopls", "main.go", &powernapconfig.ServerConfig{Command: command, FileTypes: []string{"go"}})
		require.Equal(t, want, looked, command)
	}
}
