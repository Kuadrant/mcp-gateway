package broker

import (
	"context"
	"net/http"
	"slices"

	"github.com/mark3labs/mcp-go/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// FilterResources reduces the resource set based on authorization headers.
func (broker *mcpBrokerImpl) FilterResources(ctx context.Context, _ any, mcpReq *mcp.ListResourcesRequest, mcpRes *mcp.ListResourcesResult) {
	attrs := []attribute.KeyValue{brokerComponentAttr}
	if sid := sessionIDFromContext(ctx); sid != "" {
		attrs = append(attrs, attribute.String("mcp.session.id", sid))
	}
	ctx, span := brokerTracer().Start(ctx, "mcp-broker.resources-list", trace.WithAttributes(attrs...))
	defer span.End()

	broker.logger.DebugContext(ctx, "FilterResources called", "input_resources_count", len(mcpRes.Resources))
	resources := mcpRes.Resources
	emptyResources := []mcp.Resource{}
	if len(mcpRes.Resources) == 0 {
		mcpRes.Resources = emptyResources
		return
	}

	resources = broker.applyAuthorizedCapabilitiesFilterForResources(mcpReq.Header, resources)
	resources = broker.applyVirtualServerFilterForResources(mcpReq.Header, resources)
	resources = broker.removeGatewayMetaFromResources(resources)

	span.SetAttributes(attribute.Int("mcp.resources.count", len(resources)))

	if resources == nil {
		resources = emptyResources
	}
	mcpRes.Resources = resources
}

func (broker *mcpBrokerImpl) removeGatewayMetaFromResources(resources []mcp.Resource) []mcp.Resource {
	for i := range resources {
		if resources[i].Meta != nil {
			delete(resources[i].Meta.AdditionalFields, "kuadrant/id")
			if len(resources[i].Meta.AdditionalFields) == 0 {
				resources[i].Meta = nil
			}
		}
	}
	return resources
}

func (broker *mcpBrokerImpl) applyAuthorizedCapabilitiesFilterForResources(headers http.Header, resources []mcp.Resource) []mcp.Resource {
	headerValues, present := headers[authorizedCapabilitiesHeader]

	if !present {
		if broker.enforceCapabilityFilter {
			return []mcp.Resource{}
		}
		return resources
	}

	capabilities, err := broker.parseAuthorizedCapabilitiesJWT(headerValues)
	if err != nil {
		broker.logger.Error("failed to parse x-mcp-authorized header for resources", "error", err)
		return []mcp.Resource{}
	}

	allowedResources, hasResources := capabilities["resources"]
	if !hasResources {
		if broker.enforceCapabilityFilter {
			return []mcp.Resource{}
		}
		return resources
	}

	return broker.filterResourcesByServerMap(allowedResources)
}

func (broker *mcpBrokerImpl) filterResourcesByServerMap(allowedResources map[string][]string) []mcp.Resource {
	var filtered []mcp.Resource

	for serverName, uris := range allowedResources {
		upstream := broker.findServerByName(serverName)
		if upstream == nil {
			broker.logger.Error("upstream not found for resource filtering", "server", serverName)
			continue
		}
		resources := upstream.GetManagedResources()
		if resources == nil {
			continue
		}

		for _, resource := range resources {
			if slices.Contains(uris, resource.URI) {
				filtered = append(filtered, resource)
			}
		}
	}

	return filtered
}

func (broker *mcpBrokerImpl) applyVirtualServerFilterForResources(headers http.Header, resources []mcp.Resource) []mcp.Resource {
	headerValues, ok := headers[virtualMCPHeader]
	if !ok || len(headerValues) != 1 {
		return resources
	}

	virtualServerID := headerValues[0]
	vs, err := broker.GetVirtualSeverByHeader(virtualServerID)
	if err != nil {
		broker.logger.Error("failed to get virtual server for resource filtering", "error", err)
		return resources
	}

	if len(vs.Resources) == 0 {
		return resources
	}

	filteredSet := make(map[string]struct{}, len(vs.Resources))
	for _, uri := range vs.Resources {
		filteredSet[uri] = struct{}{}
	}

	var filtered []mcp.Resource
	for _, resource := range resources {
		if _, inFilter := filteredSet[resource.URI]; inFilter {
			filtered = append(filtered, resource)
		}
	}

	return filtered
}
