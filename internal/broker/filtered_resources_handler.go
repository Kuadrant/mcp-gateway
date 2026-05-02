package broker

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
)

// FilterResources is the resources/list counterpart to FilterTools. JWT/virtual-server
// filtering for resources is intentionally deferred to a follow-up PR (see
// docs/design/resource-federation.md, Non-Goals); this handler currently only strips
// the gateway-internal "kuadrant/id" meta marker before the result reaches the client,
// so the wire format matches what an upstream MCP server would have produced directly.
func (broker *mcpBrokerImpl) FilterResources(_ context.Context, _ any, _ *mcp.ListResourcesRequest, mcpRes *mcp.ListResourcesResult) {
	if mcpRes == nil {
		return
	}
	if mcpRes.Resources == nil {
		mcpRes.Resources = []mcp.Resource{}
		return
	}
	mcpRes.Resources = broker.removeGatewayResourceMeta(mcpRes.Resources)
}

// FilterResourceTemplates mirrors FilterResources for resources/templates/list.
func (broker *mcpBrokerImpl) FilterResourceTemplates(_ context.Context, _ any, _ *mcp.ListResourceTemplatesRequest, mcpRes *mcp.ListResourceTemplatesResult) {
	if mcpRes == nil {
		return
	}
	if mcpRes.ResourceTemplates == nil {
		mcpRes.ResourceTemplates = []mcp.ResourceTemplate{}
		return
	}
	mcpRes.ResourceTemplates = broker.removeGatewayResourceTemplateMeta(mcpRes.ResourceTemplates)
}

func (broker *mcpBrokerImpl) removeGatewayResourceMeta(resources []mcp.Resource) []mcp.Resource {
	for i := range resources {
		if resources[i].Meta != nil {
			delete(resources[i].Meta.AdditionalFields, "kuadrant/id")
		}
	}
	return resources
}

func (broker *mcpBrokerImpl) removeGatewayResourceTemplateMeta(templates []mcp.ResourceTemplate) []mcp.ResourceTemplate {
	for i := range templates {
		if templates[i].Meta != nil {
			delete(templates[i].Meta.AdditionalFields, "kuadrant/id")
		}
	}
	return templates
}
