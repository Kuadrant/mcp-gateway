package upstream

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func newStatusTestManager(t *testing.T, protocolVersion string) *MCPManager {
	t.Helper()
	mock := &MockMCP{
		name:            "status-server",
		id:              config.UpstreamMCPID("ns/status-server"),
		cfg:             &config.MCPServer{Name: "status-server"},
		protocolVersion: protocolVersion,
	}
	man, err := NewUpstreamMCPManager(mock, &MockToolsAdderDeleter{}, nil, slog.Default(), 0, InvalidToolPolicyFilterOut)
	require.NoError(t, err)
	return man
}

// the expected version must be the newest version the broker proposes, not
// the negotiated one, otherwise Expected==Supported always and the signal
// that an upstream speaks an older protocol is lost.
func TestSetStatus_ExpectedVersionIsLatestKnown(t *testing.T) {
	man := newStatusTestManager(t, "2025-03-26")
	man.setStatus(nil, 1, 0, nil, nil)

	status := man.GetStatus()
	require.True(t, status.ProtocolValidation.IsValid)
	require.Equal(t, "2025-03-26", status.ProtocolValidation.SupportedVersion)
	require.Equal(t, expectedProtocolVersion, status.ProtocolValidation.ExpectedVersion)
	require.NotEqual(t, status.ProtocolValidation.SupportedVersion, status.ProtocolValidation.ExpectedVersion,
		"an old upstream must be distinguishable from a current one")
}

// pins the hard-coded expectedProtocolVersion against what the SDK client
// actually proposes in its initialize to a legacy upstream over streamable
// HTTP, which is the flow the broker's upstream connects take. fails on
// SDK bumps until the constant is updated.
func TestExpectedVersionMatchesSDKProposal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var proposed atomic.Value
	proposed.Store("")
	srv := mcp.NewServer(&mcp.Implementation{Name: "probe", Version: "0.0.1"}, nil)
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "initialize" {
				if params, ok := req.GetParams().(*mcp.InitializeParams); ok {
					proposed.Store(params.ProtocolVersion)
				}
			}
			return next(ctx, method, req)
		}
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "probe", URL: ts.URL}, "", nil)
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	require.Equal(t, expectedProtocolVersion, proposed.Load(),
		"expectedProtocolVersion drifted from the SDK client's initialize proposal")
}

// the broker's upstream client must declare roots.listChanged and
// elicitation, exactly what mark3labs sent.
func TestUpstreamClientCapabilities(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var caps atomic.Pointer[mcp.ClientCapabilities]
	srv := mcp.NewServer(&mcp.Implementation{Name: "probe", Version: "0.0.1"}, nil)
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "initialize" {
				if params, ok := req.GetParams().(*mcp.InitializeParams); ok {
					caps.Store(params.Capabilities)
				}
			}
			return next(ctx, method, req)
		}
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	up := NewUpstreamMCP(&config.MCPServer{Name: "probe", URL: ts.URL}, "", nil)
	require.NoError(t, up.Connect(ctx, func() {}))
	defer func() { _ = up.Disconnect() }()

	got := caps.Load()
	require.NotNil(t, got)
	require.True(t, got.Roots.ListChanged, "roots.listChanged must be declared as mark3labs did") //nolint:staticcheck // wire-shape assertion on the deprecated field
	require.NotNil(t, got.Elicitation)
}
