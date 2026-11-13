package mcprouter

import (
	"context"
	"sync"

	"github.com/Kuadrant/mcp-gateway/internal/protocol"
	"github.com/Kuadrant/mcp-gateway/internal/routing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ClientRequestsMetric counts client MCP requests by declared protocol version.
const ClientRequestsMetric = "mcp_router_client_requests"

type routerMetrics struct {
	clientRequests metric.Int64Counter
	// prebuilt per label so the hot path does not allocate
	versionOpts map[string]metric.AddOption
}

var (
	metricsOnce sync.Once
	metricsInst *routerMetrics
)

func getRouterMetrics() *routerMetrics {
	metricsOnce.Do(func() {
		m := &routerMetrics{versionOpts: map[string]metric.AddOption{}}
		counter, err := otel.GetMeterProvider().Meter("mcp-router").Int64Counter(ClientRequestsMetric,
			metric.WithDescription("client MCP requests by declared protocol version"),
		)
		if err != nil {
			otel.Handle(err)
		}
		m.clientRequests = counter
		for _, l := range protocol.MetricLabels() {
			m.versionOpts[l] = metric.WithAttributeSet(attribute.NewSet(attribute.String("protocol_version", l)))
		}
		metricsInst = m
	})
	return metricsInst
}

// clientProtocolVersion returns the version a client is speaking: the
// MCP-Protocol-Version header, or params.protocolVersion on a 2025 initialize
// which is sent before the header is negotiated.
func clientProtocolVersion(header string, req *routing.MCPRequest) string {
	if header != "" {
		return header
	}
	if req != nil && req.Method == "initialize" {
		if v, ok := req.Params["protocolVersion"].(string); ok {
			return v
		}
	}
	return ""
}

// recordClientRequest counts a client request. hairpin initializes sent by the
// router itself carry mcp-init-host and are skipped.
func recordClientRequest(ctx context.Context, header, initHost string, req *routing.MCPRequest) {
	if initHost != "" {
		return
	}
	m := getRouterMetrics()
	if m.clientRequests == nil {
		return
	}
	m.clientRequests.Add(ctx, 1, m.versionOpts[protocol.ClientMetricLabel(clientProtocolVersion(header, req))])
}
