package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockMCP implements the MCP interface for testing
type MockMCP struct {
	name                string
	prefix              string
	id                  config.UpstreamMCPID
	cfg                 *config.MCPServer
	connectErr          error
	pingErr             error
	tools               []mcp.Tool
	listToolsErr        error
	listToolsDelay      time.Duration
	prompts             []mcp.Prompt
	listPromptsErr      error
	protocolVersion     string
	hasToolsCap         bool
	hasPromptsCap       bool
	connected           atomic.Bool
	notificationHandler func(method string)
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
	m.connected.Store(true)
	if onConnected != nil {
		onConnected()
	}
	return nil
}

func (m *MockMCP) SupportsToolsListChanged() bool {
	return m.hasToolsCap
}

func (m *MockMCP) Disconnect() error {
	m.connected.Store(false)
	return nil
}

func (m *MockMCP) ListTools(ctx context.Context) (*mcp.ListToolsResult, error) {
	if m.listToolsDelay > 0 {
		select {
		case <-time.After(m.listToolsDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.listToolsErr != nil {
		return nil, m.listToolsErr
	}
	ptrs := make([]*mcp.Tool, len(m.tools))
	for i := range m.tools {
		ptrs[i] = &m.tools[i]
	}
	return &mcp.ListToolsResult{Tools: ptrs}, nil
}

func (m *MockMCP) SupportsPrompts() bool {
	return m.hasPromptsCap
}

func (m *MockMCP) SupportsPromptsListChanged() bool {
	return m.hasPromptsCap
}

func (m *MockMCP) ListPrompts(_ context.Context) (*mcp.ListPromptsResult, error) {
	if m.listPromptsErr != nil {
		return nil, m.listPromptsErr
	}
	ptrs := make([]*mcp.Prompt, len(m.prompts))
	for i := range m.prompts {
		ptrs[i] = &m.prompts[i]
	}
	return &mcp.ListPromptsResult{Prompts: ptrs}, nil
}

func (m *MockMCP) OnNotification(handler func(method string)) {
	m.notificationHandler = handler
}

func (m *MockMCP) OnConnectionLost(_ func(err error)) {}

func (m *MockMCP) Ping(_ context.Context) error {
	return m.pingErr
}

func (m *MockMCP) IsEnabled() bool {
	if m.cfg == nil {
		return true
	}
	return m.cfg.State == "" || m.cfg.State == string(mcpv1alpha1.ServerStateEnabled)
}

func (m *MockMCP) GetToolHints(string) (ToolHints, bool) {
	return ToolHints{}, false
}

func (m *MockMCP) ProtocolInfo() *mcp.InitializeResult {
	result := &mcp.InitializeResult{
		ProtocolVersion: m.protocolVersion,
		Capabilities:    &mcp.ServerCapabilities{},
	}
	if m.hasToolsCap {
		result.Capabilities.Tools = &mcp.ToolCapabilities{}
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
		cfg:             &config.MCPServer{Name: name, Prefix: prefix, URL: "http://mock/mcp"},
		protocolVersion: "2025-03-26",
		hasToolsCap:     true,
		tools:           []mcp.Tool{{Name: "mock_tool", InputSchema: map[string]any{"type": "object"}}},
	}
}

func validTool(name string) mcp.Tool {
	return mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}
}

// MockToolsAdderDeleter implements ToolsAdderDeleter for testing
type MockToolsAdderDeleter struct {
	tools    map[string]*GatewayTool
	addCalls int
	delCalls int
}

func newMockToolsAdderDeleter() *MockToolsAdderDeleter {
	return &MockToolsAdderDeleter{
		tools: make(map[string]*GatewayTool),
	}
}

func (m *MockToolsAdderDeleter) AddTools(tools ...GatewayTool) {
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

func (m *MockToolsAdderDeleter) ListTools() map[string]*GatewayTool {
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
			manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, tc.interval, mcpv1alpha1.InvalidToolPolicyFilterOut)
			require.NoError(t, err)
			assert.Equal(t, tc.expectedInterval, manager.tickerInterval)
		})
	}
}

