package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
)

// MockMCP implements the MCP interface for testing
type MockMCP struct {
	name            string
	prefix          string
	id              config.UpstreamMCPID
	cfg             *config.MCPServer
	connectErr      error
	pingErr         error
	tools           []mcp.Tool
	listToolsErr    error
	protocolVersion string
	hasToolsCap     bool
	connected       bool
}

func (m *MockMCP) GetName() string {
	return m.name
}

func (m *MockMCP) GetConfig() config.MCPServer {
	return *m.cfg
}

func (m *MockMCP) ID() config.UpstreamMCPID {
	return m.id
}

func (m *MockMCP) GetPrefix() string {
	return m.prefix
}

func (m *MockMCP) Connect(_ context.Context, onConnected func()) error {
	if m.connectErr != nil {
		return m.connectErr
	}
	m.connected = true
	if onConnected != nil {
		onConnected()
	}
	return nil
}

func (m *MockMCP) SupportsToolsListChanged() bool {
	return m.hasToolsCap
}

func (m *MockMCP) Disconnect() error {
	m.connected = false
	return nil
}

func (m *MockMCP) ListTools(_ context.Context, _ mcp.ListToolsRequest) (*mcp.ListToolsResult, error) {
	if m.listToolsErr != nil {
		return nil, m.listToolsErr
	}
	return &mcp.ListToolsResult{Tools: m.tools}, nil
}

func (m *MockMCP) OnNotification(_ func(notification mcp.JSONRPCNotification)) {}

func (m *MockMCP) OnConnectionLost(_ func(err error)) {}

func (m *MockMCP) Ping(_ context.Context) error {
	return m.pingErr
}

func (m *MockMCP) ProtocolInfo() *mcp.InitializeResult {
	result := &mcp.InitializeResult{
		ProtocolVersion: m.protocolVersion,
		Capabilities:    mcp.ServerCapabilities{},
	}
	if m.hasToolsCap {
		result.Capabilities.Tools = &struct {
			ListChanged bool `json:"listChanged,omitempty"`
		}{}
	}
	return result
}

// newMockMCP creates a MockMCP with sensible defaults for testing
func newMockMCP(name, prefix string) *MockMCP {
	id := config.UpstreamMCPID(fmt.Sprintf("%s:%s:http://mock/mcp", name, prefix))
	return &MockMCP{
		name:            name,
		prefix:          prefix,
		id:              id,
		cfg:             &config.MCPServer{Name: name, ToolPrefix: prefix, URL: "http://mock/mcp"},
		protocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		hasToolsCap:     true,
		tools:           []mcp.Tool{{Name: "mock_tool"}},
	}
}

// MockToolsAdderDeleter implements ToolsAdderDeleter for testing
type MockToolsAdderDeleter struct {
	tools    map[string]*server.ServerTool
	addCalls int
	delCalls int
}

func newMockToolsAdderDeleter() *MockToolsAdderDeleter {
	return &MockToolsAdderDeleter{
		tools: make(map[string]*server.ServerTool),
	}
}

func (m *MockToolsAdderDeleter) AddTools(tools ...server.ServerTool) {
	m.addCalls++
	for i := range tools {
		m.tools[tools[i].Tool.Name] = &tools[i]
	}
}

func (m *MockToolsAdderDeleter) DeleteTools(names ...string) {
	m.delCalls++
	for _, name := range names {
		delete(m.tools, name)
	}
}

func (m *MockToolsAdderDeleter) ListTools() map[string]*server.ServerTool {
	return m.tools
}

func TestNewUpstreamMCPManager(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	testCases := []struct {
		name             string
		interval         time.Duration
		expectedInterval time.Duration
	}{
		{
			name:             "uses default ticker interval when zero",
			interval:         0,
			expectedInterval: DefaultTickerInterval,
		},
		{
			name:             "uses custom ticker interval when provided",
			interval:         time.Second * 30,
			expectedInterval: time.Second * 30,
		},
		{
			name:             "uses default ticker interval when negative",
			interval:         -1,
			expectedInterval: DefaultTickerInterval,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMockMCP(tc.name, "")
			gateway := newMockToolsAdderDeleter()
			manager := NewUpstreamMCPManager(mock, gateway, logger, tc.interval)
			assert.Equal(t, tc.expectedInterval, manager.tickerInterval)
		})
	}
}

