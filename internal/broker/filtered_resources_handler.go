package broker

import (
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// applyAuthorizedCapabilitiesFilterForResourcesPerServer filters resources for a specific server based on JWT authorization.
// Called per-server before merging results. Resources have prefix already injected; we strip it to match JWT claim.
func (broker *mcpBrokerImpl) applyAuthorizedCapabilitiesFilterForResourcesPerServer(headers http.Header, serverName, prefix string, resources []*mcp.Resource) []*mcp.Resource {
	if len(resources) == 0 {
		return resources
	}

	headerValues, present := headers[authorizedCapabilitiesHeader]
	if !present {
		if broker.enforceCapabilityFilter {
			return []*mcp.Resource{}
		}
		return resources
	}

	capabilities, err := broker.parseAuthorizedCapabilitiesJWT(headerValues)
	if err != nil {
		broker.logger.Error("failed to parse x-mcp-authorized header for resources", "error", err)
		return []*mcp.Resource{}
	}

	allowedResources, hasResources := capabilities["resources"]
	if !hasResources {
		if broker.enforceCapabilityFilter {
			return []*mcp.Resource{}
		}
		return resources
	}

	// Get allowed URIs for this specific server
	allowedURIs, hasServer := allowedResources[serverName]
	if !hasServer {
		if broker.enforceCapabilityFilter {
			return []*mcp.Resource{}
		}
		return resources
	}

	// Filter this server's resources against its allowed URI list
	// Strip prefix from each resource's URI to match against JWT claim
	var filtered []*mcp.Resource
	for _, resource := range resources {
		stripped := stripResourcePrefixForFiltering(resource.URI, prefix)
		if contains(allowedURIs, stripped) {
			filtered = append(filtered, resource)
		}
	}

	return filtered
}

// stripResourcePrefixForFiltering removes the server's prefix from a resource URI's authority segment.
// For ui://prefix_name, returns ui://name.
// Prefix already includes the separator (e.g., "docs_").
// If the URI doesn't start with the expected prefix, returns the URI unchanged.
func stripResourcePrefixForFiltering(uri, prefix string) string {
	if prefix == "" || !strings.HasPrefix(uri, "ui://") {
		return uri
	}

	scheme := "ui://"
	authority := uri[len(scheme):]

	// Check if authority starts with prefix (which includes separator)
	if !strings.HasPrefix(authority, prefix) {
		return uri
	}

	// Strip the prefix and reconstruct
	strippedAuthority := authority[len(prefix):]
	return scheme + strippedAuthority
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
