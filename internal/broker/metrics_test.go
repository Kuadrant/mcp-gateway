package broker

import (
	"context"
	"log/slog"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/protocol"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type disabledServer struct{ mockActiveServer }

func (d *disabledServer) IsEnabled() bool { return false }

func TestNewestVersion(t *testing.T) {
	if got := newestVersion(nil); got != "" {
		t.Errorf("got %q", got)
	}
	if got := newestVersion([]string{protocol.Version2026, "2025-03-26", protocol.Version2025}); got != protocol.Version2026 {
		t.Errorf("got %q", got)
	}
}

func TestUpstreamVersionMetrics(t *testing.T) {
	prev := otel.GetMeterProvider()
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	b := &mcpBrokerImpl{
		logger: slog.Default(),
		mcpServers: map[config.UpstreamMCPID]upstream.ActiveMCPServer{
			"a": &mockActiveServer{supportedVersions: []string{protocol.Version2025, protocol.Version2026}},
			"b": &mockActiveServer{supportedVersions: []string{protocol.Version2025}},
			"c": &mockActiveServer{supportedVersions: []string{"2025-03-26"}},
			"d": &mockActiveServer{},
			"e": &disabledServer{mockActiveServer{supportedVersions: []string{protocol.Version2025}}},
		},
	}
	if err := b.registerUpstreamVersionMetrics(); err != nil {
		t.Fatal(err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != UpstreamServersMetric {
				continue
			}
			for _, dp := range m.Data.(metricdata.Gauge[int64]).DataPoints {
				v, _ := dp.Attributes.Value(attribute.Key("protocol_version"))
				got[v.AsString()] = dp.Value
			}
		}
	}
	want := map[string]int64{
		protocol.Version2026:  1,
		protocol.Version2025:  1,
		protocol.LabelOlder:   1,
		protocol.LabelUnknown: 1,
	}
	if len(got) != len(protocol.MetricLabels()) {
		t.Errorf("expected a point for every label, got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("protocol_version=%s: got %d, want %d", k, got[k], v)
		}
	}
}
