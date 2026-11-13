package broker

import (
	"context"
	"fmt"
	"slices"

	"github.com/Kuadrant/mcp-gateway/internal/protocol"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// UpstreamServersMetric reports enabled upstream servers by the newest MCP
// protocol version each one supports.
const UpstreamServersMetric = "mcp_broker_upstream_servers"

// WithUpstreamVersionMetrics registers the UpstreamServersMetric gauge. off by
// default so tests constructing many brokers do not leak callbacks.
func WithUpstreamVersionMetrics() Option {
	return func(m *mcpBrokerImpl) {
		m.upstreamVersionMetrics = true
	}
}

// newestVersion returns the latest of the given protocol versions. MCP
// versions are ISO dates so lexical order is chronological.
func newestVersion(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	return slices.Max(versions)
}

// upstreamVersionCounts counts enabled upstreams per bounded version label.
func (m *mcpBrokerImpl) upstreamVersionCounts() map[string]int64 {
	m.mcpLock.RLock()
	defer m.mcpLock.RUnlock()
	counts := make(map[string]int64, len(m.mcpServers))
	for _, srv := range m.mcpServers {
		if !srv.IsEnabled() {
			continue
		}
		counts[protocol.MetricLabel(newestVersion(srv.SupportedVersions()))]++
	}
	return counts
}

func (m *mcpBrokerImpl) registerUpstreamVersionMetrics() error {
	meter := otel.GetMeterProvider().Meter("mcp-broker")
	gauge, err := meter.Int64ObservableGauge(UpstreamServersMetric,
		metric.WithDescription("enabled upstream MCP servers by newest supported protocol version"),
	)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", UpstreamServersMetric, err)
	}
	labels := protocol.MetricLabels()
	opts := make(map[string]metric.ObserveOption, len(labels))
	for _, l := range labels {
		opts[l] = metric.WithAttributeSet(attribute.NewSet(attribute.String("protocol_version", l)))
	}
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		counts := m.upstreamVersionCounts()
		// report every label so a version dropping to zero is visible
		for _, l := range labels {
			o.ObserveInt64(gauge, counts[l], opts[l])
		}
		return nil
	}, gauge)
	return err
}
