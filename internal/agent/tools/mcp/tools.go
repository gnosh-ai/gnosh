package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Tool = mcp.Tool

// ToolResult represents the result of running an MCP tool.
type ToolResult struct {
	Type      string
	Content   string
	Data      []byte
	MediaType string
	// ImagePath is where the image in Data was saved, or "" when it was
	// not, so a model that cannot see it can be told where it is.
	ImagePath string
}

var allTools = csync.NewMap[string, []*Tool]()

// Tools returns all available MCP tools.
func Tools() iter.Seq2[string, []*Tool] {
	return allTools.Seq2()
}

// GetTools returns the tool list for a single MCP server by name, or
// ok=false if the server is not registered.
func GetTools(name string) ([]*Tool, bool) {
	return allTools.Get(name)
}

// RunTool runs an MCP tool with the given input parameters.
func RunTool(ctx context.Context, cfg *config.ConfigStore, name, toolName string, input string) (ToolResult, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return ToolResult{}, fmt.Errorf("error parsing parameters: %s", err)
	}

	c, err := getOrRenewClient(ctx, cfg, name)
	if err != nil {
		return ToolResult{}, err
	}
	result, err := c.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return ToolResult{}, err
	}

	return toolResultFromCall(result, name, cfg.Config().Options.DataDirectory), nil
}

// toolResultFromCall folds the content blocks of server's MCP reply into
// the one result Crush carries, with at most one image, and its text cut to
// what it may bring into the conversation (capOutput). An embedded resource
// and a resource link come in Claude Code's words, and binary content is
// saved to a file under dataDir: a binary resource, audio, and every image
// but the one shown, which is saved there too and named in the text
// ("[Image: source: PATH]"). An empty image or audio is no media at all.
func toolResultFromCall(result *mcp.CallToolResult, server, dataDir string) ToolResult {
	if len(result.Content) == 0 {
		return ToolResult{Type: "text", Content: ""}
	}

	var textParts []string
	var image shownImage
	for _, v := range result.Content {
		switch content := v.(type) {
		case *mcp.TextContent:
			textParts = append(textParts, content.Text)
		case *mcp.ImageContent:
			if text, saved := image.showOrSave(content.Data, content.MIMEType, fmt.Sprintf("[Image from %s]", server), dataDir); saved {
				textParts = append(textParts, text)
			}
		case *mcp.AudioContent:
			// Claude Code saves audio rather than send it: most models
			// take none in a tool's result.
			if raw := ensureRawBytes(content.Data); len(raw) > 0 {
				textParts = append(textParts, savedBinary(raw, content.MIMEType, fmt.Sprintf("[Audio from %s]", server), dataDir))
			}
		case *mcp.EmbeddedResource:
			if text, ok := embeddedResource(content.Resource, server, dataDir, &image); ok {
				textParts = append(textParts, text)
			}
		case *mcp.ResourceLink:
			link := fmt.Sprintf("[Resource link: %s] %s", content.Name, content.URI)
			if content.Description != "" {
				link += fmt.Sprintf(" (%s)", content.Description)
			}
			textParts = append(textParts, link)
		default:
			textParts = append(textParts, fmt.Sprintf("%v", v))
		}
	}
	textContent := capOutput(strings.Join(textParts, "\n"))
	if len(image.data) == 0 {
		return ToolResult{Type: "text", Content: textContent}
	}

	var imagePath string
	if path, err := saveBlob(image.data, image.mimeType, dataDir); err == nil {
		imagePath = path
		textContent = strings.TrimPrefix(textContent+"\n[Image: source: "+path+"]", "\n")
	} else {
		slog.Debug("Could not save an MCP tool's image", "server", server, "error", err)
	}
	return ToolResult{Type: "image", Content: textContent, Data: image.data, MediaType: image.mimeType, ImagePath: imagePath}
}

// embeddedResource is what the model is told of a resource embedded in
// server's MCP reply, in Claude Code's words: its text after "[Resource
// from SERVER at URI] ", or, when it is binary, that heading with the
// reply's image (image.showOrSave), or the file it was saved to under
// dataDir. ok is false for one with neither text nor binary. %v printed
// these as Go values, pointers and all.
func embeddedResource(r *mcp.ResourceContents, server, dataDir string, image *shownImage) (text string, ok bool) {
	if r == nil {
		return "", false
	}
	heading := fmt.Sprintf("[Resource from %s at %s]", server, r.URI)
	switch {
	case r.Text != "":
		return heading + " " + r.Text, true
	case len(r.Blob) == 0:
		return "", false
	}
	if text, saved := image.showOrSave(r.Blob, r.MIMEType, heading, dataDir); saved {
		return text, true
	}
	return heading, true
}

// shownImage is the image an MCP reply shows the model, decoded to raw
// bytes: the first of a type a model is shown.
type shownImage struct {
	data     []byte
	mimeType string
}

