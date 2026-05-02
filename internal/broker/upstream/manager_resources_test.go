package upstream

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/yosida95/uritemplate/v3"
)

// MockResourcesAdderDeleter implements ResourcesAdderDeleter for testing.
type MockResourcesAdderDeleter struct {
	resources         map[string]*server.ServerResource
	templates         []server.ServerResourceTemplate
	addCalls          int
	delCalls          int
	addTemplateCalls  int
}

func newMockResourcesAdderDeleter() *MockResourcesAdderDeleter {
	return &MockResourcesAdderDeleter{
		resources: make(map[string]*server.ServerResource),
	}
}

func (m *MockResourcesAdderDeleter) AddResources(resources ...server.ServerResource) {
	m.addCalls++
	for i := range resources {
		m.resources[resources[i].Resource.URI] = &resources[i]
	}
}

func (m *MockResourcesAdderDeleter) DeleteResources(uris ...string) {
	m.delCalls++
	for _, u := range uris {
		delete(m.resources, u)
	}
}

func (m *MockResourcesAdderDeleter) AddResourceTemplates(templates ...server.ServerResourceTemplate) {
	m.addTemplateCalls++
	m.templates = append(m.templates, templates...)
}

func TestPrefixedURI(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		uri    string
		want   string
	}{
		{"empty prefix passes through", "", "file:///x", "file:///x"},
		{"hierarchical uri", "weather_", "file:///x", "weather_+file:///x"},
		{"opaque uri", "demo_", "embedded:info", "demo_+embedded:info"},
		{"uri with query and fragment", "p_", "https://example.com/a?b=c#d", "p_+https://example.com/a?b=c#d"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, prefixedURI(c.prefix, c.uri))
		})
	}
}

func TestStripURIPrefix(t *testing.T) {
	cases := []struct {
		name      string
		prefix    string
		input     string
		wantURI   string
		wantMatch bool
	}{
		{"empty prefix is pass-through", "", "file:///x", "file:///x", true},
		{"matching prefix is stripped", "weather_", "weather_+file:///x", "file:///x", true},
		{"opaque uri matching prefix", "demo_", "demo_+embedded:info", "embedded:info", true},
		{"non-matching prefix returns false", "weather_", "other_+file:///x", "", false},
		{"missing colon returns false", "weather_", "weather_no_scheme", "", false},
		{"unprefixed uri with non-empty prefix returns false", "weather_", "file:///x", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := stripURIPrefix(c.prefix, c.input)
			assert.Equal(t, c.wantMatch, ok)
			assert.Equal(t, c.wantURI, got)
		})
	}
}

// TestStripURIPrefix_RoundTrip is a property-style check that prefixedURI and
// stripURIPrefix are inverses for any non-empty prefix and well-formed URI.
func TestStripURIPrefix_RoundTrip(t *testing.T) {
	prefixes := []string{"", "weather_", "demo_", "x-y_"}
	uris := []string{"file:///x", "embedded:info", "https://example.com/a?b=c#d", "custom-scheme:opaque-payload"}
	for _, p := range prefixes {
		for _, u := range uris {
			federated := prefixedURI(p, u)
			back, ok := stripURIPrefix(p, federated)
			assert.True(t, ok, "round-trip failed for prefix=%q uri=%q federated=%q", p, u, federated)
			assert.Equal(t, u, back, "round-trip mismatch for prefix=%q uri=%q", p, u)
		}
	}
}

func TestMCPManager_GetServedManagedResource(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test", "weather_")
	manager := NewUpstreamMCPManager(mock, nil, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)

	manager.SetResourcesForTesting([]mcp.Resource{
		{URI: "file:///forecast.json", Name: "forecast"},
		{URI: "embedded:info", Name: "info"},
	})

	got := manager.GetServedManagedResource("weather_+file:///forecast.json")
	if assert.NotNil(t, got) {
		assert.Equal(t, "file:///forecast.json", got.URI)
	}

	// unprefixed (upstream) URI is not the served form and must miss
	assert.Nil(t, manager.GetServedManagedResource("file:///forecast.json"))

	// unknown URI misses
	assert.Nil(t, manager.GetServedManagedResource("weather_+file:///nope"))
}

