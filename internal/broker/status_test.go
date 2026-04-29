package broker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestStatusHandlerNotGet(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mcpBroker := NewBroker(logger)
	sh := NewStatusHandler(mcpBroker, *logger)

	w := httptest.NewRecorder()
	sh.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/status", nil))
	res := w.Result()
	require.Equal(t, 405, res.StatusCode)
}

func createTestManagerForStatus(t *testing.T, serverName string, tools []mcp.Tool) *upstream.MCPManager {
	t.Helper()
	mcpServer := upstream.NewUpstreamMCP(&config.MCPServer{
		Name:       serverName,
		ToolPrefix: "test_",
		URL:        "http://test.local/mcp",
	})
	manager := upstream.NewUpstreamMCPManager(mcpServer, nil, slog.Default(), 0, mcpv1alpha1.InvalidToolPolicyFilterOut)
	manager.SetToolsForTesting(tools)
	manager.SetStatusForTesting(upstream.ServerValidationStatus{
		Name:  serverName,
		Ready: false,
	})
	return manager
}

func TestStatusHandlerGetSingleServer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mcpBroker := NewBroker(logger)
	sh := NewStatusHandler(mcpBroker, *logger)

	// At first, no server known for this name
	w := httptest.NewRecorder()
	sh.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/status/dummyServer", nil))
	res := w.Result()
	require.Equal(t, 404, res.StatusCode)

	// Add a server
	brokerImpl, ok := mcpBroker.(*mcpBrokerImpl)
	require.True(t, ok)
	brokerImpl.mcpServers["dummyServer:test_:http://test.local/mcp"] = createTestManagerForStatus(t,
		"dummyServer",
		[]mcp.Tool{{Name: "dummyTool"}},
	)

	w = httptest.NewRecorder()
	sh.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/status/dummyServer", nil))
	res = w.Result()
	require.Equal(t, 200, res.StatusCode)
}

func TestStatusHandlerGetAll(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mcpBroker := NewBroker(logger)
	sh := NewStatusHandler(mcpBroker, *logger)

	// Add a server
	brokerImpl, ok := mcpBroker.(*mcpBrokerImpl)
	require.True(t, ok)
	brokerImpl.mcpServers["dummyServer:test_:http://test.local/mcp"] = createTestManagerForStatus(t,
		"dummyServer",
		[]mcp.Tool{{Name: "dummyTool"}},
	)

	w := httptest.NewRecorder()
	sh.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/status", nil))
	res := w.Result()
	require.Equal(t, 200, res.StatusCode)
	data, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	m := make(map[string]interface{})
	err = json.Unmarshal(data, &m)
	require.NoError(t, err)
}

func TestStatusHandlerReturnsAnnotatedTools(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mcpBroker := NewBroker(logger)
	sh := NewStatusHandler(mcpBroker, *logger)

	readOnly := true
	destructive := false

	manager := createTestManagerForStatus(t,
		"dummyServer",
		[]mcp.Tool{{Name: "dummyTool"}},
	)
	manager.SetStatusForTesting(upstream.ServerValidationStatus{
		Name:  "dummyServer",
		Ready: true,
		AnnotatedTools: []upstream.ToolHints{
			{
				Name:            "dummyTool",
				ReadOnlyHint:    &readOnly,
				DestructiveHint: &destructive,
			},
		},
	})

	brokerImpl, ok := mcpBroker.(*mcpBrokerImpl)
	require.True(t, ok)
	brokerImpl.mcpServers["dummyServer:test_:http://test.local/mcp"] = manager

	w := httptest.NewRecorder()
	sh.ServeHTTP(w, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/status", nil))
	res := w.Result()
	require.Equal(t, 200, res.StatusCode)

	var status StatusResponse
	err := json.NewDecoder(res.Body).Decode(&status)
	require.NoError(t, err)
	require.Len(t, status.Servers, 1)
	require.Len(t, status.Servers[0].AnnotatedTools, 1)
	require.Equal(t, "dummyTool", status.Servers[0].AnnotatedTools[0].Name)
	require.True(t, *status.Servers[0].AnnotatedTools[0].ReadOnlyHint)
	require.False(t, *status.Servers[0].AnnotatedTools[0].DestructiveHint)
}