// showOrSave takes an image to be the reply's, when it is the first one of
// a type a model is shown (shownImageType), and otherwise saves it under
// dataDir and says where, after heading, as Claude Code saves an image of
// another type. saved is false when it was taken, and for an empty image,
// which is neither.
func (m *shownImage) showOrSave(data []byte, mimeType, heading, dataDir string) (text string, saved bool) {
	raw := ensureRawBytes(data)
	if len(raw) == 0 {
		return "", false
	}
	if shown := shownImageType(raw, mimeType); shown != "" && len(m.data) == 0 {
		*m = shownImage{data: raw, mimeType: shown}
		return "", false
	}
	return savedBinary(raw, mimeType, heading, dataDir), true
}

// RefreshTools gets the updated list of tools from the MCP and updates the
// global state.
func RefreshTools(ctx context.Context, cfg *config.ConfigStore, name string) {
	// Serialize with session renewal so the registered session can't be
	// swapped between the Get and the state update below — a stale error
	// transition would otherwise tear down the healthy replacement.
	mu := renewLock(name)
	mu.Lock()
	defer mu.Unlock()

	session, ok := sessions.Get(name)
	if !ok {
		slog.Warn("Refresh tools: no session", "name", name)
		return
	}

	tools, err := getTools(ctx, session)
	if err != nil {
		updateState(name, StateError, err, session, Counts{})
		return
	}

	toolCount := updateTools(cfg, name, tools)

	prev, _ := states.Get(name)
	prev.Counts.Tools = toolCount
	updateState(name, StateConnected, nil, session, prev.Counts)
}

// registerSessionTools lists the tools a live session exposes and writes them
// into the shared registry, returning the number registered after any
// configured allow/deny filtering. It is the single seam through which a
// (re)connected session's tools enter the registry, so both the initial
// connect and a lazy renew repopulate the tool list the agent sends to the LLM
// instead of leaving it empty.
func registerSessionTools(ctx context.Context, cfg *config.ConfigStore, name string, sess *ClientSession) (int, error) {
	tools, err := getTools(ctx, sess)
	if err != nil {
		return 0, err
	}
	return updateTools(cfg, name, tools), nil
}

func getTools(ctx context.Context, session *ClientSession) ([]*Tool, error) {
	// Always call ListTools to get the actual available tools.
	// The InitializeResult Capabilities.Tools field may be an empty object {},
	// which is valid per MCP spec, but we still need to call ListTools to discover tools.
	result, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	return result.Tools, nil
}

func updateTools(cfg *config.ConfigStore, name string, tools []*Tool) int {
	mcpCfg, ok := cfg.Config().MCP[name]
	if ok {
		tools = filterTools(mcpCfg, tools)
	}
	if len(tools) == 0 {
		allTools.Del(name)
		return 0
	}
	allTools.Set(name, tools)
	return len(tools)
}

// filterTools filters tools based on enabled_tools (allow list) and
// disabled_tools (deny list) from the MCP config.
func filterTools(mcpCfg config.MCPConfig, tools []*Tool) []*Tool {
	if len(mcpCfg.EnabledTools) > 0 {
		filtered := make([]*Tool, 0, len(mcpCfg.EnabledTools))
		for _, tool := range tools {
			if slices.Contains(mcpCfg.EnabledTools, tool.Name) {
				filtered = append(filtered, tool)
			}
		}
		tools = filtered
	}

	if len(mcpCfg.DisabledTools) > 0 {
		filtered := make([]*Tool, 0, len(tools))
		for _, tool := range tools {
			if !slices.Contains(mcpCfg.DisabledTools, tool.Name) {
				filtered = append(filtered, tool)
			}
		}
		tools = filtered
	}

	return tools
}

// ensureRawBytes normalizes MCP media data into raw binary bytes.
//
// The MCP Go SDK's json.Unmarshal normally base64-decodes
// ImageContent.Data into raw bytes automatically. However, some MCP
// transports (notably Docker over stdio) can deliver data in
// unexpected formats. This function handles both cases:
//
//   - If data looks like a valid base64 string (ASCII-only, decodable)
//     it is decoded and the raw bytes are returned.
//   - If data is already raw binary (contains bytes > 127) it is
//     returned as-is.
func ensureRawBytes(data []byte) []byte {
	if len(data) == 0 {
		return data
	}

	normalized := normalizeBase64Input(data)
	if decoded, ok := decodeBase64(normalized); ok {
		return decoded
	}

	// Already raw binary — return unchanged.
	return data
}

func normalizeBase64Input(data []byte) []byte {
	normalized := strings.Join(strings.Fields(string(data)), "")
	return []byte(normalized)
}

func decodeBase64(data []byte) ([]byte, bool) {
	if len(data) == 0 {
		return data, true
	}

	for _, b := range data {
		if b > 127 {
			return nil, false
		}
	}

	s := string(data)
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err == nil {
		return decoded, true
	}
	decoded, err = base64.RawStdEncoding.DecodeString(s)
	if err == nil {
		return decoded, true
	}
	return nil, false
}
