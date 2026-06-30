package broker

import (
	"context"
	"slices"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	listTagsName          = "list_tags"
	filterToolsByTagsName = "filter_tools_by_tags"
)

func (m *mcpBrokerImpl) registerTagsTools() {
	listTags := mcp.NewTool(listTagsName,
		mcp.WithDescription("List all tags across registered MCP servers"),
	)
	listTags.Meta = mcp.NewMetaFromMap(map[string]any{
		brokerToolMetaKey: true,
		"kuadrant/id":     brokerServerID,
	})

	filterByTags := mcp.NewTool(filterToolsByTagsName,
		mcp.WithDescription("Return tools available through the gateway that match all of the given tags"),
		mcp.WithArray("tags",
			mcp.Description("list of tags to filter by (must not be empty)"),
			mcp.Required(),
		),
	)
	filterByTags.Meta = mcp.NewMetaFromMap(map[string]any{
		brokerToolMetaKey: true,
		"kuadrant/id":     brokerServerID,
	})

	m.listeningMCPServer.AddTool(
		listTags,
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return m.handleListTags(req)
		},
	)

	m.listeningMCPServer.AddTool(
		filterByTags,
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return m.handleFilterToolsByTags(req)
		},
	)
}

func (m *mcpBrokerImpl) handleListTags(req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	m.mcpLock.RLock()
	visible := m.getVisibleToolNames(req.Header)
	seen := make(map[string]struct{})
	for _, mgr := range m.mcpServers {
		cfg := mgr.Config()
		if len(m.visibleToolNames(cfg.Prefix, mgr, visible)) == 0 {
			continue
		}
		for _, tag := range cfg.Tags {
			seen[tag] = struct{}{}
		}
	}
	m.mcpLock.RUnlock()

	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	slices.Sort(tags)

	return m.marshalToolResult(tags), nil
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

	if len(rawSlice) == 0 {
		return mcp.NewToolResultError("tags must not be empty"), nil
	}

	filterTags := make([]string, 0, len(rawSlice))
	for _, v := range rawSlice {
		s, ok := v.(string)
		if !ok {
			return mcp.NewToolResultError("tags must be an array of strings"), nil
		}
		if s == "" {
			return mcp.NewToolResultError("tags must not contain empty strings"), nil
		}
		filterTags = append(filterTags, s)
	}

	type serverRef struct {
		tags   []string
		prefix string
		server upstream.ActiveMCPServer
	}
	m.mcpLock.RLock()
	visible := m.getVisibleToolNames(req.Header)
	refs := make([]serverRef, 0, len(m.mcpServers))
	for _, mgr := range m.mcpServers {
		cfg := mgr.Config()
		refs = append(refs, serverRef{
			tags:   cfg.Tags,
			prefix: cfg.Prefix,
			server: mgr,
		})
	}
	m.mcpLock.RUnlock()

	matched := make([]mcp.Tool, 0)
	for _, ref := range refs {
		if !hasAllTags(ref.tags, filterTags) {
			continue
		}
		for _, tool := range ref.server.GetManagedTools() {
			t := tool
			t.Name = ref.prefix + t.Name
			if _, ok := visible[t.Name]; !ok {
				continue
			}
			matched = append(matched, t)
		}
	}

	return m.marshalToolResult(matched), nil
}

// syncTagsTools registers or deregisters list_tags/filter_tools_by_tags based on
// whether any server in the current config has tags.
func (m *mcpBrokerImpl) syncTagsTools(ctx context.Context, servers []*config.MCPServer) {
	hasTags := false
	for _, s := range servers {
		if len(s.Tags) > 0 {
			hasTags = true
			break
		}
	}

	if hasTags && !m.tagsToolsRegistered.Load() {
		m.logger.InfoContext(ctx, "registering tags tools")
		m.registerTagsTools()
		m.tagsToolsRegistered.Store(true)
	} else if !hasTags && m.tagsToolsRegistered.Load() {
		m.logger.InfoContext(ctx, "deregistering tags tools")
		m.listeningMCPServer.DeleteTools(listTagsName, filterToolsByTagsName)
		m.tagsToolsRegistered.Store(false)
	}
}

func hasAllTags(serverTags, required []string) bool {
	for _, r := range required {
		if !slices.Contains(serverTags, r) {
			return false
		}
	}
	return true
}
