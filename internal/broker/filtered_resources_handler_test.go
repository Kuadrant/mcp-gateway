package broker

import (
	"log/slog"
	"net/http"
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
)

func createResourceFilterTestManager(t *testing.T, serverName, prefix string, resources []mcp.Resource) *upstream.MCPManager {
	t.Helper()
	mcpServer := upstream.NewUpstreamMCP(&config.MCPServer{
		Name:   serverName,
		Prefix: prefix,
		URL:    "http://test.local/mcp",
	})
	manager, _ := upstream.NewUpstreamMCPManager(mcpServer, newMockGateway(), nil, nil, slog.Default(), 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	manager.SetResourcesForTesting(resources)
	return manager
}

func TestFilterResources(t *testing.T) {
	testCases := []struct {
		Name                 string
		FullResourceList     *mcp.ListResourcesResult
		RegisteredMCPServers map[config.UpstreamMCPID]upstream.ActiveMCPServer
		enforceFilterList    bool
		ExpectedCount        int
	}{
		{
			Name: "returns all resources when no headers and enforce is false",
			FullResourceList: &mcp.ListResourcesResult{Resources: []mcp.Resource{
				{URI: "test://r1", Name: "r1"},
				{URI: "test://r2", Name: "r2"},
			}},
			RegisteredMCPServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
			enforceFilterList:    false,
			ExpectedCount:        2,
		},
		{
			Name: "returns empty resources when no headers and enforce is true",
			FullResourceList: &mcp.ListResourcesResult{Resources: []mcp.Resource{
				{URI: "test://r1", Name: "r1"},
			}},
			RegisteredMCPServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
			enforceFilterList:    true,
			ExpectedCount:        0,
		},
		{
			Name:                 "returns empty slice for nil resources input",
			FullResourceList:     &mcp.ListResourcesResult{Resources: nil},
			RegisteredMCPServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
			enforceFilterList:    false,
			ExpectedCount:        0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			mcpBroker := &mcpBrokerImpl{
				enforceCapabilityFilter: tc.enforceFilterList,
				trustedHeadersPublicKey: testPublicKey,
				logger:                  slog.Default(),
				mcpServers:              tc.RegisteredMCPServers,
			}
			request := &mcp.ListResourcesRequest{Header: http.Header{}}
			mcpBroker.FilterResources(t.Context(), 1, request, tc.FullResourceList)
			if len(tc.FullResourceList.Resources) != tc.ExpectedCount {
				t.Fatalf("expected %d resources but got %d", tc.ExpectedCount, len(tc.FullResourceList.Resources))
			}
		})
	}
}

func TestFilterResources_JWTFiltering(t *testing.T) {
	jwt := createTestJWTWithCapabilities(t, map[string]map[string][]string{
		"resources": {"mcp-test/test-server1": {"test://r1"}},
	})

	mcpBroker := &mcpBrokerImpl{
		enforceCapabilityFilter: true,
		trustedHeadersPublicKey: testPublicKey,
		logger:                  slog.Default(),
		mcpServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"mcp-test/test-server1:test_:http://test.local/mcp": upstream.NewActiveForTesting(createResourceFilterTestManager(t,
				"mcp-test/test-server1",
				"test_",
				[]mcp.Resource{{URI: "test://r1", Name: "r1"}, {URI: "test://r2", Name: "r2"}},
			)),
		},
	}

	result := &mcp.ListResourcesResult{Resources: []mcp.Resource{
		{URI: "test://r1", Name: "r1"},
		{URI: "test://r2", Name: "r2"},
	}}
	request := &mcp.ListResourcesRequest{
		Header: http.Header{
			authorizedCapabilitiesHeader: {jwt},
		},
	}

	mcpBroker.FilterResources(t.Context(), 1, request, result)

	if len(result.Resources) != 1 {
		t.Fatalf("expected 1 resource but got %d", len(result.Resources))
	}
	if result.Resources[0].URI != "test://r1" {
		t.Fatalf("expected test://r1 but got %s", result.Resources[0].URI)
	}
}

func TestVirtualServerResourceFiltering(t *testing.T) {
	testCases := []struct {
		Name            string
		InputResources  *mcp.ListResourcesResult
		VirtualServers  map[string]*config.VirtualServer
		VirtualServerID string
		ExpectedURIs    []string
	}{
		{
			Name: "filters resources to virtual server subset",
			InputResources: &mcp.ListResourcesResult{Resources: []mcp.Resource{
				{URI: "test://r1", Name: "r1"},
				{URI: "test://r2", Name: "r2"},
				{URI: "test://r3", Name: "r3"},
			}},
			VirtualServers: map[string]*config.VirtualServer{
				"mcp-test/my-vs": {
					Name:      "mcp-test/my-vs",
					Resources: []string{"test://r1", "test://r3"},
				},
			},
			VirtualServerID: "mcp-test/my-vs",
			ExpectedURIs:    []string{"test://r1", "test://r3"},
		},
		{
			Name: "returns all resources when virtual server has empty resources list",
			InputResources: &mcp.ListResourcesResult{Resources: []mcp.Resource{
				{URI: "test://r1", Name: "r1"},
				{URI: "test://r2", Name: "r2"},
			}},
			VirtualServers: map[string]*config.VirtualServer{
				"mcp-test/my-vs": {
					Name:      "mcp-test/my-vs",
					Resources: []string{},
				},
			},
			VirtualServerID: "mcp-test/my-vs",
			ExpectedURIs:    []string{"test://r1", "test://r2"},
		},
		{
			Name: "returns all resources when no virtual server header",
			InputResources: &mcp.ListResourcesResult{Resources: []mcp.Resource{
				{URI: "test://r1", Name: "r1"},
			}},
			VirtualServers:  map[string]*config.VirtualServer{},
			VirtualServerID: "",
			ExpectedURIs:    []string{"test://r1"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			mcpBroker := &mcpBrokerImpl{
				enforceCapabilityFilter: false,
				virtualServers:          tc.VirtualServers,
				logger:                  slog.Default(),
			}

			request := &mcp.ListResourcesRequest{Header: http.Header{}}
			if tc.VirtualServerID != "" {
				request.Header[virtualMCPHeader] = []string{tc.VirtualServerID}
			}

			mcpBroker.FilterResources(t.Context(), 1, request, tc.InputResources)

			if len(tc.InputResources.Resources) != len(tc.ExpectedURIs) {
				t.Fatalf("expected %d resources but got %d: %v", len(tc.ExpectedURIs), len(tc.InputResources.Resources), tc.InputResources.Resources)
			}

			resultURIs := make(map[string]bool, len(tc.InputResources.Resources))
			for _, r := range tc.InputResources.Resources {
				resultURIs[r.URI] = true
			}
			for _, expectedURI := range tc.ExpectedURIs {
				if !resultURIs[expectedURI] {
					t.Fatalf("expected resource URI %s not found in result set", expectedURI)
				}
			}
		})
	}
}
