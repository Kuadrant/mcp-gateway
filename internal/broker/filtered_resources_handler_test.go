package broker

import (
	"context"
	"log/slog"
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
)

func newTestBroker(t *testing.T) *mcpBrokerImpl {
	t.Helper()
	b, ok := NewBroker(slog.Default()).(*mcpBrokerImpl)
	if !ok {
		t.Fatalf("NewBroker did not return *mcpBrokerImpl")
	}
	return b
}

func TestFilterResources_StripsGatewayIDMeta(t *testing.T) {
	b := newTestBroker(t)
	res := &mcp.ListResourcesResult{
		Resources: []mcp.Resource{
			{
				URI: "weather_+file:///x",
				Meta: mcp.NewMetaFromMap(map[string]any{
					"kuadrant/id": "mcp-test/weather",
					"keep-me":     "yes",
				}),
			},
		},
	}

	b.FilterResources(context.Background(), nil, &mcp.ListResourcesRequest{}, res)

	assert.Len(t, res.Resources, 1)
	meta := res.Resources[0].Meta
	if assert.NotNil(t, meta) {
		_, hasInternal := meta.AdditionalFields["kuadrant/id"]
		assert.False(t, hasInternal, "gateway-internal meta key must be stripped")
		_, keptUserField := meta.AdditionalFields["keep-me"]
		assert.True(t, keptUserField, "non-gateway meta keys must survive")
	}
}

func TestFilterResources_NormalisesNilSlice(t *testing.T) {
	b := newTestBroker(t)
	res := &mcp.ListResourcesResult{Resources: nil}
	b.FilterResources(context.Background(), nil, &mcp.ListResourcesRequest{}, res)
	assert.NotNil(t, res.Resources, "nil slice must be replaced with empty slice so wire format is [] not null")
	assert.Empty(t, res.Resources)
}

func TestFilterResourceTemplates_StripsGatewayIDMeta(t *testing.T) {
	b := newTestBroker(t)
	res := &mcp.ListResourceTemplatesResult{
		ResourceTemplates: []mcp.ResourceTemplate{
			{
				Name: "by-name",
				Meta: mcp.NewMetaFromMap(map[string]any{
					"kuadrant/id": "mcp-test/weather",
				}),
			},
		},
	}
	b.FilterResourceTemplates(context.Background(), nil, &mcp.ListResourceTemplatesRequest{}, res)
	assert.Len(t, res.ResourceTemplates, 1)
	if assert.NotNil(t, res.ResourceTemplates[0].Meta) {
		_, hasInternal := res.ResourceTemplates[0].Meta.AdditionalFields["kuadrant/id"]
		assert.False(t, hasInternal)
	}
}

func TestGetServerInfoByResourceURI(t *testing.T) {
	b := newTestBroker(t)

	weatherCfg := &config.MCPServer{
		Name:       "mcp-test/weather",
		ToolPrefix: "weather_",
		URL:        "http://weather.local/mcp",
	}
	weather := upstream.NewUpstreamMCP(weatherCfg)
	weatherMgr := upstream.NewUpstreamMCPManager(weather, nil, nil, slog.Default(), 0, mcpv1alpha1.InvalidToolPolicyFilterOut, nil, nil)
	weatherMgr.SetResourcesForTesting([]mcp.Resource{
		{URI: "file:///forecast.json", Name: "forecast"},
	})

	otherCfg := &config.MCPServer{
		Name:       "mcp-test/other",
		ToolPrefix: "other_",
		URL:        "http://other.local/mcp",
	}
	other := upstream.NewUpstreamMCP(otherCfg)
	otherMgr := upstream.NewUpstreamMCPManager(other, nil, nil, slog.Default(), 0, mcpv1alpha1.InvalidToolPolicyFilterOut, nil, nil)
	otherMgr.SetResourcesForTesting([]mcp.Resource{
		{URI: "file:///config.json", Name: "config"},
	})

	b.mcpServers[weather.ID()] = weatherMgr
	b.mcpServers[other.ID()] = otherMgr

	got, err := b.GetServerInfoByResourceURI("weather_+file:///forecast.json")
	if assert.NoError(t, err) && assert.NotNil(t, got) {
		assert.Equal(t, weatherCfg.Name, got.Name)
	}

	got, err = b.GetServerInfoByResourceURI("other_+file:///config.json")
	if assert.NoError(t, err) && assert.NotNil(t, got) {
		assert.Equal(t, otherCfg.Name, got.Name)
	}

	// the upstream form (no prefix in scheme) must not match — that is the wire form
	// the gateway never serves and would indicate a client bug.
	_, err = b.GetServerInfoByResourceURI("file:///forecast.json")
	assert.Error(t, err)

	// unknown URI errors
	_, err = b.GetServerInfoByResourceURI("weather_+file:///nope")
	assert.Error(t, err)
}