func TestMCPManager_MCPName(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("my-test-server", "prefix_")
	manager, err := NewUpstreamMCPManager(mock, newMockToolsAdderDeleter(), nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	assert.Equal(t, "my-test-server", manager.MCPName())
}

func TestMCPManager_GetStatus(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test-server", "test_")
	manager, err := NewUpstreamMCPManager(mock, newMockToolsAdderDeleter(), nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

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
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	tools := []mcp.Tool{
		{Name: "tool1", Description: "Tool 1", InputSchema: map[string]any{"type": "object"}},
		{Name: "tool2", Description: "Tool 2", InputSchema: map[string]any{"type": "object"}},
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
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	tools := []mcp.Tool{validTool("tool1")}
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
			manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
			require.NoError(t, err)
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
			manager, err := NewUpstreamMCPManager(mock, newMockToolsAdderDeleter(), nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
			require.NoError(t, err)
			manager.serverTools = make([]GatewayTool, tc.numServerTools)

			manager.setStatus(tc.err, tc.totalTools, 0, nil, nil)

			assert.Equal(t, string(mock.id), manager.status.ID)
			assert.Equal(t, "test-server", manager.status.Name)
			assert.Equal(t, tc.expectReady, manager.status.Ready)
			assert.Contains(t, manager.status.Message, tc.messageContain)
			if tc.expectReady {
				assert.Equal(t, tc.totalTools, manager.status.TotalTools)
				assert.True(t, manager.status.ProtocolValidation.IsValid)
				assert.Equal(t, mock.protocolVersion, manager.status.ProtocolValidation.SupportedVersion)
				assert.Equal(t, expectedProtocolVersion, manager.status.ProtocolValidation.ExpectedVersion)
			}
		})
	}
}

// TestMCPManager_setStatus_ProtocolVersions verifies the negotiated protocol version
// reported by an upstream is surfaced on the status across valid versions.
func TestMCPManager_setStatus_ProtocolVersions(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	var testProtocolVersions = []string{"2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25", "2026-07-28"}

	for _, version := range testProtocolVersions {
		t.Run(version, func(t *testing.T) {
			mock := newMockMCP("test-server", "test_")
			mock.protocolVersion = version
			manager, err := NewUpstreamMCPManager(mock, newMockToolsAdderDeleter(), nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
			require.NoError(t, err)

			manager.setStatus(nil, 1, 0, nil, nil)

			assert.True(t, manager.status.ProtocolValidation.IsValid)
			assert.Equal(t, version, manager.status.ProtocolValidation.SupportedVersion)
			assert.Equal(t, expectedProtocolVersion, manager.status.ProtocolValidation.ExpectedVersion)
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
	manager, err := NewUpstreamMCPManager(mock, newMockToolsAdderDeleter(), nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	tool := mcp.Tool{
		Name:        "mytool",
		Description: "A test tool",
	}

	serverTool := manager.toolToServerTool(tool)

	assert.Equal(t, "prefix_mytool", serverTool.Tool.Name)
	assert.Equal(t, "A test tool", serverTool.Tool.Description)

	// check that meta has id field
	id, ok := serverTool.Tool.Meta[gatewayServerID]
	assert.True(t, ok)
	assert.Equal(t, string(mock.id), id)

	// handler should return error result
	result, err := serverTool.Handler(context.Background(), &mcp.CallToolRequest{})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result.IsError)
}

func TestMCPManager_Stop_Idempotent(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test", "")
	gateway := newMockToolsAdderDeleter()
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, time.Hour, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	active := manager.Start(context.Background())

	// calling Stop multiple times should not panic
	active.Stop()
	active.Stop()
	active.Stop()

	// verify manager state after stop
	assert.False(t, mock.connected.Load(), "mock should be disconnected after stop")
}

func TestMCPManager_manage_ConnectError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.connectErr = fmt.Errorf("connection refused")
	gateway := newMockToolsAdderDeleter()
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

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
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

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
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.False(t, status.Ready)
	assert.Contains(t, status.Message, "list tools")
}

func TestMCPManager_manage_Success(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{validTool("tool1"), validTool("tool2")}
	mock.hasToolsCap = false // ensure we list tools every time
	gateway := newMockToolsAdderDeleter()
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.True(t, status.Ready)
	assert.Equal(t, 2, status.TotalTools)

	// tools should be added to gateway
	assert.Len(t, gateway.tools, 2)
	assert.Contains(t, gateway.tools, "test_tool1")
	assert.Contains(t, gateway.tools, "test_tool2")
}

func TestMCPManager_manage_UserSpecificList_SkipsToolCaching(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("user-specific-server", "us_")
	mock.cfg.UserSpecificList = true
	mock.tools = []mcp.Tool{validTool("tool1")}
	mock.hasToolsCap = false
	gateway := newMockToolsAdderDeleter()
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.True(t, status.Ready, "server should be healthy")
	assert.Equal(t, 0, status.TotalTools, "no tools should be cached")
	assert.Contains(t, status.Message, "userSpecificList")

	// no tools added to gateway
	assert.Empty(t, gateway.tools, "tools should not be added to gateway for userSpecificList servers")

	// connect and ping still ran
	assert.True(t, mock.connected.Load(), "server should still be connected for health checks")
}

func TestDiffTools(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test-server", "test_")
	manager, err := NewUpstreamMCPManager(mock, newMockToolsAdderDeleter(), nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

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
			oldTools:        []mcp.Tool{validTool("tool1"), validTool("tool2")},
			newTools:        []mcp.Tool{validTool("tool1"), validTool("tool2")},
			expectedAdded:   0,
			expectedRemoved: 0,
		},
		{
			name:            "add new tool",
			oldTools:        []mcp.Tool{validTool("tool1")},
			newTools:        []mcp.Tool{validTool("tool1"), validTool("tool2")},
			expectedAdded:   1,
			expectedRemoved: 0,
			addedNames:      []string{"test_tool2"},
		},
		{
			name:            "remove tool",
			oldTools:        []mcp.Tool{validTool("tool1"), validTool("tool2")},
			newTools:        []mcp.Tool{validTool("tool1")},
			expectedAdded:   0,
			expectedRemoved: 1,
			removedNames:    []string{"test_tool2"},
		},
		{
			name:            "add and remove tools",
			oldTools:        []mcp.Tool{validTool("tool1"), validTool("tool2")},
			newTools:        []mcp.Tool{validTool("tool1"), validTool("tool3")},
			expectedAdded:   1,
			expectedRemoved: 1,
			addedNames:      []string{"test_tool3"},
			removedNames:    []string{"test_tool2"},
		},
		{
			name:            "empty old tools",
			oldTools:        []mcp.Tool{},
			newTools:        []mcp.Tool{validTool("tool1"), validTool("tool2")},
			expectedAdded:   2,
			expectedRemoved: 0,
		},
		{
			name:            "empty new tools",
			oldTools:        []mcp.Tool{validTool("tool1"), validTool("tool2")},
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
	tools map[string]*GatewayTool
	mu    sync.Mutex
}

func NewMockGatewayServer() *MockGatewayServer {
	return &MockGatewayServer{
		tools: make(map[string]*GatewayTool),
	}
}

func (m *MockGatewayServer) AddTools(tools ...GatewayTool) {
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

func (m *MockGatewayServer) ListTools() map[string]*GatewayTool {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]*GatewayTool, len(m.tools))
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
			eventType:               eventTypeToolNotification,
			expectedShouldFetch:     true,
		},
		{
			name:                    "with tools list change support fetch on notification",
			supportsToolsListChange: true,
			hasExistingTools:        true,
			eventType:               eventTypeToolNotification,
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
			manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
			require.NoError(t, err)

			if tt.hasExistingTools {
				manager.serverTools = []GatewayTool{{Tool: mcp.Tool{Name: "existing_tool"}}}
			}

			result := manager.shouldFetchTools(tt.eventType)
			assert.Equal(t, tt.expectedShouldFetch, result)
		})
	}
}

func TestMCPManager_manage_SkipsFetchOnTimerWhenToolsListChangeSupported(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{validTool("tool1")}
	mock.hasToolsCap = true // supports tools list change notifications
	gateway := newMockToolsAdderDeleter()
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	// First call with notification - should fetch and add tools
	manager.manage(context.Background(), eventTypeToolNotification)
	assert.Equal(t, 1, gateway.addCalls, "should add tools on notification")
	assert.Len(t, gateway.tools, 1)

	// Update mock tools - simulating a change on the server
	mock.tools = []mcp.Tool{validTool("tool1"), validTool("tool2")}

	// Timer event - should skip fetching since we support notifications and have tools
	manager.manage(context.Background(), eventTypeTimer)
	assert.Equal(t, 1, gateway.addCalls, "should not fetch tools on timer when notifications supported")
	assert.Len(t, gateway.tools, 1, "tools should remain unchanged")

	// Notification event - should fetch and update tools
	manager.manage(context.Background(), eventTypeToolNotification)
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
			initialTools:     []mcp.Tool{validTool("tool1"), validTool("tool2")},
			updatedTools:     []mcp.Tool{validTool("tool1"), validTool("tool2")},
			expectedAddCalls: 1, // only initial add
			expectedDelCalls: 0,
		},
		{
			name:             "only adding tools - only AddTools called",
			initialTools:     []mcp.Tool{validTool("tool1")},
			updatedTools:     []mcp.Tool{validTool("tool1"), validTool("tool2")},
			expectedAddCalls: 2, // initial + update
			expectedDelCalls: 0,
		},
		{
			name:             "only removing tools - only DeleteTools called",
			initialTools:     []mcp.Tool{validTool("tool1"), validTool("tool2")},
			updatedTools:     []mcp.Tool{validTool("tool1")},
			expectedAddCalls: 1, // only initial
			expectedDelCalls: 1,
		},
		{
			name:             "adding and removing - both called",
			initialTools:     []mcp.Tool{validTool("tool1"), validTool("tool2")},
			updatedTools:     []mcp.Tool{validTool("tool1"), validTool("tool3")},
			expectedAddCalls: 2, // initial + update
			expectedDelCalls: 1,
		},
		{
			name:             "empty to tools - only AddTools called",
			initialTools:     []mcp.Tool{},
			updatedTools:     []mcp.Tool{validTool("tool1")},
			expectedAddCalls: 1, // only update (initial has nothing to add)
			expectedDelCalls: 0,
		},
		{
			name:             "tools to empty - only DeleteTools called",
			initialTools:     []mcp.Tool{validTool("tool1")},
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
			manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
			require.NoError(t, err)

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
			updatedTools:         []mcp.Tool{validTool("tool1"), validTool("tool2")},
			expectedServerTools:  []string{"test_tool1", "test_tool2"},
			expectedGatewayTools: []string{"test_tool1", "test_tool2"},
		},
		{
			name:                 "remove single tool",
			prefix:               "test_",
			initialTools:         []mcp.Tool{validTool("tool1"), validTool("tool2"), validTool("tool3")},
			updatedTools:         []mcp.Tool{validTool("tool1"), validTool("tool3")},
			expectedServerTools:  []string{"test_tool1", "test_tool3"},
			expectedGatewayTools: []string{"test_tool1", "test_tool3"},
		},
		{
			name:                 "remove multiple tools",
			prefix:               "test_",
			initialTools:         []mcp.Tool{validTool("tool1"), validTool("tool2"), validTool("tool3")},
			updatedTools:         []mcp.Tool{validTool("tool1")},
			expectedServerTools:  []string{"test_tool1"},
			expectedGatewayTools: []string{"test_tool1"},
		},
		{
			name:                 "add and remove tools simultaneously",
			prefix:               "test_",
			initialTools:         []mcp.Tool{validTool("tool1"), validTool("tool2")},
			updatedTools:         []mcp.Tool{validTool("tool1"), validTool("tool3"), validTool("tool4")},
			expectedServerTools:  []string{"test_tool1", "test_tool3", "test_tool4"},
			expectedGatewayTools: []string{"test_tool1", "test_tool3", "test_tool4"},
		},
		{
			name:                 "no changes keeps existing tools",
			prefix:               "test_",
			initialTools:         []mcp.Tool{validTool("tool1"), validTool("tool2")},
			updatedTools:         []mcp.Tool{validTool("tool1"), validTool("tool2")},
			expectedServerTools:  []string{"test_tool1", "test_tool2"},
			expectedGatewayTools: []string{"test_tool1", "test_tool2"},
		},
		{
			name:                 "remove all tools",
			prefix:               "test_",
			initialTools:         []mcp.Tool{validTool("tool1"), validTool("tool2")},
			updatedTools:         []mcp.Tool{},
			expectedServerTools:  []string{},
			expectedGatewayTools: []string{},
		},
		{
			name:                 "works without prefix",
			prefix:               "",
			initialTools:         []mcp.Tool{validTool("tool1")},
			updatedTools:         []mcp.Tool{validTool("tool1"), validTool("tool2")},
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
			manager, err := NewUpstreamMCPManager(mockMCP, mockGateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
			require.NoError(t, err)

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

func TestMCPManager_manage_FilterOutPolicy(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{
		validTool("good_tool"),
		{Name: "bad_tool", InputSchema: map[string]any{"type": "int"}},
	}
	mock.hasToolsCap = false
	gateway := newMockToolsAdderDeleter()
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.True(t, status.Ready)
	assert.Equal(t, 1, status.TotalTools)
	assert.Equal(t, 1, status.InvalidTools)
	assert.Len(t, status.InvalidToolList, 1)
	assert.Equal(t, "bad_tool", status.InvalidToolList[0].Name)
	assert.Len(t, gateway.tools, 1)
	assert.Contains(t, gateway.tools, "test_good_tool")
}

func TestMCPManager_manage_FilterOutPolicy_AllInvalid(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{
		{Name: "bad1", InputSchema: map[string]any{"type": "int"}},
		{Name: "bad2", InputSchema: map[string]any{"type": "string"}},
	}
	mock.hasToolsCap = false
	gateway := newMockToolsAdderDeleter()
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.True(t, status.Ready)
	assert.Equal(t, 0, status.TotalTools)
	assert.Equal(t, 2, status.InvalidTools)
	assert.Empty(t, gateway.tools)
}

func TestMCPManager_manage_RejectServerPolicy(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{
		validTool("good_tool"),
		{Name: "bad_tool", InputSchema: map[string]any{"type": "int"}},
	}
	mock.hasToolsCap = false
	gateway := newMockToolsAdderDeleter()
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyRejectServer)
	require.NoError(t, err)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.False(t, status.Ready)
	assert.Contains(t, status.Message, "rejected")
	assert.Equal(t, 1, status.InvalidTools)
	assert.Empty(t, gateway.tools)
}

func TestMCPManager_manage_AllValidTools(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{validTool("tool1"), validTool("tool2")}
	mock.hasToolsCap = false
	gateway := newMockToolsAdderDeleter()
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.True(t, status.Ready)
	assert.Equal(t, 2, status.TotalTools)
	assert.Equal(t, 0, status.InvalidTools)
	assert.Empty(t, status.InvalidToolList)
	assert.Len(t, gateway.tools, 2)
}

func TestMCPManager_NewUpstreamMCPManager_nilGateway(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test-server", "test_")
	_, err := NewUpstreamMCPManager(mock, nil, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gateway server is required")
}

func TestMCPManager_EventChannel_NotificationRoutesThrough(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{validTool("tool1")}
	mock.hasToolsCap = true
	gateway := NewMockGatewayServer()
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, time.Hour, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go manager.Start(ctx)

	require.Eventually(t, func() bool {
		return len(gateway.ListTools()) == 1
	}, time.Second, 10*time.Millisecond, "initial tools should be added")

	// simulate upstream adding a tool
	mock.tools = []mcp.Tool{validTool("tool1"), validTool("tool2")}

	// fire notification through the captured callback
	require.NotNil(t, mock.notificationHandler, "notification handler should be registered")
	mock.notificationHandler(notificationToolsListChanged)

	require.Eventually(t, func() bool {
		tools := gateway.ListTools()
		_, has := tools["test_tool2"]
		return len(tools) == 2 && has
	}, time.Second, 10*time.Millisecond, "notification should trigger tool sync")
}

// verifies GetManagedTools/GetServedManagedTool don't race with manage() under -race.
func TestMCPManager_ConcurrentReadsDuringManage(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.hasToolsCap = false
	gateway := newMockToolsAdderDeleter()
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	ctx := context.Background()
	var wg sync.WaitGroup
	const readers = 10
	const iterations = 100

	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				tools := manager.GetManagedTools()
				n := len(tools)
				assert.True(t, n == 0 || n == 1 || n == 2, "unexpected tool count: %d", n)
				_ = manager.GetServedManagedTool("test_tool1")
			}
		}()
	}

	for i := range iterations {
		if i%2 == 0 {
			mock.tools = []mcp.Tool{validTool("tool1"), validTool("tool2")}
		} else {
			mock.tools = []mcp.Tool{validTool("tool1")}
		}
		manager.manage(ctx, eventTypeTimer)
	}

	wg.Wait()
	tools := manager.GetManagedTools()
	assert.NotEmpty(t, tools, "tools should be present after concurrent access")
}

