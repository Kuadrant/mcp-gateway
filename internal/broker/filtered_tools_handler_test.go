package broker

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	testPublicKey = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE7WdMdvC8hviEAL4wcebqaYbLEtVO
VEiyi/nozagw7BaWXmzbOWyy95gZLirTkhUb1P4Z4lgKLU2rD5NCbGPHAA==
-----END PUBLIC KEY-----`
)

func createTestJWT(t *testing.T, allowedTools map[string][]string) string {
	t.Helper()
	claimPayload, _ := json.Marshal(allowedTools)
	block, _ := pem.Decode([]byte(`-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIEY3QeiP9B9Bm3NHG3SgyiDHcbckwsGsQLKgv4fJxjJWoAoGCCqGSM49
AwEHoUQDQgAE7WdMdvC8hviEAL4wcebqaYbLEtVOVEiyi/nozagw7BaWXmzbOWyy
95gZLirTkhUb1P4Z4lgKLU2rD5NCbGPHAA==
-----END EC PRIVATE KEY-----`))
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"allowed-tools": string(claimPayload)})
	parsedKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("error parsing key for jwt %s", err)
	}
	jwtToken, err := token.SignedString(parsedKey)
	if err != nil {
		t.Fatalf("error signing jwt %s", err)
	}
	return jwtToken
}

// createTestManager creates a test MCPManager with pre-populated tools
func createTestManager(t *testing.T, serverName, toolPrefix string, tools []mcp.Tool) *upstream.MCPManager {
	t.Helper()
	mcpServer := upstream.NewUpstreamMCP(&config.MCPServer{
		Name:       serverName,
		ToolPrefix: toolPrefix,
		URL:        "http://test.local/mcp",
	})
	manager := upstream.NewUpstreamMCPManager(mcpServer, nil, slog.Default(), 0)
	// populate tools directly for testing (this requires accessing internal state)
	manager.SetToolsForTesting(tools)
	return manager
}

func TestFilteredTools(t *testing.T) {

	testCases := []struct {
		Name                 string
		FullToolList         *mcp.ListToolsResult
		AllowedToolsList     map[string][]string
		RegisteredMCPServers map[config.UpstreamMCPID]*upstream.MCPManager
		enforceFilterList    bool
		ExpectedTools        []mcp.Tool
	}{
		{
			Name: "test filters tools as expected",
			FullToolList: &mcp.ListToolsResult{Tools: []mcp.Tool{
				{Name: "test_tool"},
				{Name: "test_tool2"},
			}},
			RegisteredMCPServers: map[config.UpstreamMCPID]*upstream.MCPManager{
				"mcp-test/test-server1:test_:http://test.local/mcp": createTestManager(t,
					"mcp-test/test-server1",
					"test_",
					[]mcp.Tool{{Name: "tool"}, {Name: "tool2"}},
				),
			},
			AllowedToolsList: map[string][]string{
				"mcp-test/test-server1": {"tool"},
			},
			enforceFilterList: true,
			ExpectedTools: []mcp.Tool{
				{Name: "test_tool"},
			},
		},
		{
			Name: "test filters tools with same tool name as expected",
			FullToolList: &mcp.ListToolsResult{Tools: []mcp.Tool{
				{Name: "test1_tool"},
				{Name: "test2_tool"},
			}},
			RegisteredMCPServers: map[config.UpstreamMCPID]*upstream.MCPManager{
				"mcp-test/test-server1:test1_:http://test.local/mcp": createTestManager(t,
					"mcp-test/test-server1",
					"test1_",
					[]mcp.Tool{{Name: "tool"}, {Name: "tool2"}},
				),
				"mcp-test/test-server2:test2_:http://test.local/mcp": createTestManager(t,
					"mcp-test/test-server2",
					"test2_",
					[]mcp.Tool{{Name: "tool"}, {Name: "tool2"}},
				),
			},
			AllowedToolsList: map[string][]string{
				"mcp-test/test-server1": {"tool"},
				"mcp-test/test-server2": {"tool"},
			},
			enforceFilterList: true,
			ExpectedTools: []mcp.Tool{
				{Name: "test1_tool"},
				{Name: "test2_tool"},
			},
		},
		{
			Name: "test filters tools returns no tools if none allowed",
			FullToolList: &mcp.ListToolsResult{Tools: []mcp.Tool{
				{Name: "test1_tool"},
				{Name: "test2_tool"},
			}},
			RegisteredMCPServers: map[config.UpstreamMCPID]*upstream.MCPManager{
				"mcp-test/test-server1:test1_:http://test.local/mcp": createTestManager(t,
					"mcp-test/test-server1",
					"test1_",
					[]mcp.Tool{{Name: "tool"}, {Name: "tool2"}},
				),
				"mcp-test/test-server2:test2_:http://test.local/mcp": createTestManager(t,
					"mcp-test/test-server2",
					"test2_",
					[]mcp.Tool{{Name: "tool"}, {Name: "tool2"}},
				),
			},
			AllowedToolsList:  map[string][]string{},
			enforceFilterList: true,
			ExpectedTools:     []mcp.Tool{},
		},
		{
			Name: "test filters tools returns all tools enforce tool filter set to false",
			FullToolList: &mcp.ListToolsResult{Tools: []mcp.Tool{
				{Name: "test1_tool"},
				{Name: "test1_tool2"},
			}},
			RegisteredMCPServers: map[config.UpstreamMCPID]*upstream.MCPManager{
				"mcp-test/test-server1:test1_:http://test.local/mcp": createTestManager(t,
					"mcp-test/test-server1",
					"test1_",
					[]mcp.Tool{{Name: "tool"}, {Name: "tool2"}},
				),
			},
			AllowedToolsList:  nil,
			enforceFilterList: false,
			ExpectedTools: []mcp.Tool{
				{Name: "test1_tool"},
				{Name: "test1_tool2"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			mcpBroker := &mcpBrokerImpl{
				enforceToolFilter:       tc.enforceFilterList,
				trustedHeadersPublicKey: testPublicKey,
				logger:                  slog.Default(),
				mcpServers:              tc.RegisteredMCPServers,
			}

			request := &mcp.ListToolsRequest{}
			if tc.AllowedToolsList != nil {
				headerValue := createTestJWT(t, tc.AllowedToolsList)
				request.Header = http.Header{
					authorizedToolsHeader: {headerValue},
				}
			}
			mcpBroker.FilterTools(context.TODO(), 1, request, tc.FullToolList)

			if len(tc.ExpectedTools) != len(tc.FullToolList.Tools) {
				t.Fatalf("expected %d tools but got %d: %v", len(tc.ExpectedTools), len(tc.FullToolList.Tools), tc.FullToolList.Tools)
			}

			for _, exp := range tc.ExpectedTools {
				found := false
				for _, actual := range tc.FullToolList.Tools {
					if exp.Name == actual.Name {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected to find tool %s but it was not in returned tools %v", exp.Name, tc.FullToolList.Tools)
				}
			}
		})
	}
}

func TestVirtualServerFiltering(t *testing.T) {
	testCases := []struct {
		Name            string
		InputTools      *mcp.ListToolsResult
		VirtualServers  map[string]*config.VirtualServer
		VirtualServerID string
		ExpectedTools   []string
	}{
		{
			Name: "filters tools to virtual server subset",
			InputTools: &mcp.ListToolsResult{Tools: []mcp.Tool{
				{Name: "server1_tool1"},
				{Name: "server1_tool2"},
				{Name: "server2_tool1"},
			}},
			VirtualServers: map[string]*config.VirtualServer{
				"mcp-test/my-virtual-server": {
					Name:  "mcp-test/my-virtual-server",
					Tools: []string{"server1_tool1", "server2_tool1"},
				},
			},
			VirtualServerID: "mcp-test/my-virtual-server",
			ExpectedTools:   []string{"server1_tool1", "server2_tool1"},
		},
		{
			Name: "returns empty when virtual server has no matching tools",
			InputTools: &mcp.ListToolsResult{Tools: []mcp.Tool{
				{Name: "server1_tool1"},
				{Name: "server1_tool2"},
			}},
			VirtualServers: map[string]*config.VirtualServer{
				"mcp-test/empty-vs": {
					Name:  "mcp-test/empty-vs",
					Tools: []string{"nonexistent_tool"},
				},
			},
			VirtualServerID: "mcp-test/empty-vs",
			ExpectedTools:   []string{},
		},
		{
			Name: "returns all tools when virtual server not found",
			InputTools: &mcp.ListToolsResult{Tools: []mcp.Tool{
				{Name: "server1_tool1"},
			}},
			VirtualServers:  map[string]*config.VirtualServer{},
			VirtualServerID: "mcp-test/nonexistent",
			ExpectedTools:   []string{"server1_tool1"}, // returns original tools when VS not found
		},
		{
			Name: "returns all tools when no virtual server header",
			InputTools: &mcp.ListToolsResult{Tools: []mcp.Tool{
				{Name: "server1_tool1"},
				{Name: "server1_tool2"},
			}},
			VirtualServers: map[string]*config.VirtualServer{
				"mcp-test/my-vs": {
					Name:  "mcp-test/my-vs",
					Tools: []string{"server1_tool1"},
				},
			},
			VirtualServerID: "", // no header
			ExpectedTools:   []string{"server1_tool1", "server1_tool2"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			mcpBroker := &mcpBrokerImpl{
				enforceToolFilter: false,
				virtualServers:    tc.VirtualServers,
				logger:            slog.Default(),
			}

			request := &mcp.ListToolsRequest{Header: http.Header{}}
			if tc.VirtualServerID != "" {
				request.Header[virtualMCPHeader] = []string{tc.VirtualServerID}
			}

			mcpBroker.FilterTools(context.TODO(), 1, request, tc.InputTools)

			if len(tc.ExpectedTools) != len(tc.InputTools.Tools) {
				t.Fatalf("expected %d tools but got %d: %v", len(tc.ExpectedTools), len(tc.InputTools.Tools), tc.InputTools.Tools)
			}

			for _, expectedName := range tc.ExpectedTools {
				found := false
				for _, tool := range tc.InputTools.Tools {
					if tool.Name == expectedName {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected tool %s not found in %v", expectedName, tc.InputTools.Tools)
				}
			}
		})
	}
}

func TestFilterToolsSerializesAsEmptyArray(t *testing.T) {
	mcpBroker := &mcpBrokerImpl{
		enforceToolFilter: true, // will return empty when no header
		logger:            slog.Default(),
	}

	// nil tools input
	result := &mcp.ListToolsResult{Tools: nil}
	request := &mcp.ListToolsRequest{Header: http.Header{}}

	mcpBroker.FilterTools(context.TODO(), 1, request, result)

	// tools should be non-nil empty slice
	if result.Tools == nil {
		t.Fatal("expected Tools to be non-nil empty slice, got nil")
	}
	if len(result.Tools) != 0 {
		t.Fatalf("expected 0 tools, got %d", len(result.Tools))
	}

	// verify JSON serialization produces [] not null
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	if string(data) == `{"tools":null}` {
		t.Fatal("tools serialized as null, expected empty array")
	}
	// should contain "tools":[]
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	tools, ok := parsed["tools"].([]any)
	if !ok {
		t.Fatalf("tools is not an array: %T", parsed["tools"])
	}
	if len(tools) != 0 {
		t.Fatalf("expected empty array, got %v", tools)
	}
}

func TestCombinedAuthorizedToolsAndVirtualServer(t *testing.T) {
	testCases := []struct {
		Name             string
		MCPServers       map[config.UpstreamMCPID]*upstream.MCPManager
		VirtualServers   map[string]*config.VirtualServer
		AllowedToolsList map[string][]string
		VirtualServerID  string
		ExpectedTools    []string
	}{
		{
			Name: "x-authorized-tools filtered first then virtual server filters further",
			MCPServers: map[config.UpstreamMCPID]*upstream.MCPManager{
				"mcp-test/server1:s1_:http://test.local/mcp": createTestManager(t,
					"mcp-test/server1",
					"s1_",
					[]mcp.Tool{{Name: "tool1"}, {Name: "tool2"}, {Name: "tool3"}},
				),
			},
			VirtualServers: map[string]*config.VirtualServer{
				"mcp-test/my-vs": {
					Name:  "mcp-test/my-vs",
					Tools: []string{"s1_tool1", "s1_tool3"}, // only allow tool1 and tool3
				},
			},
			AllowedToolsList: map[string][]string{
				"mcp-test/server1": {"tool1", "tool2"}, // JWT allows tool1 and tool2
			},
			VirtualServerID: "mcp-test/my-vs",
			// JWT allows: s1_tool1, s1_tool2
			// Virtual server allows: s1_tool1, s1_tool3
			// Intersection: s1_tool1
			ExpectedTools: []string{"s1_tool1"},
		},
		{
			Name: "x-authorized-tools only when no virtual server header",
			MCPServers: map[config.UpstreamMCPID]*upstream.MCPManager{
				"mcp-test/server1:s1_:http://test.local/mcp": createTestManager(t,
					"mcp-test/server1",
					"s1_",
					[]mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
				),
			},
			VirtualServers: map[string]*config.VirtualServer{
				"mcp-test/my-vs": {
					Name:  "mcp-test/my-vs",
					Tools: []string{"s1_tool1"},
				},
			},
			AllowedToolsList: map[string][]string{
				"mcp-test/server1": {"tool1", "tool2"},
			},
			VirtualServerID: "", // no virtual server header
			ExpectedTools:   []string{"s1_tool1", "s1_tool2"},
		},
		{
			Name: "virtual server only when no x-authorized-tools header",
			MCPServers: map[config.UpstreamMCPID]*upstream.MCPManager{
				"mcp-test/server1:s1_:http://test.local/mcp": createTestManager(t,
					"mcp-test/server1",
					"s1_",
					[]mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
				),
			},
			VirtualServers: map[string]*config.VirtualServer{
				"mcp-test/my-vs": {
					Name:  "mcp-test/my-vs",
					Tools: []string{"s1_tool1"},
				},
			},
			AllowedToolsList: nil, // no JWT header
			VirtualServerID:  "mcp-test/my-vs",
			ExpectedTools:    []string{"s1_tool1"},
		},
		{
			Name: "empty result when filters have no intersection",
			MCPServers: map[config.UpstreamMCPID]*upstream.MCPManager{
				"mcp-test/server1:s1_:http://test.local/mcp": createTestManager(t,
					"mcp-test/server1",
					"s1_",
					[]mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
				),
			},
			VirtualServers: map[string]*config.VirtualServer{
				"mcp-test/my-vs": {
					Name:  "mcp-test/my-vs",
					Tools: []string{"s1_tool2"}, // only allows tool2
				},
			},
			AllowedToolsList: map[string][]string{
				"mcp-test/server1": {"tool1"}, // JWT only allows tool1
			},
			VirtualServerID: "mcp-test/my-vs",
			// JWT allows: s1_tool1
			// Virtual server allows: s1_tool2
			// Intersection: empty
			ExpectedTools: []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			mcpBroker := &mcpBrokerImpl{
				enforceToolFilter:       false,
				trustedHeadersPublicKey: testPublicKey,
				mcpServers:              tc.MCPServers,
				virtualServers:          tc.VirtualServers,
				logger:                  slog.Default(),
			}

			// build input tools from all registered servers
			inputTools := &mcp.ListToolsResult{Tools: []mcp.Tool{}}
			for _, manager := range tc.MCPServers {
				for _, tool := range manager.GetManagedTools() {
					inputTools.Tools = append(inputTools.Tools, mcp.Tool{
						Name: manager.MCP.GetPrefix() + tool.Name,
					})
				}
			}

			request := &mcp.ListToolsRequest{Header: http.Header{}}
			if tc.AllowedToolsList != nil {
				request.Header[authorizedToolsHeader] = []string{createTestJWT(t, tc.AllowedToolsList)}
			}
			if tc.VirtualServerID != "" {
				request.Header[virtualMCPHeader] = []string{tc.VirtualServerID}
			}

			mcpBroker.FilterTools(context.TODO(), 1, request, inputTools)

			if len(tc.ExpectedTools) != len(inputTools.Tools) {
				t.Fatalf("expected %d tools but got %d: %v", len(tc.ExpectedTools), len(inputTools.Tools), inputTools.Tools)
			}

			for _, expectedName := range tc.ExpectedTools {
				found := false
				for _, tool := range inputTools.Tools {
					if tool.Name == expectedName {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected tool %s not found in %v", expectedName, inputTools.Tools)
				}
			}
		})
	}
}
