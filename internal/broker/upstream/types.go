package upstream

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// GatewayTool pairs a tool definition with the handler the gateway
// registers for it.
type GatewayTool struct {
	Tool    mcp.Tool
	Handler mcp.ToolHandler
}

// GatewayPrompt pairs a prompt definition with the handler the gateway
// registers for it.
type GatewayPrompt struct {
	Prompt  mcp.Prompt
	Handler mcp.PromptHandler
}

// ToolsAdderDeleter defines the interface for interacting with the gateway directly
type ToolsAdderDeleter interface {
	AddTools(tools ...GatewayTool)
	DeleteTools(tools ...string)
	ListTools() map[string]*GatewayTool
}

// PromptsAdderDeleter defines the interface for managing prompts on the gateway server
type PromptsAdderDeleter interface {
	AddPrompts(prompts ...GatewayPrompt)
	DeletePrompts(names ...string)
	ListPrompts() map[string]*GatewayPrompt
}

// NewToolResultError creates a CallToolResult with the error flag set
func NewToolResultError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}

// NewToolResultText creates a CallToolResult with text content
func NewToolResultText(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// NoopToolHandler is a placeholder handler for pass-through tools
func NoopToolHandler(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return NewToolResultError("Kagenti MCP Broker doesn't forward tool calls"), nil
}

// NoopPromptHandler is a placeholder handler for pass-through prompts
func NoopPromptHandler(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return &mcp.GetPromptResult{}, nil
}