// TestMCPManager_StopDuringManage starts the event loop, triggers a manage()
// cycle via the events channel (with a slow ListTools), then calls Stop() while
// manage() is in-flight. Verifies the shutdown path completes without deadlock.
func TestMCPManager_StopDuringManage(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("race-server", "race_")
	mock.tools = []mcp.Tool{validTool("tool1"), validTool("tool2")}
	mock.hasToolsCap = false
	// slow down ListTools so manage() is mid-flight when Stop() fires
	mock.listToolsDelay = 100 * time.Millisecond
	gateway := NewMockGatewayServer()
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, time.Hour, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	active := manager.Start(context.Background())
	// let Start complete its initial manage() call
	time.Sleep(200 * time.Millisecond)

	// trigger another manage() on the event loop via the events channel;
	// ListTools will block for 100ms giving us time to call Stop()
	manager.toolEvents <- struct{}{}

	time.Sleep(10 * time.Millisecond)

	// Stop() cancels the context; the event loop will finish the in-flight
	// manage(), then select ctx.Done() and run removeAllTools
	active.Stop()

	assert.False(t, mock.connected.Load(), "mock should be disconnected after stop")
}

// MockPromptsAdderDeleter implements PromptsAdderDeleter for testing
type MockPromptsAdderDeleter struct {
	prompts  map[string]*GatewayPrompt
	addCalls int
	delCalls int
	mu       sync.Mutex
}

