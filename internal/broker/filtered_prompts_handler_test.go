package broker

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func createPromptTestManager(t *testing.T, serverName, prefix string, prompts []mcp.Prompt) *upstream.MCPManager {
	t.Helper()
	mcpServer := upstream.NewUpstreamMCP(&config.MCPServer{
		Name:   serverName,
		Prefix: prefix,
		URL:    "http://test.local/mcp",
	}, "", nil)
	manager, _ := upstream.NewUpstreamMCPManager(mcpServer, newMockGateway(), nil, slog.Default(), 0, mcpv1.InvalidToolPolicyFilterOut)
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
			FullPromptList: &mcp.ListPromptsResult{Prompts: []*mcp.Prompt{
				{Name: "test_prompt1"},
				{Name: "test_prompt2"},
			}},
			RegisteredMCPServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{},
			enforceFilterList:    false,
			ExpectedPrompts:      []string{"test_prompt1", "test_prompt2"},
		},
		{
			Name: "returns empty prompts when no headers and enforce is true",
			FullPromptList: &mcp.ListPromptsResult{Prompts: []*mcp.Prompt{
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

			headers := http.Header{}
			mcpBroker.FilterPrompts(context.TODO(), headers, tc.FullPromptList)

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

	result := &mcp.ListPromptsResult{Prompts: []*mcp.Prompt{
		{Name: "test_prompt1"},
		{Name: "test_prompt2"},
	}}
	headers := http.Header{
		authorizedCapabilitiesHeader: {jwt},
	}

	mcpBroker.FilterPrompts(context.TODO(), headers, result)

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

	result := &mcp.ListPromptsResult{Prompts: []*mcp.Prompt{
		{Name: "test_prompt1"},
	}}
	headers := http.Header{
		authorizedCapabilitiesHeader: {jwt},
	}

	mcpBroker.FilterPrompts(context.TODO(), headers, result)

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
			InputPrompts: &mcp.ListPromptsResult{Prompts: []*mcp.Prompt{
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
			InputPrompts: &mcp.ListPromptsResult{Prompts: []*mcp.Prompt{
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
			InputPrompts: &mcp.ListPromptsResult{Prompts: []*mcp.Prompt{
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

			headers := http.Header{}
			if tc.VirtualServerID != "" {
				headers[virtualMCPHeader] = []string{tc.VirtualServerID}
			}

			mcpBroker.FilterPrompts(context.TODO(), headers, tc.InputPrompts)

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

// regression for the SDK migration: prompts/list results hold pointers to
// the server's stored Prompt objects. deleting from the shared Meta map
// permanently stripped kuadrant/id and raced with concurrent lists.
func TestRemoveGatewayMetaFromPrompts_DoesNotMutateSharedPrompts(t *testing.T) {
	b := &mcpBrokerImpl{logger: slog.Default()}

	stored := &mcp.Prompt{
		Name: "srv_prompt",
		Meta: mcp.Meta{"kuadrant/id": "ns/server", "keep": "me"},
	}

	for range 2 {
		res := []*mcp.Prompt{stored}
		cleaned := b.removeGatewayMetaFromPrompts(res)

		require.Len(t, cleaned, 1)
		require.NotContains(t, cleaned[0].Meta, "kuadrant/id")
		require.Equal(t, "me", cleaned[0].Meta["keep"])

		require.Equal(t, "ns/server", stored.Meta["kuadrant/id"])
	}
}

// concurrent lists share the stored Prompt pointers; the previous in-place
// delete() was a concurrent map write under -race.
func TestRemoveGatewayMetaFromPrompts_ConcurrentLists(t *testing.T) {
	b := &mcpBrokerImpl{logger: slog.Default()}

	stored := make([]*mcp.Prompt, 0, 10)
	for i := range 10 {
		stored = append(stored, &mcp.Prompt{
			Name: fmt.Sprintf("prompt_%d", i),
			Meta: mcp.Meta{"kuadrant/id": "ns/server"},
		})
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := make([]*mcp.Prompt, len(stored))
			copy(res, stored)
			b.removeGatewayMetaFromPrompts(res)
		}()
	}
	wg.Wait()

	for _, p := range stored {
		require.Equal(t, "ns/server", p.Meta["kuadrant/id"])
	}
}
