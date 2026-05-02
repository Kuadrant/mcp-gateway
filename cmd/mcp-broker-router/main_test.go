package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

type stubBroker struct {
	status broker.StatusResponse
}

func (s *stubBroker) ValidateAllServers() broker.StatusResponse { return s.status }
func (s *stubBroker) GetServerInfo(_ string) (*config.MCPServer, error) {
	panic("unused")
}
func (s *stubBroker) MCPServer() *mcpserver.MCPServer { panic("unused") }
func (s *stubBroker) RegisteredMCPServers() map[config.UpstreamMCPID]*upstream.MCPManager {
	panic("unused")
}
func (s *stubBroker) GetVirtualSeverByHeader(_ string) (config.VirtualServer, error) {
	panic("unused")
}
func (s *stubBroker) HandleStatusRequest(_ http.ResponseWriter, _ *http.Request) { panic("unused") }
func (s *stubBroker) Shutdown(_ context.Context) error                           { panic("unused") }
func (s *stubBroker) OnConfigChange(_ context.Context, _ *config.MCPServersConfig) {
	panic("unused")
}
func (s *stubBroker) ToolAnnotations(_ config.UpstreamMCPID, _ string) (mcp.ToolAnnotation, bool) {
	return mcp.ToolAnnotation{}, false
}

func TestReadyzHandler(t *testing.T) {
	tests := []struct {
		name       string
		status     broker.StatusResponse
		wantStatus int
	}{
		{
			name:       "no servers configured",
			status:     broker.StatusResponse{TotalServers: 0, HealthyServers: 0},
			wantStatus: http.StatusOK,
		},
		{
			name:       "servers configured, none healthy yet",
			status:     broker.StatusResponse{TotalServers: 2, HealthyServers: 0, UnHealthyServers: 2},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "servers configured, partial healthy",
			status:     broker.StatusResponse{TotalServers: 2, HealthyServers: 1, UnHealthyServers: 1},
			wantStatus: http.StatusOK,
		},
		{
			name:       "all servers healthy",
			status:     broker.StatusResponse{TotalServers: 3, HealthyServers: 3},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := readyzHandler(&stubBroker{status: tt.status})
			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Errorf("readyzHandler() status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}