func newMockPromptsAdderDeleter() *MockPromptsAdderDeleter {
	return &MockPromptsAdderDeleter{
		prompts: make(map[string]*GatewayPrompt),
	}
}

func (m *MockPromptsAdderDeleter) AddPrompts(prompts ...GatewayPrompt) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addCalls++
	for i := range prompts {
		m.prompts[prompts[i].Prompt.Name] = &prompts[i]
	}
}

func (m *MockPromptsAdderDeleter) DeletePrompts(names ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delCalls++
	for _, name := range names {
		delete(m.prompts, name)
	}
}

func (m *MockPromptsAdderDeleter) ListPrompts() map[string]*GatewayPrompt {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make(map[string]*GatewayPrompt, len(m.prompts))
	for k, v := range m.prompts {
		result[k] = v
	}
	return result
}

func TestMCPManager_promptToServerPrompt(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	tests := []struct {
		name         string
		prefix       string
		promptName   string
		expectedName string
		promptDesc   string
	}{
		{name: "with prefix", prefix: "prefix_", promptName: "myprompt", expectedName: "prefix_myprompt", promptDesc: "A test prompt"},
		{name: "without prefix", prefix: "", promptName: "myprompt", expectedName: "myprompt", promptDesc: "No prefix"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := newMockMCP("test-server", tt.prefix)
			manager, err := NewUpstreamMCPManager(mock, newMockToolsAdderDeleter(), nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
			require.NoError(t, err)

			serverPrompt := manager.promptToServerPrompt(mcp.Prompt{Name: tt.promptName, Description: tt.promptDesc})

			assert.Equal(t, tt.expectedName, serverPrompt.Prompt.Name)
			assert.Equal(t, tt.promptDesc, serverPrompt.Prompt.Description)

			id, ok := serverPrompt.Prompt.Meta[gatewayServerID]
			assert.True(t, ok)
			assert.Equal(t, string(mock.id), id)

			result, promptErr := serverPrompt.Handler(context.Background(), &mcp.GetPromptRequest{})
			assert.NoError(t, promptErr)
			assert.NotNil(t, result)
			assert.Empty(t, result.Messages)
		})
	}
}

