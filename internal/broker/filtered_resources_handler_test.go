package broker

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestApplyAuthorizedCapabilitiesFilterForResourcesPerServer_NoHeaderAllowsAll(t *testing.T) {
	broker := &mcpBrokerImpl{
		logger:                  slog.Default(),
		enforceCapabilityFilter: false,
	}

	resources := []*mcp.Resource{
		{URI: "ui://secure_resource1.html", Name: "resource1"},
		{URI: "ui://secure_resource2.html", Name: "resource2"},
	}

	result := broker.applyAuthorizedCapabilitiesFilterForResourcesPerServer(nil, "testserver", "secure_", resources)
	require.Equal(t, len(resources), len(result), "should allow all resources when no header present")
}

func TestApplyAuthorizedCapabilitiesFilterForResourcesPerServer_NoHeaderEnforcesDeniesAll(t *testing.T) {
	broker := &mcpBrokerImpl{
		logger:                  slog.Default(),
		enforceCapabilityFilter: true,
	}

	resources := []*mcp.Resource{
		{URI: "ui://secure_resource1.html", Name: "resource1"},
		{URI: "ui://secure_resource2.html", Name: "resource2"},
	}

	result := broker.applyAuthorizedCapabilitiesFilterForResourcesPerServer(nil, "testserver", "secure_", resources)
	require.Empty(t, result, "should deny all resources when enforce enabled and no header")
}

func TestApplyAuthorizedCapabilitiesFilterForResourcesPerServer_InvalidJWTDeniesAll(t *testing.T) {
	broker := &mcpBrokerImpl{
		logger:                  slog.Default(),
		enforceCapabilityFilter: false,
	}

	// Create headers with invalid JWT
	headers := http.Header{}
	headers.Set("x-mcp-authorized", "invalid-jwt-token")

	resources := []*mcp.Resource{
		{URI: "ui://secure_resource1.html", Name: "resource1"},
	}

	result := broker.applyAuthorizedCapabilitiesFilterForResourcesPerServer(headers, "testserver", "secure_", resources)
	require.Empty(t, result, "should deny all resources with invalid JWT")
}

func TestStripResourcePrefixForFiltering_UIScheme(t *testing.T) {
	tests := []struct {
		name     string
		uri      string
		prefix   string
		expected string
	}{
		{
			name:     "strip prefix from ui:// URI",
			uri:      "ui://docs_readme.md",
			prefix:   "docs_",
			expected: "ui://readme.md",
		},
		{
			name:     "no prefix in URI returns unchanged",
			uri:      "ui://readme.md",
			prefix:   "app_",
			expected: "ui://readme.md",
		},
		{
			name:     "empty prefix returns unchanged",
			uri:      "ui://docs_readme.md",
			prefix:   "",
			expected: "ui://docs_readme.md",
		},
		{
			name:     "non-ui scheme returns unchanged",
			uri:      "file://docs_readme.md",
			prefix:   "docs_",
			expected: "file://docs_readme.md",
		},
		{
			name:     "longest prefix match case",
			uri:      "ui://app_admin_resource.html",
			prefix:   "app_admin_",
			expected: "ui://resource.html",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripResourcePrefixForFiltering(tt.uri, tt.prefix)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestContains(t *testing.T) {
	tests := []struct {
		name     string
		slice    []string
		item     string
		expected bool
	}{
		{
			name:     "item present",
			slice:    []string{"a", "b", "c"},
			item:     "b",
			expected: true,
		},
		{
			name:     "item not present",
			slice:    []string{"a", "b", "c"},
			item:     "d",
			expected: false,
		},
		{
			name:     "empty slice",
			slice:    []string{},
			item:     "a",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := contains(tt.slice, tt.item)
			require.Equal(t, tt.expected, result)
		})
	}
}
