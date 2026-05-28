package upstream

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

// TestMCPManager_ManageConcurrencyRace validates the fix for issue #916
// It concurrently fires tool and prompt list changed notifications while the
// manager's internal ticker is also firing. The event loop architecture
// should serialize these events safely without data races or panics.
func TestMCPManager_ManageConcurrencyRace(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mockMCP := newMockMCP("race-test", "rt_")
	mockTools := newMockToolsAdderDeleter()

	// Create manager with a very fast ticker (1ms) to maximize collision probability
	manager, err := NewUpstreamMCPManager(mockMCP, mockTools, nil, logger, time.Millisecond, mcpv1alpha1.InvalidToolPolicyFilterOut)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the manager event loop
	activeServer := manager.Start(ctx)
	defer activeServer.Stop()

	// Wait for the manager to connect and register callbacks
	require.Eventually(t, func() bool {
		return mockMCP.GetNotificationHandler() != nil
	}, 2*time.Second, 10*time.Millisecond, "notification handler should be registered")

	handler := mockMCP.GetNotificationHandler()
	require.NotNil(t, handler)

	var wg sync.WaitGroup

	// Fire 100 concurrent tool list changed notifications
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler(mcp.JSONRPCNotification{
				Notification: mcp.Notification{
					Method: "notifications/tools/list_changed",
				},
			})
		}()
	}

	// Fire 100 concurrent prompt list changed notifications
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			handler(mcp.JSONRPCNotification{
				Notification: mcp.Notification{
					Method: "notifications/prompts/list_changed",
				},
			})
		}()
	}

	// Wait for all concurrent notifications to be sent
	wg.Wait()

	// Wait for the event loop to process the buffered notifications and converge
	require.Eventually(t, func() bool {
		status := activeServer.GetStatus()
		tools := activeServer.GetManagedTools()
		return status.Ready && status.TotalTools == 1 && len(tools) == 1 && tools[0].Name == "mock_tool"
	}, 2*time.Second, 10*time.Millisecond, "manager should process notifications and converge to correct state")

	// Verify that the manager is still healthy and no panic/race occurred
	require.True(t, activeServer.GetStatus().Ready)
	require.Equal(t, 1, activeServer.GetStatus().TotalTools)
	require.Empty(t, activeServer.GetStatus().InvalidToolList)

	// Ensure tools can be fetched safely concurrently
	tools := activeServer.GetManagedTools()
	require.NotNil(t, tools)

	// Validate state consistency: no duplicate managed tools or stale entries
	// The mock MCP server is initialized with exactly 1 tool ("mock_tool")
	require.Len(t, tools, 1, "There should be exactly 1 tool without duplicates")
	require.Equal(t, "mock_tool", tools[0].Name, "Tool name should match the raw tool name from upstream")
}