func TestMCPManager_MCPName(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("my-test-server", "prefix_")
	manager := NewUpstreamMCPManager(mock, nil, logger, 0)

	assert.Equal(t, "my-test-server", manager.MCPName())
}

func TestMCPManager_GetStatus(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test-server", "test_")
	manager := NewUpstreamMCPManager(mock, nil, logger, 0)

	expectedStatus := ServerValidationStatus{
		ID:            "test-id",
		Name:          "test-server",
		LastValidated: time.Now(),
		Message:       "test message",
		Ready:         true,
		TotalTools:    5,
	}
	manager.SetStatusForTesting(expectedStatus)

	status := manager.GetStatus()
	assert.Equal(t, expectedStatus.ID, status.ID)
	assert.Equal(t, expectedStatus.Name, status.Name)
	assert.Equal(t, expectedStatus.Ready, status.Ready)
	assert.Equal(t, expectedStatus.TotalTools, status.TotalTools)
}

func TestMCPManager_GetManagedTools(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test-server", "test_")
	gateway := newMockToolsAdderDeleter()
	manager := NewUpstreamMCPManager(mock, gateway, logger, 0)

	tools := []mcp.Tool{
		{Name: "tool1", Description: "Tool 1"},
		{Name: "tool2", Description: "Tool 2"},
	}
	manager.SetToolsForTesting(tools)

	managedTools := manager.GetManagedTools()

	assert.Len(t, managedTools, 2)
	assert.Equal(t, "tool1", managedTools[0].Name)
	assert.Equal(t, "tool2", managedTools[1].Name)
}

func TestMCPManager_GetManagedTools_ReturnsCopy(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test-server", "test_")
	gateway := newMockToolsAdderDeleter()
	manager := NewUpstreamMCPManager(mock, gateway, logger, 0)

	tools := []mcp.Tool{
		{Name: "tool1"},
	}
	manager.SetToolsForTesting(tools)

	// get tools and modify the returned slice
	managedTools := manager.GetManagedTools()
	managedTools[0].Name = "modified"

	// original should be unchanged
	original := manager.GetManagedTools()
	assert.Equal(t, "tool1", original[0].Name)
}

func TestMCPManager_GetServedManagedTool(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	testCases := []struct {
		name         string
		prefix       string
		tools        []mcp.Tool
		lookupName   string
		expectNil    bool
		expectedName string
	}{
		{
			name:         "returns tool with prefix",
			prefix:       "prefix_",
			tools:        []mcp.Tool{{Name: "mytool", Description: "My Tool"}},
			lookupName:   "prefix_mytool",
			expectNil:    false,
			expectedName: "mytool",
		},
		{
			name:         "returns tool without prefix",
			prefix:       "",
			tools:        []mcp.Tool{{Name: "mytool", Description: "My Tool"}},
			lookupName:   "mytool",
			expectNil:    false,
			expectedName: "mytool",
		},
		{
			name:       "returns nil for non-existent tool",
			prefix:     "prefix_",
			tools:      []mcp.Tool{{Name: "mytool"}},
			lookupName: "nonexistent",
			expectNil:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMockMCP("test-server", tc.prefix)
			gateway := newMockToolsAdderDeleter()
			manager := NewUpstreamMCPManager(mock, gateway, logger, 0)
			manager.SetToolsForTesting(tc.tools)

			tool := manager.GetServedManagedTool(tc.lookupName)
			if tc.expectNil {
				assert.Nil(t, tool)
			} else {
				assert.NotNil(t, tool)
				assert.Equal(t, tc.expectedName, tool.Name)
			}
		})
	}
}

func TestMCPManager_setStatus(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	testCases := []struct {
		name           string
		err            error
		totalTools     int
		numServerTools int
		expectReady    bool
		messageContain string
	}{
		{
			name:           "sets success status",
			err:            nil,
			totalTools:     3,
			numServerTools: 3,
			expectReady:    true,
			messageContain: "server added successfully",
		},
		{
			name:           "sets error status",
			err:            fmt.Errorf("connection failed"),
			totalTools:     0,
			numServerTools: 0,
			expectReady:    false,
			messageContain: "connection failed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMockMCP("test-server", "test_")
			manager := NewUpstreamMCPManager(mock, nil, logger, 0)
			manager.serverTools = make([]server.ServerTool, tc.numServerTools)

			manager.setStatus(tc.err, tc.totalTools)

			assert.Equal(t, string(mock.id), manager.status.ID)
			assert.Equal(t, "test-server", manager.status.Name)
			assert.Equal(t, tc.expectReady, manager.status.Ready)
			assert.Contains(t, manager.status.Message, tc.messageContain)
			if tc.expectReady {
				assert.Equal(t, tc.totalTools, manager.status.TotalTools)
			}
		})
	}
}