func TestMCPManager_diffPrompts(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test-server", "test_")
	manager, err := NewUpstreamMCPManager(mock, newMockToolsAdderDeleter(), nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	tests := []struct {
		name            string
		oldPrompts      []mcp.Prompt
		newPrompts      []mcp.Prompt
		expectedAdded   int
		expectedRemoved int
	}{
		{
			name:            "no changes",
			oldPrompts:      []mcp.Prompt{{Name: "p1"}, {Name: "p2"}},
			newPrompts:      []mcp.Prompt{{Name: "p1"}, {Name: "p2"}},
			expectedAdded:   0,
			expectedRemoved: 0,
		},
		{
			name:            "add new prompt",
			oldPrompts:      []mcp.Prompt{{Name: "p1"}},
			newPrompts:      []mcp.Prompt{{Name: "p1"}, {Name: "p2"}},
			expectedAdded:   1,
			expectedRemoved: 0,
		},
		{
			name:            "remove prompt",
			oldPrompts:      []mcp.Prompt{{Name: "p1"}, {Name: "p2"}},
			newPrompts:      []mcp.Prompt{{Name: "p1"}},
			expectedAdded:   0,
			expectedRemoved: 1,
		},
		{
			name:            "both empty",
			oldPrompts:      []mcp.Prompt{},
			newPrompts:      []mcp.Prompt{},
			expectedAdded:   0,
			expectedRemoved: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, removed := manager.diffPrompts(tt.oldPrompts, tt.newPrompts)
			assert.Len(t, added, tt.expectedAdded)
			assert.Len(t, removed, tt.expectedRemoved)
		})
	}
}

