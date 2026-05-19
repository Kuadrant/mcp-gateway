package broker

import (
	"context"
	"net/http"
	"slices"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/mark3labs/mcp-go/mcp"
)

// FilterResources reduces the resource set based on authorization headers.
// Priority: x-mcp-authorized JWT filtering, then x-mcp-virtualserver filtering.
func (broker *mcpBrokerImpl) FilterResources(_ context.Context, _ any, mcpReq *mcp.ListResourcesRequest, mcpRes *mcp.ListResourcesResult) {
	broker.logger.Debug("FilterResources called", "input_resources_count", len(mcpRes.Resources))
	resources := mcpRes.Resources
	emptyResources := []mcp.Resource{}
	if len(mcpRes.Resources) == 0 {
		mcpRes.Resources = emptyResources
		return
	}

	// step 1: apply x-mcp-authorized filtering (JWT-based)
	resources = broker.applyAuthorizedResourcesFilter(mcpReq.Header, resources)
	broker.logger.Debug("FilterResources authorized capabilities result", "output_resources_count", len(resources))

	// step 2: apply virtual server filtering
	resources = broker.applyVirtualServerResourcesFilter(mcpReq.Header, resources)
	// filter out any gateway specific meta data we are storing internally before sending to clients
	resources = broker.removeGatewayMetaResources(resources)
	broker.logger.Debug("FilterResources virtual server result", "output_resources_count", len(resources))

	// ensure we never return nil (would serialize as null instead of [])
	if resources == nil {
		resources = emptyResources
	}
	mcpRes.Resources = resources
}

func (broker *mcpBrokerImpl) removeGatewayMetaResources(resources []mcp.Resource) []mcp.Resource {
	broker.logger.Debug("removing gateway specific meta from resources")
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

// applyAuthorizedResourcesFilter filters resources based on x-mcp-authorized JWT header.
// Returns original resources if header not present and enforcement is off.
// Returns empty slice if header validation fails or enforcement is on without header.
func (broker *mcpBrokerImpl) applyAuthorizedResourcesFilter(headers http.Header, resources []mcp.Resource) []mcp.Resource {
	headerValues, present := headers[authorizedCapabilitiesHeader]

	if !present {
		broker.logger.Debug("no x-mcp-authorized header for resources", "enforced", broker.enforceCapabilityFilter)
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
		broker.logger.Debug("no resources key in capabilities")
		if broker.enforceCapabilityFilter {
			return []mcp.Resource{}
		}
		return resources
	}

	return broker.filterResourcesByServerMap(allowedResources)
}

// filterResourcesByServerMap filters resources based on a map of server name to allowed resource URIs.
func (broker *mcpBrokerImpl) filterResourcesByServerMap(allowedResources map[string][]string) []mcp.Resource {
	var filtered []mcp.Resource

	for serverName, resourceURIs := range allowedResources {
		upstreamServer := broker.findServerByName(serverName)
		if upstreamServer == nil {
			broker.logger.Error("upstream not found", "server", serverName)
			continue
		}
		resList := upstreamServer.GetManagedResources()
		if resList == nil {
			broker.logger.Debug("no resources registered for upstream server", "server", upstreamServer.MCPName)
			continue
		}

		for _, res := range resList {
			broker.logger.Debug("checking access for resource", "uri", res.URI, "against", resourceURIs)
			if slices.Contains(resourceURIs, res.URI) {
				broker.logger.Debug("access granted for resource", "uri", res.URI)
				res.URI = upstream.PrefixURI(res.URI, upstreamServer.Config().Prefix)
				filtered = append(filtered, res)
			}
		}
	}

	return filtered
}

// applyVirtualServerResourcesFilter filters resources to only those specified in the virtual server.
func (broker *mcpBrokerImpl) applyVirtualServerResourcesFilter(headers http.Header, resources []mcp.Resource) []mcp.Resource {
	headerValues, ok := headers[virtualMCPHeader]
	if !ok || len(headerValues) != 1 {
		return resources
	}

	virtualServerID := headerValues[0]
	broker.logger.Debug("applying virtual server filter to resources", "virtualServer", virtualServerID)

	vs, err := broker.GetVirtualSeverByHeader(virtualServerID)
	if err != nil {
		broker.logger.Error("failed to get virtual server for resources", "error", err)
		return resources
	}

	// build a set of allowed resource URIs for O(1) lookup
	filteredSet := make(map[string]struct{}, len(vs.Resources))
	for _, name := range vs.Resources {
		filteredSet[name] = struct{}{}
	}

	var filtered []mcp.Resource
	for _, res := range resources {
		if _, inFilter := filteredSet[res.URI]; inFilter {
			filtered = append(filtered, res)
		}
	}

	return filtered
}
