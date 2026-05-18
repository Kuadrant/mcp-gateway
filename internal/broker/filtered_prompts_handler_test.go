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
)

func createPromptTestManager(t *testing.T, serverName, prefix string, prompts []mcp.Prompt) *upstream.MCPManager {
	t.Helper()
	mcpServer := upstream.NewUpstreamMCP(&config.MCPServer{
		Name:   serverName,
		Prefix: prefix,
		URL:    "http://test.local/mcp",
	})
	manager, _ := upstream.NewUpstreamMCPManager(mcpServer, newMockGateway(), nil, slog.Default(), 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	manager.SetPromptsForTesting(prompts)
	return manager
}

func TestFilterPrompts(t *testing.T) {
	testCases := []struct {
		Name                 string
		FullPromptList       *mcp.ListPromptsResult
		RegisteredMCPServers map[config.UpstreamMCPID]upstream.ActiveMCPServer
		enforceFilterList    bool
		ExpectedPrompts      []string
	}{
		{
			Name: "returns all prompts when no headers and enforce is false",
			FullPromptList: &mcp.ListPromptsResult{Prompts: []mcp.Prompt{
				{Name: "test_prompt1"},
				{Name: "test_prompt2"},
			}},
			RegisteredMCPServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
			enforceFilterList:    false,
			ExpectedPrompts:      []string{"test_prompt1", "test_prompt2"},
		},
		{
			Name: "returns empty prompts when no headers and enforce is true",
			FullPromptList: &mcp.ListPromptsResult{Prompts: []mcp.Prompt{
				{Name: "test_prompt1"},
			}},
			RegisteredMCPServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
			enforceFilterList:    true,
			ExpectedPrompts:      []string{},
		},
		{
			Name:                 "returns empty slice for nil prompts input",
			FullPromptList:       &mcp.ListPromptsResult{Prompts: nil},
			RegisteredMCPServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
			enforceFilterList:    false,
			ExpectedPrompts:      []string{},
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

			request := &mcp.ListPromptsRequest{Header: http.Header{}}
			mcpBroker.FilterPrompts(context.TODO(), 1, request, tc.FullPromptList)

			if len(tc.ExpectedPrompts) != len(tc.FullPromptList.Prompts) {
				t.Fatalf("expected %d prompts but got %d: %v", len(tc.ExpectedPrompts), len(tc.FullPromptList.Prompts), tc.FullPromptList.Prompts)
			}
		})
	}
}

func TestFilterPrompts_JWTFiltering(t *testing.T) {
	jwt := createTestJWTWithCapabilities(t, map[string]map[string][]string{
		"prompts": {"mcp-test/test-server1": {"prompt1"}},
	})

	mcpBroker := &mcpBrokerImpl{
		enforceCapabilityFilter: true,
		trustedHeadersPublicKey: testPublicKey,
		logger:                  slog.Default(),
		mcpServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"mcp-test/test-server1:test_:http://test.local/mcp": upstream.NewActiveForTesting(createPromptTestManager(t,
				"mcp-test/test-server1",
				"test_",
				[]mcp.Prompt{{Name: "prompt1"}, {Name: "prompt2"}},
			)),
		},
	}

	result := &mcp.ListPromptsResult{Prompts: []mcp.Prompt{
		{Name: "test_prompt1"},
		{Name: "test_prompt2"},
	}}
	request := &mcp.ListPromptsRequest{
		Header: http.Header{
			authorizedCapabilitiesHeader: {jwt},
		},
	}

	mcpBroker.FilterPrompts(context.TODO(), 1, request, result)

	if len(result.Prompts) != 1 {
		t.Fatalf("expected 1 prompt but got %d: %v", len(result.Prompts), result.Prompts)
	}
	if result.Prompts[0].Name != "test_prompt1" {
		t.Fatalf("expected test_prompt1 but got %s", result.Prompts[0].Name)
	}
}