func TestPrefixedName(t *testing.T) {
	testCases := []struct {
		name     string
		prefix   string
		toolName string
		expected string
	}{
		{
			name:     "with prefix",
			prefix:   "server_",
			toolName: "tool",
			expected: "server_tool",
		},
		{
			name:     "without prefix",
			prefix:   "",
			toolName: "tool",
			expected: "tool",
		},
		{
			name:     "prefix with underscore",
			prefix:   "my_prefix_",
			toolName: "mytool",
			expected: "my_prefix_mytool",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := prefixedName(tc.prefix, tc.toolName)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestMCPManager_toolToServerTool(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test-server", "prefix_")
	manager := NewUpstreamMCPManager(mock, nil, logger, 0)

	tool := mcp.Tool{
		Name:        "mytool",
		Description: "A test tool",
	}

	serverTool := manager.toolToServerTool(tool)

	assert.Equal(t, "prefix_mytool", serverTool.Tool.Name)
	assert.Equal(t, "A test tool", serverTool.Tool.Description)

	// check that meta has id field
	id, ok := serverTool.Tool.Meta.AdditionalFields["id"]
	assert.True(t, ok)
	assert.Equal(t, string(mock.id), id)

	// handler should return error result
	result, err := serverTool.Handler(context.Background(), mcp.CallToolRequest{})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.IsError)
}

func TestMCPManager_Stop_Idempotent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test", "")
	gateway := newMockToolsAdderDeleter()
	manager := NewUpstreamMCPManager(mock, gateway, logger, time.Hour)

	// calling Stop multiple times should not panic
	manager.Stop()
	manager.Stop()
	manager.Stop()

	// verify manager state after stop
	assert.False(t, mock.connected, "mock should be disconnected after stop")
}

func TestMCPManager_manage_ConnectError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.connectErr = fmt.Errorf("connection refused")
	gateway := newMockToolsAdderDeleter()
	manager := NewUpstreamMCPManager(mock, gateway, logger, 0)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.False(t, status.Ready)
	assert.Contains(t, status.Message, "connection refused")
}

func TestMCPManager_manage_PingError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.pingErr = fmt.Errorf("ping timeout")
	gateway := newMockToolsAdderDeleter()
	manager := NewUpstreamMCPManager(mock, gateway, logger, 0)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.False(t, status.Ready)
	assert.Contains(t, status.Message, "ping")
}

func TestMCPManager_manage_ListToolsError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.listToolsErr = fmt.Errorf("list tools failed")
	mock.hasToolsCap = false // ensure we try to list tools
	gateway := newMockToolsAdderDeleter()
	manager := NewUpstreamMCPManager(mock, gateway, logger, 0)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.False(t, status.Ready)
	assert.Contains(t, status.Message, "list tools")
}

func TestMCPManager_manage_Success(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{
		{Name: "tool1"},
		{Name: "tool2"},
	}
	mock.hasToolsCap = false // ensure we list tools every time
	gateway := newMockToolsAdderDeleter()
	manager := NewUpstreamMCPManager(mock, gateway, logger, 0)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.True(t, status.Ready)
	assert.Equal(t, 2, status.TotalTools)

	// tools should be added to gateway
	assert.Len(t, gateway.tools, 2)
	assert.Contains(t, gateway.tools, "test_tool1")
	assert.Contains(t, gateway.tools, "test_tool2")
}

