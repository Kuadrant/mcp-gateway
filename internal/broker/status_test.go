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

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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
		Name:   serverName,
		Prefix: "test_",
		URL:    "http://test.local/mcp",
	}, "", nil)
	manager, err := upstream.NewUpstreamMCPManager(mcpServer, newMockGateway(), nil, slog.Default(), 0, upstream.InvalidToolPolicyFilterOut)
	require.NoError(t, err)
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
	brokerImpl.mcpServers["dummyServer:test_:http://test.local/mcp"] = upstream.NewActiveForTesting(createTestManagerForStatus(t,
		"dummyServer",
		[]mcp.Tool{{Name: "dummyTool"}},
	))

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
	brokerImpl.mcpServers["dummyServer:test_:http://test.local/mcp"] = upstream.NewActiveForTesting(createTestManagerForStatus(t,
		"dummyServer",
		[]mcp.Tool{{Name: "dummyTool"}},
	))

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

func TestValidateAllServers(t *testing.T) {
	tests := []struct {
		name             string
		setup            func(b *mcpBrokerImpl)
		wantTotal        int
		wantHealthy      int
		wantUnhealthy    int
		wantOverallValid bool
	}{
		{
			name:             "no servers",
			setup:            func(_ *mcpBrokerImpl) {},
			wantTotal:        0,
			wantHealthy:      0,
			wantUnhealthy:    0,
			wantOverallValid: true,
		},
		{
			name: "one unhealthy server",
			setup: func(b *mcpBrokerImpl) {
				mgr := createTestManagerForStatus(t, "s1", nil)
				mgr.SetStatusForTesting(upstream.ServerValidationStatus{Name: "s1", Ready: false})
				b.mcpServers["s1"] = upstream.NewActiveForTesting(mgr)
			},
			wantTotal:        1,
			wantHealthy:      0,
			wantUnhealthy:    1,
			wantOverallValid: false,
		},
		{
			name: "one healthy server",
			setup: func(b *mcpBrokerImpl) {
				mgr := createTestManagerForStatus(t, "s1", nil)
				mgr.SetStatusForTesting(upstream.ServerValidationStatus{Name: "s1", Ready: true})
				b.mcpServers["s1"] = upstream.NewActiveForTesting(mgr)
			},
			wantTotal:        1,
			wantHealthy:      1,
			wantUnhealthy:    0,
			wantOverallValid: true,
		},
		{
			name: "mixed healthy and unhealthy",
			setup: func(b *mcpBrokerImpl) {
				m1 := createTestManagerForStatus(t, "s1", nil)
				m1.SetStatusForTesting(upstream.ServerValidationStatus{Name: "s1", Ready: true})
				b.mcpServers["s1"] = upstream.NewActiveForTesting(m1)
				m2 := createTestManagerForStatus(t, "s2", nil)
				m2.SetStatusForTesting(upstream.ServerValidationStatus{Name: "s2", Ready: false})
				b.mcpServers["s2"] = upstream.NewActiveForTesting(m2)
			},
			wantTotal:        2,
			wantHealthy:      1,
			wantUnhealthy:    1,
			wantOverallValid: false,
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBroker(logger).(*mcpBrokerImpl)
			tt.setup(b)
			resp := b.ValidateAllServers()
			require.Equal(t, tt.wantTotal, resp.TotalServers)
			require.Equal(t, tt.wantHealthy, resp.HealthyServers)
			require.Equal(t, tt.wantUnhealthy, resp.UnHealthyServers)
			require.Equal(t, tt.wantOverallValid, resp.OverallValid)
			require.Len(t, resp.Servers, tt.wantTotal)
		})
	}
}

func TestValidateSingleServer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	t.Run("server not found when empty", func(t *testing.T) {
		b := NewBroker(logger).(*mcpBrokerImpl)
		status, ok := b.ValidateSingleServer("nonexistent")
		require.False(t, ok)
		require.Empty(t, status.Name)
	})

	t.Run("server found", func(t *testing.T) {
		b := NewBroker(logger).(*mcpBrokerImpl)
		mgr := createTestManagerForStatus(t, "test-server", nil)
		mgr.SetStatusForTesting(upstream.ServerValidationStatus{Name: "test-server", Ready: true})
		b.mcpServers["test-server"] = upstream.NewActiveForTesting(mgr)

		status, ok := b.ValidateSingleServer("test-server")
		require.True(t, ok)
		require.Equal(t, "test-server", status.Name)
		require.True(t, status.Ready)

		_, ok = b.ValidateSingleServer("other-server")
		require.False(t, ok)
	})

	t.Run("multiple servers returns matching one", func(t *testing.T) {
		b := NewBroker(logger).(*mcpBrokerImpl)
		m1 := createTestManagerForStatus(t, "s1", nil)
		m1.SetStatusForTesting(upstream.ServerValidationStatus{Name: "s1", Ready: true})
		b.mcpServers["s1"] = upstream.NewActiveForTesting(m1)

		m2 := createTestManagerForStatus(t, "s2", nil)
		m2.SetStatusForTesting(upstream.ServerValidationStatus{Name: "s2", Ready: false})
		b.mcpServers["s2"] = upstream.NewActiveForTesting(m2)

		s1Status, ok := b.ValidateSingleServer("s1")
		require.True(t, ok)
		require.Equal(t, "s1", s1Status.Name)
		require.True(t, s1Status.Ready)

		s2Status, ok := b.ValidateSingleServer("s2")
		require.True(t, ok)
		require.Equal(t, "s2", s2Status.Name)
		require.False(t, s2Status.Ready)
	})
}
