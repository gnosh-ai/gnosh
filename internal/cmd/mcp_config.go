package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/spf13/cobra"
)

// addMCPConfigFlags adds Claude Code's --mcp-config and
// --strict-mcp-config; read them with mcpConfigFlags.
func addMCPConfigFlags(cmd *cobra.Command) {
	cmd.Flags().StringArray("mcp-config", nil, `MCP servers to load, from a file in Claude Code's .mcp.json shape, {"mcpServers": {...}}, or that JSON itself (repeatable)`)
	cmd.Flags().Bool("strict-mcp-config", false, "Use only the MCP servers from --mcp-config, not those in the config files")
}

// mcpConfigFlags returns the MCP servers --mcp-config names, by name, and
// whether --strict-mcp-config asks for those alone. Each value is a file,
// or the JSON itself when it starts with "{", in Claude Code's .mcp.json
// shape. A server named twice takes its last definition. Something that is
// not that shape is an error, so a server meant to be there cannot go
// missing unnoticed.
func mcpConfigFlags(cmd *cobra.Command) (config.MCPs, bool, error) {
	values, err := cmd.Flags().GetStringArray("mcp-config")
	if err != nil {
		// The command does not take the flags.
		return nil, false, nil
	}
	strict, _ := cmd.Flags().GetBool("strict-mcp-config")
	if strict && len(values) == 0 {
		return nil, false, fmt.Errorf("--strict-mcp-config needs --mcp-config: it would leave no MCP servers")
	}
	servers := config.MCPs{}
	for _, value := range values {
		data := []byte(value)
		if !strings.HasPrefix(strings.TrimSpace(value), "{") {
			if data, err = os.ReadFile(value); err != nil {
				return nil, false, fmt.Errorf("--mcp-config %s: %w", value, err)
			}
		}
		if err := parseMCPServers(data, servers); err != nil {
			return nil, false, fmt.Errorf("--mcp-config %s: %w", value, err)
		}
	}
	return servers, strict, nil
}

// claudeMCPServer is a server in Claude Code's .mcp.json: a command for a
// stdio server, or a URL, with type "http" or "sse".
type claudeMCPServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// parseMCPServers adds to servers those of data, a .mcp.json document.
// A server without a type is stdio when it names a command and http when
// it names a URL; the types are Claude Code's and Gnosh's alike.
func parseMCPServers(data []byte, servers config.MCPs) error {
	var doc struct {
		MCPServers map[string]claudeMCPServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("not JSON in .mcp.json's shape: %w", err)
	}
	if doc.MCPServers == nil {
		return fmt.Errorf(`no "mcpServers" object, as in Claude Code's .mcp.json`)
	}
	for name, s := range doc.MCPServers {
		t := config.MCPType(s.Type)
		switch {
		case t == "" && s.Command != "":
			t = config.MCPStdio
		case t == "" && s.URL != "":
			t = config.MCPHttp
		}
		switch t {
		case config.MCPStdio:
			if s.Command == "" {
				return fmt.Errorf("server %q: a stdio server needs a command", name)
			}
		case config.MCPHttp, config.MCPSSE:
			if s.URL == "" {
				return fmt.Errorf("server %q: an %s server needs a url", name, t)
			}
		case "":
			return fmt.Errorf("server %q names neither a command nor a url", name)
		default:
			return fmt.Errorf("server %q: unknown type %q; expected stdio, http or sse", name, s.Type)
		}
		servers[name] = config.MCPConfig{
			Type:    t,
			Command: s.Command,
			Args:    s.Args,
			Env:     s.Env,
			URL:     s.URL,
			Headers: s.Headers,
		}
	}
	return nil
}