func TestMCPManager_GetManagedPrompts(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test-server", "test_")
	manager, err := NewUpstreamMCPManager(mock, newMockToolsAdderDeleter(), nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	prompts := []mcp.Prompt{
		{Name: "prompt1", Description: "Prompt 1"},
		{Name: "prompt2", Description: "Prompt 2"},
	}
	manager.SetPromptsForTesting(prompts)

	managedPrompts := manager.GetManagedPrompts()
	assert.Len(t, managedPrompts, 2)
	assert.Equal(t, "prompt1", managedPrompts[0].Name)
	assert.Equal(t, "prompt2", managedPrompts[1].Name)
}

func TestMCPManager_GetServedManagedPrompt(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test-server", "prefix_")
	manager, err := NewUpstreamMCPManager(mock, newMockToolsAdderDeleter(), nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)
	manager.SetPromptsForTesting([]mcp.Prompt{{Name: "myprompt", Description: "My Prompt"}})

	prompt := manager.GetServedManagedPrompt("prefix_myprompt")
	assert.NotNil(t, prompt)
	assert.Equal(t, "myprompt", prompt.Name)

	nilPrompt := manager.GetServedManagedPrompt("nonexistent")
	assert.Nil(t, nilPrompt)
}

func TestMCPManager_manage_PromptsSuccess(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{validTool("tool1")}
	mock.prompts = []mcp.Prompt{{Name: "prompt1"}, {Name: "prompt2"}}
	mock.hasToolsCap = false
	mock.hasPromptsCap = true
	gateway := newMockToolsAdderDeleter()
	promptsGateway := newMockPromptsAdderDeleter()
	manager, err := NewUpstreamMCPManager(mock, gateway, promptsGateway, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.True(t, status.Ready)
	assert.Equal(t, 1, status.TotalTools)
	assert.Equal(t, 2, status.TotalPrompts)

	assert.Len(t, gateway.tools, 1)
	assert.Len(t, promptsGateway.prompts, 2)
	assert.Contains(t, promptsGateway.prompts, "test_prompt1")
	assert.Contains(t, promptsGateway.prompts, "test_prompt2")
}

func TestMCPManager_manage_PromptsNilServer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{validTool("tool1")}
	mock.prompts = []mcp.Prompt{{Name: "prompt1"}}
	mock.hasToolsCap = false
	gateway := newMockToolsAdderDeleter()
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.True(t, status.Ready)
	assert.Equal(t, 1, status.TotalTools)
}

func TestMCPManager_Backoff(t *testing.T) {
	ctx := context.Background()

	mock := newMockMCP("test-server", "")
	mock.hasToolsCap = false // Force it to fetch tools on every tick
	gateway := newMockToolsAdderDeleter()

	// Set a long ticker interval
	tickerInterval := time.Minute
	manager, err := NewUpstreamMCPManager(mock, gateway, nil, slog.Default(), tickerInterval, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	// 1. Simulate failure (Connect)
	mock.connectErr = fmt.Errorf("connect error")
	manager.manage(ctx, eventTypeTimer)

	status := manager.GetStatus()
	assert.False(t, status.Ready)
	assert.Contains(t, status.Message, "connect error")

	// 2. Simulate success
	mock.connectErr = nil
	manager.manage(ctx, eventTypeTimer)

	status = manager.GetStatus()
	assert.True(t, status.Ready)
	assert.Contains(t, status.Message, "server added successfully")

	// 3. Test Ping failure
	mock.pingErr = fmt.Errorf("ping error")
	manager.manage(ctx, eventTypeTimer)

	status = manager.GetStatus()
	assert.False(t, status.Ready)
	assert.Contains(t, status.Message, "ping error")

	// Reset
	mock.pingErr = nil
	manager.manage(ctx, eventTypeTimer)
	assert.True(t, manager.GetStatus().Ready)

	// 4. Test ListTools failure
	mock.listToolsErr = fmt.Errorf("list tools error")
	manager.manage(ctx, eventTypeTimer)

	status = manager.GetStatus()
	assert.False(t, status.Ready)
	assert.Contains(t, status.Message, "list tools error")

	// Reset
	mock.listToolsErr = nil
	manager.manage(ctx, eventTypeTimer)
	assert.True(t, manager.GetStatus().Ready)
}
