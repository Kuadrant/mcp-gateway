package broker

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/mark3labs/mcp-go/mcp"
)

func (m *mcpBrokerImpl) registerTagsTools() {
	m.listeningMCPServer.AddTool(
		mcp.NewTool("list_tags",
			mcp.WithDescription("List all tags across registered MCP servers"),
		),
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return m.handleListTags()
		},
	)

	m.listeningMCPServer.AddTool(
		mcp.NewTool("filter_tools_by_tags",
			mcp.WithDescription("Return tools available through the gateway that match all of the given tags"),
			mcp.WithArray("tags",
				mcp.Description("list of tags to filter by"),
				mcp.Required(),
			),
		),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return m.handleFilterToolsByTags(req)
		},
	)
}

func (m *mcpBrokerImpl) handleListTags() (*mcp.CallToolResult, error) {
	m.mcpLock.RLock()
	serverTags := make([][]string, 0, len(m.mcpServers))
	for _, mgr := range m.mcpServers {
		serverTags = append(serverTags, mgr.MCP.GetConfig().Tags)
	}
	m.mcpLock.RUnlock()

	seen := map[string]struct{}{}
	var tags []string
	for _, tagsForServer := range serverTags {
		for _, tag := range tagsForServer {
			if _, ok := seen[tag]; !ok {
				seen[tag] = struct{}{}
				tags = append(tags, tag)
			}
		}
	}
	slices.Sort(tags)

	b, err := json.Marshal(tags)
	if err != nil {
		return mcp.NewToolResultError("failed to marshal tags"), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

func (m *mcpBrokerImpl) handleFilterToolsByTags(req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	rawTags, ok := args["tags"]
	if !ok {
		return mcp.NewToolResultError("missing required parameter: tags"), nil
	}

	rawSlice, ok := rawTags.([]any)
	if !ok {
		return mcp.NewToolResultError("tags must be an array"), nil
	}

	filterTags := make([]string, 0, len(rawSlice))
	for _, v := range rawSlice {
		s, ok := v.(string)
		if !ok {
			return mcp.NewToolResultError("tags must be an array of strings"), nil
		}
		filterTags = append(filterTags, s)
	}

	type serverSnapshot struct {
		tags      []string
		prefix    string
		tools     []mcp.Tool
	}
	m.mcpLock.RLock()
	snapshots := make([]serverSnapshot, 0, len(m.mcpServers))
	for _, mgr := range m.mcpServers {
		cfg := mgr.MCP.GetConfig()
		snapshots = append(snapshots, serverSnapshot{
			tags:   cfg.Tags,
			prefix: cfg.ToolPrefix,
			tools:  mgr.GetManagedTools(),
		})
	}
	m.mcpLock.RUnlock()

	var matched []mcp.Tool
	for _, snap := range snapshots {
		if !hasAllTags(snap.tags, filterTags) {
			continue
		}
		for _, tool := range snap.tools {
			t := tool
			t.Name = snap.prefix + t.Name
			matched = append(matched, t)
		}
	}

	b, err := json.Marshal(matched)
	if err != nil {
		return mcp.NewToolResultError("failed to marshal tools"), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

// hasAllTags returns true if serverTags contains every tag in required.
func hasAllTags(serverTags, required []string) bool {
	for _, r := range required {
		if !slices.Contains(serverTags, r) {
			return false
		}
	}
	return true
}
