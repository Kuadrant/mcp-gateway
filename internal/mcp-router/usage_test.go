package mcprouter

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Kuadrant/mcp-gateway/internal/protocol"
	"github.com/Kuadrant/mcp-gateway/internal/routing"
)

func TestRecordServerUse(t *testing.T) {
	initialize := &routing.MCPRequest{Method: "initialize", Params: map[string]any{"protocolVersion": protocol.Version2025}}
	call := &routing.MCPRequest{Method: "tools/call"}

	recordServerUse("ns/a", protocol.Version2026, "", call)
	recordServerUse("ns/a", protocol.Version2026, "", call)
	// before the header is negotiated, a 2025 initialize carries the version in params
	recordServerUse("ns/b", "", "", initialize)
	// no declared version counts as the default
	recordServerUse("ns/b", "", "", call)
	// unknown versions, the broker itself and hairpin initializes are not recorded
	recordServerUse("ns/c", "1999-01-01", "", call)
	recordServerUse("", protocol.Version2026, "", call)
	recordServerUse("ns/d", protocol.Version2025, "upstream.example", initialize)

	got := UsedProtocolVersions()
	require.Equal(t, []string{protocol.Version2026}, got["ns/a"])
	require.ElementsMatch(t, []string{protocol.Version2025, protocol.DefaultClientVersion}, got["ns/b"])
	require.NotContains(t, got, "ns/c")
	require.NotContains(t, got, "ns/d")
	require.NotContains(t, got, "")
}

func BenchmarkRecordServerUseSeen(b *testing.B) {
	call := &routing.MCPRequest{Method: "tools/call"}
	recordServerUse("ns/bench", protocol.Version2026, "", call)
	b.ReportAllocs()
	for b.Loop() {
		recordServerUse("ns/bench", protocol.Version2026, "", call)
	}
}
