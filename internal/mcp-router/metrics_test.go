package mcprouter

import (
	"context"
	"sync"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/protocol"
	"github.com/Kuadrant/mcp-gateway/internal/routing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func withManualReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	prev := otel.GetMeterProvider()
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	metricsOnce = sync.Once{}
	t.Cleanup(func() {
		otel.SetMeterProvider(prev)
		metricsOnce = sync.Once{}
	})
	return reader
}

func clientRequestCounts(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != ClientRequestsMetric {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				v, _ := dp.Attributes.Value(attribute.Key("protocol_version"))
				out[v.AsString()] += dp.Value
			}
		}
	}
	return out
}

func TestRecordClientRequest(t *testing.T) {
	reader := withManualReader(t)
	ctx := context.Background()

	initReq := &routing.MCPRequest{Method: "initialize", Params: map[string]any{"protocolVersion": "2025-06-18"}}
	recordClientRequest(ctx, "", "", initReq)
	recordClientRequest(ctx, protocol.Version2026, "", &routing.MCPRequest{Method: "tools/list"})
	recordClientRequest(ctx, protocol.Version2025, "", &routing.MCPRequest{Method: "tools/call"})
	recordClientRequest(ctx, "", "", &routing.MCPRequest{Method: "tools/list"})
	recordClientRequest(ctx, "not-a-version", "", &routing.MCPRequest{Method: "tools/list"})
	// hairpin initialize from the router itself
	recordClientRequest(ctx, "", "backend.example.com", initReq)

	got := clientRequestCounts(t, reader)
	want := map[string]int64{
		protocol.Version2026: 1,
		protocol.Version2025: 1,
		// 2025-06-18 initialize, plus a request with no version (the default)
		protocol.LabelOlder:   2,
		protocol.LabelUnknown: 1,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("protocol_version=%s: got %d, want %d", k, got[k], v)
		}
	}
}

func TestClientProtocolVersionHeaderWins(t *testing.T) {
	req := &routing.MCPRequest{Method: "initialize", Params: map[string]any{"protocolVersion": "2025-03-26"}}
	if got := clientProtocolVersion(protocol.Version2026, req); got != protocol.Version2026 {
		t.Errorf("got %q", got)
	}
}
