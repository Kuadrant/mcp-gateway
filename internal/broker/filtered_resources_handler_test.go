package broker

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func createResourceTestManager(t *testing.T, serverName, prefix string, resources []mcp.Resource) *upstream.MCPManager {
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
		ExpectedResources    []string
	}{
		{
			Name: "returns all resources when no headers and enforce is false",
			FullResourceList: &mcp.ListResourcesResult{Resources: []mcp.Resource{
				{URI: "test_resource1"},
				{URI: "test_resource2"},
			}},
			RegisteredMCPServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
			enforceFilterList:    false,
			ExpectedResources:    []string{"test_resource1", "test_resource2"},
		},
		{
			Name: "returns empty resources when no headers and enforce is true",
			FullResourceList: &mcp.ListResourcesResult{Resources: []mcp.Resource{
				{URI: "test_resource1"},
			}},
			RegisteredMCPServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
			enforceFilterList:    true,
			ExpectedResources:    []string{},
		},
		{
			Name:                 "returns empty slice for nil resources input",
			FullResourceList:     &mcp.ListResourcesResult{Resources: nil},
			RegisteredMCPServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
			enforceFilterList:    false,
			ExpectedResources:    []string{},
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
			mcpBroker.FilterResources(context.TODO(), 1, request, tc.FullResourceList)

			if len(tc.ExpectedResources) != len(tc.FullResourceList.Resources) {
				t.Fatalf("expected %d resources but got %d: %v", len(tc.ExpectedResources), len(tc.FullResourceList.Resources), tc.FullResourceList.Resources)
			}
		})
	}
}

func TestFilterResources_JWTFiltering(t *testing.T) {
	jwtToken := createTestJWTWithCapabilities(t, map[string]map[string][]string{
		"resources": {"mcp-test/test-server1": {"file://settings"}},
	})

	mcpBroker := &mcpBrokerImpl{
		enforceCapabilityFilter: true,
		trustedHeadersPublicKey: testPublicKey,
		logger:                  slog.Default(),
		mcpServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"mcp-test/test-server1:test_:http://test.local/mcp": upstream.NewActiveForTesting(createResourceTestManager(t,
				"mcp-test/test-server1",
				"test_",
				[]mcp.Resource{{URI: "file://settings"}},
			)),
		},
	}

	result := &mcp.ListResourcesResult{Resources: []mcp.Resource{
		{URI: "test_file://settings"},
		{URI: "test_file://other"},
	}}

	request := &mcp.ListResourcesRequest{Header: http.Header{
		"X-Mcp-Authorized": []string{jwtToken},
	}}

	mcpBroker.FilterResources(context.TODO(), 1, request, result)

	require.Len(t, result.Resources, 1)
	require.Equal(t, "test_file://settings", result.Resources[0].URI)
}

func TestFilterResources_VirtualServerFiltering(t *testing.T) {
	mcpBroker := &mcpBrokerImpl{
		enforceCapabilityFilter: false,
		logger:                  slog.Default(),
		virtualServers: map[string]*config.VirtualServer{
			"mcp-test/my-virtual-server": {
				Name:      "mcp-test/my-virtual-server",
				Resources: []string{"test_file://settings"},
			},
		},
	}

	result := &mcp.ListResourcesResult{Resources: []mcp.Resource{
		{URI: "test_file://settings"},
		{URI: "test_file://other"},
	}}

	request := &mcp.ListResourcesRequest{Header: http.Header{
		"X-Mcp-Virtualserver": []string{"mcp-test/my-virtual-server"},
	}}

	mcpBroker.FilterResources(context.TODO(), 1, request, result)

	require.Len(t, result.Resources, 1)
	require.Equal(t, "test_file://settings", result.Resources[0].URI)
}
