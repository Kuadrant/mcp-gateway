package upstream

import (
	"log/slog"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPServer_SupportedVersions(t *testing.T) {
	tests := []struct {
		name              string
		negotiatedVersion string
		expectedVersions  []string
	}{
		{
			name:              "2026 server supports only negotiated version",
			negotiatedVersion: "2026-07-28",
			expectedVersions:  []string{"2026-07-28"},
		},
		{
			name:              "2025 server supports only 2025",
			negotiatedVersion: "2025-11-25",
			expectedVersions:  []string{"2025-11-25"},
		},
		{
			name:              "older server supports only its version",
			negotiatedVersion: "2024-11-05",
			expectedVersions:  []string{"2024-11-05"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.MCPServer{
				Name:  "test-server",
				URL:   "http://test",
				State: "enabled",
			}

			up := NewUpstreamMCP(cfg, "", slog.Default())

			// simulate what Connect does: set init and supportedVersions
			up.init = &mcp.InitializeResult{
				ProtocolVersion: tt.negotiatedVersion,
				Capabilities:    &mcp.ServerCapabilities{},
			}
			up.supportedVersions = []string{up.init.ProtocolVersion}

			versions := up.SupportedVersions()
			if len(versions) != len(tt.expectedVersions) {
				t.Errorf("got %d versions, want %d", len(versions), len(tt.expectedVersions))
				return
			}

			for i, v := range versions {
				if v != tt.expectedVersions[i] {
					t.Errorf("version[%d] = %q, want %q", i, v, tt.expectedVersions[i])
				}
			}

			// test SupportsVersion
			for _, v := range tt.expectedVersions {
				if !up.SupportsVersion(v) {
					t.Errorf("SupportsVersion(%q) = false, want true", v)
				}
			}

			// test that it doesn't support other versions
			if up.SupportsVersion("9999-99-99") {
				t.Error("SupportsVersion(9999-99-99) = true, want false")
			}
		})
	}
}

func TestMCPServer_SupportedVersions_NotConnected(t *testing.T) {
	cfg := &config.MCPServer{
		Name:  "test-server",
		URL:   "http://test",
		State: "enabled",
	}

	up := NewUpstreamMCP(cfg, "", slog.Default())

	versions := up.SupportedVersions()
	if versions != nil {
		t.Errorf("SupportedVersions() before connect = %v, want nil", versions)
	}

	if up.SupportsVersion("2025-11-25") {
		t.Error("SupportsVersion before connect = true, want false")
	}
}
