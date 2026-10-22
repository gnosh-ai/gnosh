package config

import powernapConfig "github.com/charmbracelet/x/powernap/pkg/config"

// extraLSPServers are language servers powernap's catalog does not know.
// They get the catalog's treatment: an `lsp add gnopls` with no other flags
// is filled in, and the LSP manager starts one on its own when its binary
// is installed and a root marker is in the project.
var extraLSPServers = map[string]*powernapConfig.ServerConfig{
	// gnopls is the Gno language server (github.com/gnoverse/gnopls, a
	// gopls fork; `go install github.com/gnoverse/gnopls@latest`). A Gno
	// package declares itself in gnomod.toml (older ones in gno.mod);
	// gnowork.toml marks a workspace of several.
	"gnopls": {
		Command:     "gnopls",
		FileTypes:   []string{"gno"},
		RootMarkers: []string{"gnomod.toml", "gnowork.toml", "gno.mod"},
	},
}

// AddExtraLSPServers registers on m the servers powernap's catalog lacks,
// leaving alone any m already has.
func AddExtraLSPServers(m *powernapConfig.Manager) {
	for name, server := range extraLSPServers {
		if _, ok := m.GetServer(name); ok {
			continue
		}
		own := *server
		own.FileTypes = append([]string(nil), server.FileTypes...)
		own.RootMarkers = append([]string(nil), server.RootMarkers...)
		m.AddServer(name, &own)
	}
}