func TestDiffTools(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test-server", "test_")
	manager := NewUpstreamMCPManager(mock, nil, logger, 0)

	tests := []struct {
		name            string
		oldTools        []mcp.Tool
		newTools        []mcp.Tool
		expectedAdded   int
		expectedRemoved int
		addedNames      []string
		removedNames    []string
	}{
		{
			name:            "no changes",
			oldTools:        []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			newTools:        []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			expectedAdded:   0,
			expectedRemoved: 0,
		},
		{
			name:            "add new tool",
			oldTools:        []mcp.Tool{{Name: "tool1"}},
			newTools:        []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			expectedAdded:   1,
			expectedRemoved: 0,
			addedNames:      []string{"test_tool2"},
		},
		{
			name:            "remove tool",
			oldTools:        []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			newTools:        []mcp.Tool{{Name: "tool1"}},
			expectedAdded:   0,
			expectedRemoved: 1,
			removedNames:    []string{"test_tool2"},
		},
		{
			name:            "add and remove tools",
			oldTools:        []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			newTools:        []mcp.Tool{{Name: "tool1"}, {Name: "tool3"}},
			expectedAdded:   1,
			expectedRemoved: 1,
			addedNames:      []string{"test_tool3"},
			removedNames:    []string{"test_tool2"},
		},
		{
			name:            "empty old tools",
			oldTools:        []mcp.Tool{},
			newTools:        []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			expectedAdded:   2,
			expectedRemoved: 0,
		},
		{
			name:            "empty new tools",
			oldTools:        []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			newTools:        []mcp.Tool{},
			expectedAdded:   0,
			expectedRemoved: 2,
		},
		{
			name:            "both empty",
			oldTools:        []mcp.Tool{},
			newTools:        []mcp.Tool{},
			expectedAdded:   0,
			expectedRemoved: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, removed := manager.diffTools(tt.oldTools, tt.newTools)
			assert.Len(t, added, tt.expectedAdded, "unexpected number of added tools")
			assert.Len(t, removed, tt.expectedRemoved, "unexpected number of removed tools")

			if len(tt.addedNames) > 0 {
				addedToolNames := make([]string, len(added))
				for i, tool := range added {
					addedToolNames[i] = tool.Tool.Name
				}
				for _, expectedName := range tt.addedNames {
					assert.Contains(t, addedToolNames, expectedName)
				}
			}

			if len(tt.removedNames) > 0 {
				for _, expectedName := range tt.removedNames {
					assert.Contains(t, removed, expectedName)
				}
			}
		})
	}
}

// MockGatewayServer implements ToolsAdderDeleter for testing
type MockGatewayServer struct {
	tools map[string]*server.ServerTool
	mu    sync.Mutex
}

func NewMockGatewayServer() *MockGatewayServer {
	return &MockGatewayServer{
		tools: make(map[string]*server.ServerTool),
	}
}

func (m *MockGatewayServer) AddTools(tools ...server.ServerTool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range tools {
		m.tools[tools[i].Tool.Name] = &tools[i]
	}
}

func (m *MockGatewayServer) DeleteTools(names ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, name := range names {
		delete(m.tools, name)
	}
}

func (m *MockGatewayServer) ListTools() map[string]*server.ServerTool {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]*server.ServerTool, len(m.tools))
	for k, v := range m.tools {
		result[k] = v
	}
	return result
}

func TestMCPManager_shouldFetchTools(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	tests := []struct {
		name                    string
		supportsToolsListChange bool
		hasExistingTools        bool
		eventType               eventType
		expectedShouldFetch     bool
	}{
		{
			name:                    "no tools list change support always fetch on timer",
			supportsToolsListChange: false,
			hasExistingTools:        true,
			eventType:               eventTypeTimer,
			expectedShouldFetch:     true,
		},
		{
			name:                    "no tools list change support always fetch on notification",
			supportsToolsListChange: false,
			hasExistingTools:        true,
			eventType:               eventTypeNotification,
			expectedShouldFetch:     true,
		},
		{
			name:                    "with tools list change support fetch on notification",
			supportsToolsListChange: true,
			hasExistingTools:        true,
			eventType:               eventTypeNotification,
			expectedShouldFetch:     true,
		},
		{
			name:                    "with tools list change support skip fetch on timer when tools exist",
			supportsToolsListChange: true,
			hasExistingTools:        true,
			eventType:               eventTypeTimer,
			expectedShouldFetch:     false,
		},
		{
			name:                    "with tools list change support fetch on timer when no tools",
			supportsToolsListChange: true,
			hasExistingTools:        false,
			eventType:               eventTypeTimer,
			expectedShouldFetch:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockMCP("test-server", "test_")
			mock.hasToolsCap = tt.supportsToolsListChange
			gateway := newMockToolsAdderDeleter()
			manager := NewUpstreamMCPManager(mock, gateway, logger, 0)

			if tt.hasExistingTools {
				// set serverTools directly since shouldFetchTools checks this field
				manager.serverTools = []server.ServerTool{{Tool: mcp.Tool{Name: "existing_tool"}}}
			}

			result := manager.shouldFetchTools(tt.eventType)
			assert.Equal(t, tt.expectedShouldFetch, result)
		})
	}
}

