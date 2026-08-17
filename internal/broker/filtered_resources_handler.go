package broker

import (
	"context"
	"net/http"
	"strings"

	"github.com/Kuadrant/mcp-gateway/internal/routing"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// FilterResources reduces the resource set based on authorization headers.
func (broker *mcpBrokerImpl) FilterResources(ctx context.Context, headers http.Header, mcpRes *mcp.ListResourcesResult) {
	attrs := []attribute.KeyValue{brokerComponentAttr}
	ctx, span := brokerTracer().Start(ctx, "mcp-broker.resources-filter", trace.WithAttributes(attrs...))
	defer span.End()

	broker.logger.DebugContext(ctx, "FilterResources called", "input_resources_count", len(mcpRes.Resources))
	resources := mcpRes.Resources
	if len(mcpRes.Resources) == 0 {
		mcpRes.Resources = []*mcp.Resource{}
		return
	}

	resources = broker.applyAuthorizedCapabilitiesFilterForResources(ctx, headers, resources)

	span.SetAttributes(attribute.Int("mcp.resources.count", len(resources)))

	if resources == nil {
		resources = []*mcp.Resource{}
	}
	mcpRes.Resources = resources
}

func (broker *mcpBrokerImpl) applyAuthorizedCapabilitiesFilterForResources(ctx context.Context, headers http.Header, resources []*mcp.Resource) []*mcp.Resource {
	headerValues, present := headers[authorizedCapabilitiesHeader]

	if !present {
		if broker.enforceCapabilityFilter {
			return []*mcp.Resource{}
		}
		return resources
	}

	capabilities, err := broker.parseAuthorizedCapabilitiesJWT(headerValues)
	if err != nil {
		broker.logger.ErrorContext(ctx, "failed to parse x-mcp-authorized header for resources", "error", err)
		return []*mcp.Resource{}
	}

	allowedResources, hasResources := capabilities["resources"]
	if !hasResources {
		if broker.enforceCapabilityFilter {
			return []*mcp.Resource{}
		}
		return resources
	}

	return broker.filterResourcesByServerMap(ctx, allowedResources, resources)
}

func (broker *mcpBrokerImpl) filterResourcesByServerMap(ctx context.Context, allowedResources map[string][]string, resources []*mcp.Resource) []*mcp.Resource {
	var filtered []*mcp.Resource

	for _, resource := range resources {
		if resource == nil {
			continue
		}

		serverInfo, err := broker.GetServerInfoByResource(resource.URI)
		if err != nil {
			broker.logger.DebugContext(ctx, "unable to determine server for resource, excluding", "uri", resource.URI, "error", err)
			continue
		}

		allowedAuthorities, hasServer := allowedResources[serverInfo.Name]
		if !hasServer {
			broker.logger.DebugContext(ctx, "server not in resources claim, excluding resource", "server", serverInfo.Name, "uri", resource.URI)
			continue
		}

		// extract original authority by stripping the prefix from the URI authority
		prefixedAuthority := routing.ResourceAuthority(resource.URI)
		originalAuthority := stripResourcePrefix(prefixedAuthority, serverInfo.Prefix)

		allowed := false
		for _, authority := range allowedAuthorities {
			if originalAuthority == authority {
				allowed = true
				break
			}
		}

		if allowed {
			filtered = append(filtered, resource)
		} else {
			broker.logger.DebugContext(ctx, "resource authority not in claim, excluding", "server", serverInfo.Name, "uri", resource.URI, "authority", originalAuthority)
		}
	}

	return filtered
}

// stripResourcePrefix removes the prefix and separator from a prefixed authority.
// For authority "app_example.com" with prefix "app", returns "example.com".
// For authority without matching prefix, returns the authority unchanged.
func stripResourcePrefix(authority, prefix string) string {
	if prefix == "" {
		return authority
	}
	separator := routing.EnsureSeparator(prefix)
	if strings.HasPrefix(authority, separator) {
		return authority[len(separator):]
	}
	return authority
}