func TestFilterPrompts_ToolsOnlyJWTReturnsAllPrompts(t *testing.T) {
	jwt := createTestJWTWithCapabilities(t, map[string]map[string][]string{
		"tools": {"mcp-test/test-server1": {"tool1"}},
	})

	mcpBroker := &mcpBrokerImpl{
		enforceCapabilityFilter: false,
		trustedHeadersPublicKey: testPublicKey,
		logger:                  slog.Default(),
		mcpServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"mcp-test/test-server1:test_:http://test.local/mcp": upstream.NewActiveForTesting(createPromptTestManager(t,
				"mcp-test/test-server1",
				"test_",
				[]mcp.Prompt{{Name: "prompt1"}},
			)),
		},
	}

	result := &mcp.ListPromptsResult{Prompts: []mcp.Prompt{
		{Name: "test_prompt1"},
	}}
	request := &mcp.ListPromptsRequest{
		Header: http.Header{
			authorizedCapabilitiesHeader: {jwt},
		},
	}

	mcpBroker.FilterPrompts(context.TODO(), 1, request, result)

	if len(result.Prompts) != 1 {
		t.Fatalf("expected 1 prompt but got %d", len(result.Prompts))
	}
}

func TestVirtualServerPromptFiltering(t *testing.T) {
	testCases := []struct {
		Name            string
		InputPrompts    *mcp.ListPromptsResult
		VirtualServers  map[string]*config.VirtualServer
		VirtualServerID string
		ExpectedPrompts []string
	}{
		{
			Name: "filters prompts to virtual server subset",
			InputPrompts: &mcp.ListPromptsResult{Prompts: []mcp.Prompt{
				{Name: "prompt1"},
				{Name: "prompt2"},
				{Name: "prompt3"},
			}},
			VirtualServers: map[string]*config.VirtualServer{
				"mcp-test/my-vs": {
					Name:    "mcp-test/my-vs",
					Prompts: []string{"prompt1", "prompt3"},
				},
			},
			VirtualServerID: "mcp-test/my-vs",
			ExpectedPrompts: []string{"prompt1", "prompt3"},
		},
		{
			Name: "returns all prompts when virtual server has empty prompts list",
			InputPrompts: &mcp.ListPromptsResult{Prompts: []mcp.Prompt{
				{Name: "prompt1"},
				{Name: "prompt2"},
			}},
			VirtualServers: map[string]*config.VirtualServer{
				"mcp-test/my-vs": {
					Name:    "mcp-test/my-vs",
					Tools:   []string{"tool1"},
					Prompts: []string{},
				},
			},
			VirtualServerID: "mcp-test/my-vs",
			ExpectedPrompts: []string{"prompt1", "prompt2"},
		},
		{
			Name: "returns all prompts when no virtual server header",
			InputPrompts: &mcp.ListPromptsResult{Prompts: []mcp.Prompt{
				{Name: "prompt1"},
			}},
			VirtualServers:  map[string]*config.VirtualServer{},
			VirtualServerID: "",
			ExpectedPrompts: []string{"prompt1"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			mcpBroker := &mcpBrokerImpl{
				enforceCapabilityFilter: false,
				virtualServers:          tc.VirtualServers,
				logger:                  slog.Default(),
			}

			request := &mcp.ListPromptsRequest{Header: http.Header{}}
			if tc.VirtualServerID != "" {
				request.Header[virtualMCPHeader] = []string{tc.VirtualServerID}
			}

			mcpBroker.FilterPrompts(context.TODO(), 1, request, tc.InputPrompts)

			resultPrompts := tc.InputPrompts.Prompts
			if len(tc.ExpectedPrompts) != len(resultPrompts) {
				t.Fatalf("expected %d prompts but got %d: %v", len(tc.ExpectedPrompts), len(resultPrompts), resultPrompts)
			}

			resultNames := make(map[string]bool, len(resultPrompts))
			for _, p := range resultPrompts {
				resultNames[p.Name] = true
			}
			for _, expectedName := range tc.ExpectedPrompts {
				if !resultNames[expectedName] {
					t.Fatalf("expected prompt %s not found in result set", expectedName)
				}
			}
		})
	}
}
