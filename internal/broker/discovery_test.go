package broker

import (
	"context"
	"log/slog"
	"net/http"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestApplyProgressiveDiscoveryFilterDisabled(t *testing.T) {
	b := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
	tools := []mcp.Tool{{Name: "a"}, {Name: "b"}}
	out := b.applyProgressiveDiscoveryFilter(context.Background(), tools)
	require.Equal(t, tools, out)
}

func TestApplyProgressiveDiscoveryFilterBelowThresholdShowsAll(t *testing.T) {
	b := NewBroker(slog.Default(),
		WithDiscoveryToolsEnabled(true),
		WithDiscoveryToolThreshold(10),
	).(*mcpBrokerImpl)
	tools := []mcp.Tool{
		{Name: ToolDiscoverTools},
		{Name: ToolSelectTools},
		{Name: "upstream_one"},
	}
	out := b.applyProgressiveDiscoveryFilter(context.Background(), tools)
	require.Len(t, out, 3)
}

func TestApplyProgressiveDiscoveryFilterAboveThresholdUnsetShowsMetaOnly(t *testing.T) {
	b := NewBroker(slog.Default(),
		WithDiscoveryToolsEnabled(true),
		WithDiscoveryToolThreshold(1),
	).(*mcpBrokerImpl)
	tools := []mcp.Tool{
		{Name: ToolDiscoverTools},
		{Name: ToolSelectTools},
		{Name: "t1"},
		{Name: "t2"},
	}
	out := b.applyProgressiveDiscoveryFilter(context.Background(), tools)
	require.Len(t, out, 2)
	require.Equal(t, ToolDiscoverTools, out[0].Name)
	require.Equal(t, ToolSelectTools, out[1].Name)
}

func TestFilterToolsDiscoveryIntegration(t *testing.T) {
	b := NewBroker(slog.Default(),
		WithDiscoveryToolsEnabled(true),
		WithDiscoveryToolThreshold(1),
	).(*mcpBrokerImpl)
	res := &mcp.ListToolsResult{Tools: []mcp.Tool{
		{Name: ToolDiscoverTools},
		{Name: ToolSelectTools},
		{Name: "x"},
		{Name: "y"},
	}}
	b.FilterTools(context.Background(), nil, &mcp.ListToolsRequest{Header: http.Header{}}, res)
	require.Len(t, res.Tools, 2)
}