func TestMCPManager_GetManagedResources_ReturnsCopy(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test", "p_")
	manager := NewUpstreamMCPManager(mock, nil, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)

	manager.SetResourcesForTesting([]mcp.Resource{{URI: "file:///x", Name: "x"}})

	got := manager.GetManagedResources()
	got[0].Name = "mutated"

	again := manager.GetManagedResources()
	assert.Equal(t, "x", again[0].Name)
}

func TestMCPManager_diffResources(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("test", "weather_")
	manager := NewUpstreamMCPManager(mock, nil, nil, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)

	old := []mcp.Resource{
		{URI: "file:///a", Name: "a"},
		{URI: "file:///b", Name: "b"},
	}
	now := []mcp.Resource{
		{URI: "file:///b", Name: "b"},
		{URI: "file:///c", Name: "c"},
	}

	added, removed := manager.diffResources(old, now)

	if assert.Len(t, added, 1) {
		assert.Equal(t, "weather_+file:///c", added[0].Resource.URI)
	}
	if assert.Len(t, removed, 1) {
		assert.Equal(t, "weather_+file:///a", removed[0])
	}
}

func TestMCPManager_manageResources_NoSupport(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("no-resources", "")
	mock.hasResourcesCap = false
	gateway := newMockResourcesAdderDeleter()
	manager := NewUpstreamMCPManager(mock, nil, gateway, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)

	r, tpl, err := manager.manageResources(context.Background(), eventTypeTimer)
	assert.NoError(t, err)
	assert.Equal(t, 0, r)
	assert.Equal(t, 0, tpl)
	assert.Empty(t, gateway.resources)
	assert.Zero(t, gateway.addCalls)
}

func TestMCPManager_manageResources_FederatesAndPrefixes(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("weather", "weather_")
	mock.hasResourcesCap = true
	mock.resources = []mcp.Resource{
		{URI: "file:///forecast.json", Name: "forecast"},
		{URI: "embedded:info", Name: "info"},
	}
	tpl, err := uritemplate.New("file:///{name}.json")
	if err != nil {
		t.Fatalf("failed to create test template: %v", err)
	}
	mock.resourceTemplates = []mcp.ResourceTemplate{
		{
			URITemplate: &mcp.URITemplate{Template: tpl},
			Name:        "by-name",
		},
	}
	gateway := newMockResourcesAdderDeleter()
	manager := NewUpstreamMCPManager(mock, nil, gateway, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)

	count, tplCount, err := manager.manageResources(context.Background(), eventTypeTimer)
	assert.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.Equal(t, 1, tplCount)

	// gateway should hold the prefixed URIs
	assert.Contains(t, gateway.resources, "weather_+file:///forecast.json")
	assert.Contains(t, gateway.resources, "weather_+embedded:info")
	if assert.Len(t, gateway.templates, 1) {
		raw := gateway.templates[0].Template.URITemplate.Template.Raw()
		assert.Equal(t, "weather_+file:///{name}.json", raw)
	}

	// served map keys must be the federated form
	assert.NotNil(t, manager.GetServedManagedResource("weather_+file:///forecast.json"))
}

func TestMCPManager_manageResources_PropagatesListError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("err", "err_")
	mock.hasResourcesCap = true
	mock.listResourcesErr = errors.New("upstream boom")
	gateway := newMockResourcesAdderDeleter()
	manager := NewUpstreamMCPManager(mock, nil, gateway, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)

	_, _, err := manager.manageResources(context.Background(), eventTypeTimer)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to list resources")
}

func TestMCPManager_removeAllResources_ClearsGateway(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	mock := newMockMCP("weather", "weather_")
	mock.hasResourcesCap = true
	mock.resources = []mcp.Resource{{URI: "file:///x", Name: "x"}}
	gateway := newMockResourcesAdderDeleter()
	manager := NewUpstreamMCPManager(mock, nil, gateway, logger, 0, mcpv1alpha1.InvalidToolPolicyFilterOut)

	_, _, err := manager.manageResources(context.Background(), eventTypeTimer)
	assert.NoError(t, err)
	assert.Len(t, gateway.resources, 1)

	manager.removeAllResources()
	assert.Empty(t, gateway.resources)
	assert.Empty(t, manager.GetManagedResources())
}