func TestMCPManager_manage_SkipsFetchOnTimerWhenToolsListChangeSupported(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{{Name: "tool1"}}
	mock.hasToolsCap = true // supports tools list change notifications
	gateway := newMockToolsAdderDeleter()
	manager := NewUpstreamMCPManager(mock, gateway, logger, 0)

	// First call with notification - should fetch and add tools
	manager.manage(context.Background(), eventTypeNotification)
	assert.Equal(t, 1, gateway.addCalls, "should add tools on notification")
	assert.Len(t, gateway.tools, 1)

	// Update mock tools - simulating a change on the server
	mock.tools = []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}}

	// Timer event - should skip fetching since we support notifications and have tools
	manager.manage(context.Background(), eventTypeTimer)
	assert.Equal(t, 1, gateway.addCalls, "should not fetch tools on timer when notifications supported")
	assert.Len(t, gateway.tools, 1, "tools should remain unchanged")

	// Notification event - should fetch and update tools
	manager.manage(context.Background(), eventTypeNotification)
	assert.Equal(t, 2, gateway.addCalls, "should fetch tools on notification")
	assert.Len(t, gateway.tools, 2, "tools should be updated")
}

func TestMCPManager_manage_OnlyCallsAddDeleteWhenNeeded(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	tests := []struct {
		name             string
		initialTools     []mcp.Tool
		updatedTools     []mcp.Tool
		expectedAddCalls int
		expectedDelCalls int
	}{
		{
			name:             "no changes - neither AddTools nor DeleteTools called",
			initialTools:     []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			updatedTools:     []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			expectedAddCalls: 1, // only initial add
			expectedDelCalls: 0,
		},
		{
			name:             "only adding tools - only AddTools called",
			initialTools:     []mcp.Tool{{Name: "tool1"}},
			updatedTools:     []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			expectedAddCalls: 2, // initial + update
			expectedDelCalls: 0,
		},
		{
			name:             "only removing tools - only DeleteTools called",
			initialTools:     []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			updatedTools:     []mcp.Tool{{Name: "tool1"}},
			expectedAddCalls: 1, // only initial
			expectedDelCalls: 1,
		},
		{
			name:             "adding and removing - both called",
			initialTools:     []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			updatedTools:     []mcp.Tool{{Name: "tool1"}, {Name: "tool3"}},
			expectedAddCalls: 2, // initial + update
			expectedDelCalls: 1,
		},
		{
			name:             "empty to tools - only AddTools called",
			initialTools:     []mcp.Tool{},
			updatedTools:     []mcp.Tool{{Name: "tool1"}},
			expectedAddCalls: 1, // only update (initial has nothing to add)
			expectedDelCalls: 0,
		},
		{
			name:             "tools to empty - only DeleteTools called",
			initialTools:     []mcp.Tool{{Name: "tool1"}},
			updatedTools:     []mcp.Tool{},
			expectedAddCalls: 1, // only initial
			expectedDelCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mock := newMockMCP("test-server", "test_")
			mock.hasToolsCap = false // ensure we fetch tools on every manage call
			gateway := newMockToolsAdderDeleter()
			manager := NewUpstreamMCPManager(mock, gateway, logger, 0)

			// first manage call - establish initial tools
			mock.tools = tt.initialTools
			manager.manage(ctx, eventTypeTimer)

			// second manage call - apply updates
			mock.tools = tt.updatedTools
			manager.manage(ctx, eventTypeTimer)

			assert.Equal(t, tt.expectedAddCalls, gateway.addCalls,
				"unexpected number of AddTools calls")
			assert.Equal(t, tt.expectedDelCalls, gateway.delCalls,
				"unexpected number of DeleteTools calls")
		})
	}
}

func TestServerToolsManagement(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	tests := []struct {
		name                 string
		prefix               string
		initialTools         []mcp.Tool // tools returned by first ListTools call to backend MCP
		updatedTools         []mcp.Tool // tools returned by second ListTools call to backend MCP
		expectedServerTools  []string   // expected tool names in serverTools after update from backend MCP
		expectedGatewayTools []string   // expected tool names in gateway after update from backend MCP
	}{
		{
			name:                 "add tools to empty",
			prefix:               "test_",
			initialTools:         []mcp.Tool{},
			updatedTools:         []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			expectedServerTools:  []string{"test_tool1", "test_tool2"},
			expectedGatewayTools: []string{"test_tool1", "test_tool2"},
		},
		{
			name:                 "remove single tool",
			prefix:               "test_",
			initialTools:         []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}, {Name: "tool3"}},
			updatedTools:         []mcp.Tool{{Name: "tool1"}, {Name: "tool3"}},
			expectedServerTools:  []string{"test_tool1", "test_tool3"},
			expectedGatewayTools: []string{"test_tool1", "test_tool3"},
		},
		{
			name:                 "remove multiple tools",
			prefix:               "test_",
			initialTools:         []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}, {Name: "tool3"}},
			updatedTools:         []mcp.Tool{{Name: "tool1"}},
			expectedServerTools:  []string{"test_tool1"},
			expectedGatewayTools: []string{"test_tool1"},
		},
		{
			name:                 "add and remove tools simultaneously",
			prefix:               "test_",
			initialTools:         []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			updatedTools:         []mcp.Tool{{Name: "tool1"}, {Name: "tool3"}, {Name: "tool4"}},
			expectedServerTools:  []string{"test_tool1", "test_tool3", "test_tool4"},
			expectedGatewayTools: []string{"test_tool1", "test_tool3", "test_tool4"},
		},
		{
			name:                 "no changes keeps existing tools",
			prefix:               "test_",
			initialTools:         []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			updatedTools:         []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			expectedServerTools:  []string{"test_tool1", "test_tool2"},
			expectedGatewayTools: []string{"test_tool1", "test_tool2"},
		},
		{
			name:                 "remove all tools",
			prefix:               "test_",
			initialTools:         []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			updatedTools:         []mcp.Tool{},
			expectedServerTools:  []string{},
			expectedGatewayTools: []string{},
		},
		{
			name:                 "works without prefix",
			prefix:               "",
			initialTools:         []mcp.Tool{{Name: "tool1"}},
			updatedTools:         []mcp.Tool{{Name: "tool1"}, {Name: "tool2"}},
			expectedServerTools:  []string{"tool1", "tool2"},
			expectedGatewayTools: []string{"tool1", "tool2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mockMCP := newMockMCP("test-server", tt.prefix)
			mockMCP.hasToolsCap = false // ensure we fetch tools on every manage call
			mockGateway := NewMockGatewayServer()
			manager := NewUpstreamMCPManager(mockMCP, mockGateway, logger, 0)

			// First manage call - establish initial tools
			mockMCP.tools = tt.initialTools
			manager.manage(ctx, eventTypeTimer)

			// Second manage call - apply updates
			mockMCP.tools = tt.updatedTools
			manager.manage(ctx, eventTypeTimer)

			// Verify serverTools
			manager.toolsLock.RLock()
			serverToolNames := make([]string, len(manager.serverTools))
			for i, st := range manager.serverTools {
				serverToolNames[i] = st.Tool.Name
			}
			manager.toolsLock.RUnlock()

			assert.ElementsMatch(t, tt.expectedServerTools, serverToolNames,
				"serverTools mismatch")

			// Verify gateway tools
			gatewayTools := mockGateway.ListTools()
			gatewayToolNames := make([]string, 0, len(gatewayTools))
			for name := range gatewayTools {
				gatewayToolNames = append(gatewayToolNames, name)
			}

			assert.ElementsMatch(t, tt.expectedGatewayTools, gatewayToolNames,
				"gateway tools mismatch")

			// Verify no duplicates in serverTools
			seen := make(map[string]bool)
			for _, name := range serverToolNames {
				assert.False(t, seen[name], "duplicate tool found: %s", name)
				seen[name] = true
			}
		})
	}
}
