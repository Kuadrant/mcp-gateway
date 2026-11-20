package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/protocol"
	"github.com/prometheus/client_golang/prometheus/testutil"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const sampleBrokerMetrics = `# HELP mcp_router_client_requests_total client MCP requests by declared protocol version
# TYPE mcp_router_client_requests_total counter
mcp_router_client_requests_total{otel_scope_name="mcp-router",protocol_version="2025-11-25"} 7
mcp_router_client_requests_total{otel_scope_name="mcp-router",protocol_version="2026-07-28"} 3
mcp_router_client_requests_total{otel_scope_name="mcp-router",protocol_version="injected-junk"} 2
# HELP mcp_broker_upstream_servers enabled upstream MCP servers by newest supported protocol version
# TYPE mcp_broker_upstream_servers gauge
mcp_broker_upstream_servers{otel_scope_name="mcp-broker",protocol_version="2025-11-25"} 2
mcp_broker_upstream_servers{otel_scope_name="mcp-broker",protocol_version="unknown"} 1
# TYPE mcp_broker_tools_discovered gauge
mcp_broker_tools_discovered{server_name="ns/a"} 4
`

func TestParseBrokerMetrics(t *testing.T) {
	s, err := parseBrokerMetrics(strings.NewReader(sampleBrokerMetrics))
	if err != nil {
		t.Fatal(err)
	}
	if s.clientRequests[protocol.Version2025] != 7 || s.clientRequests[protocol.Version2026] != 3 {
		t.Errorf("client requests: %v", s.clientRequests)
	}
	if s.clientRequests[protocol.LabelUnknown] != 2 || len(s.clientRequests) != 3 {
		t.Errorf("unrecognised versions must collapse to unknown: %v", s.clientRequests)
	}
	if s.upstreamServers[protocol.Version2025] != 2 || s.upstreamServers[protocol.LabelUnknown] != 1 {
		t.Errorf("upstream servers: %v", s.upstreamServers)
	}
}

func TestParseBrokerMetricsMissingFamilies(t *testing.T) {
	s, err := parseBrokerMetrics(strings.NewReader("# TYPE other gauge\nother 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.clientRequests) != 0 || len(s.upstreamServers) != 0 {
		t.Errorf("expected empty sample, got %+v", s)
	}
}

func clientCount() float64 {
	return testutil.ToFloat64(clientRequestsByVersion.WithLabelValues(protocol.Version2025))
}

func TestAddClientDeltas(t *testing.T) {
	c := &ProtocolTelemetryCollector{startTime: time.Now(), lastClient: map[types.UID]map[string]float64{}}
	v := protocol.Version2025
	before := clientCount()

	// pod running before the collector started is baselined
	old := &podTarget{uid: "old", started: c.startTime.Add(-time.Hour)}
	c.addClientDeltas(old, map[string]float64{v: 100})
	if got := clientCount() - before; got != 0 {
		t.Fatalf("baseline added %v", got)
	}
	c.addClientDeltas(old, map[string]float64{v: 110})
	if got := clientCount() - before; got != 10 {
		t.Fatalf("delta: got %v, want 10", got)
	}

	// counter reset after a container restart counts the new value
	c.addClientDeltas(old, map[string]float64{v: 4})
	if got := clientCount() - before; got != 14 {
		t.Fatalf("reset: got %v, want 14", got)
	}

	// pod started after the collector counts from zero
	fresh := &podTarget{uid: "new", started: c.startTime.Add(time.Minute)}
	c.addClientDeltas(fresh, map[string]float64{v: 5})
	if got := clientCount() - before; got != 19 {
		t.Fatalf("new pod: got %v, want 19", got)
	}
}

func TestCollect(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = mcpv1.AddToScheme(scheme)

	running := func(name, ns, ip string) client.Object {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: types.UID(ns + "/" + name), Labels: brokerRouterLabels()},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: ip},
		}
	}
	objs := []client.Object{
		&mcpv1.MCPGatewayExtension{ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "team-a"}},
		&mcpv1.MCPGatewayExtension{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "team-b"}},
		running("broker-1", "team-a", "10.0.0.1"),
		running("broker-2", "team-a", "10.0.0.2"),
		running("broker-1", "team-b", "10.0.0.3"),
		// no extension in this namespace: ignored
		running("broker-1", "rogue", "10.0.0.4"),
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "team-b", Labels: brokerRouterLabels()},
			Status:     corev1.PodStatus{Phase: corev1.PodPending},
		},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()

	samples := map[string]*brokerSample{
		"10.0.0.1:9090": {upstreamServers: map[string]float64{protocol.Version2025: 2, protocol.Version2026: 1}},
		"10.0.0.2:9090": {upstreamServers: map[string]float64{protocol.Version2025: 2, protocol.Version2026: 1}},
		"10.0.0.3:9090": {upstreamServers: map[string]float64{protocol.Version2025: 1}},
		"10.0.0.4:9090": {upstreamServers: map[string]float64{protocol.Version2025: 50}},
	}
	var scraped []string
	c := &ProtocolTelemetryCollector{
		Reader:     reader,
		Logger:     slog.New(slog.DiscardHandler),
		startTime:  time.Now(),
		lastClient: map[types.UID]map[string]float64{},
		scrape: func(_ context.Context, addr string) (*brokerSample, error) {
			scraped = append(scraped, addr)
			return samples[addr], nil
		},
	}
	c.collect(context.Background())

	if len(scraped) != 3 {
		t.Errorf("expected 3 scrapes, got %v", scraped)
	}
	// team-a replicas dedupe to 2+1, team-b adds 1
	if got := testutil.ToFloat64(upstreamServersByVersion.WithLabelValues(protocol.Version2025)); got != 3 {
		t.Errorf("2025 upstreams: got %v, want 3", got)
	}
	if got := testutil.ToFloat64(upstreamServersByVersion.WithLabelValues(protocol.Version2026)); got != 1 {
		t.Errorf("2026 upstreams: got %v, want 1", got)
	}
	if got := testutil.ToFloat64(upstreamServersByVersion.WithLabelValues(protocol.LabelUnknown)); got != 0 {
		t.Errorf("unknown upstreams: got %v, want 0", got)
	}
}

func TestCollectScrapeRecoveryKeepsCounterBaseline(t *testing.T) {
	tests := []struct {
		name    string
		started time.Time
		want    float64
	}{
		{name: "existing pod", started: time.Now().Add(-time.Hour), want: 20},
		{name: "new pod", started: time.Now().Add(time.Minute), want: 120},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, setValue, setFailure := telemetryCollectorForOnePod(t, tt.started)
			before := clientCount()
			setValue(100)
			c.collect(context.Background())
			setValue(110)
			c.collect(context.Background())
			setFailure(true)
			c.collect(context.Background())
			setFailure(false)
			setValue(120)
			c.collect(context.Background())
			if got := clientCount() - before; got != tt.want {
				t.Errorf("requests counted across failure: got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCollectUsesOneCompleteReplicaSnapshot(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := mcpv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&mcpv1.MCPGatewayExtension{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "team"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "broker-a", Namespace: "team", UID: "a", Labels: brokerRouterLabels()}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "broker-b", Namespace: "team", UID: "b", Labels: brokerRouterLabels()}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.2"}},
	).Build()
	for _, label := range protocol.MetricLabels() {
		upstreamServersByVersion.WithLabelValues(label).Set(0)
	}
	c := &ProtocolTelemetryCollector{
		Reader:     reader,
		Logger:     slog.New(slog.DiscardHandler),
		startTime:  time.Now(),
		lastClient: map[types.UID]map[string]float64{},
		scrape: func(_ context.Context, addr string) (*brokerSample, error) {
			if strings.HasSuffix(addr, "10.0.0.1:9090") {
				return &brokerSample{upstreamServers: map[string]float64{protocol.Version2026: 1}}, nil
			}
			return &brokerSample{upstreamServers: map[string]float64{protocol.LabelUnknown: 1}}, nil
		},
	}
	c.collect(context.Background())
	total := 0.0
	for _, label := range protocol.MetricLabels() {
		total += testutil.ToFloat64(upstreamServersByVersion.WithLabelValues(label))
	}
	if total != 1 {
		t.Errorf("upstream total across disagreeing replicas: got %v, want 1", total)
	}
}

func TestCollectRetainsUpstreamGaugeWhenScrapesFail(t *testing.T) {
	started := time.Now().Add(-time.Hour)
	c, _, setFailure := telemetryCollectorForOnePod(t, started)
	for _, label := range protocol.MetricLabels() {
		upstreamServersByVersion.WithLabelValues(label).Set(0)
	}
	c.collect(context.Background())
	setFailure(true)
	c.collect(context.Background())
	if got := testutil.ToFloat64(upstreamServersByVersion.WithLabelValues(protocol.Version2025)); got != 1 {
		t.Errorf("upstream gauge after failed scrape: got %v, want retained value 1", got)
	}
}

func telemetryCollectorForOnePod(t *testing.T, started time.Time) (*ProtocolTelemetryCollector, func(float64), func(bool)) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := mcpv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&mcpv1.MCPGatewayExtension{ObjectMeta: metav1.ObjectMeta{Name: "gateway", Namespace: "team"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "broker", Namespace: "team", UID: "broker", Labels: brokerRouterLabels(), CreationTimestamp: metav1.NewTime(started)},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1"},
		},
	).Build()
	value := 0.0
	failed := false
	c := &ProtocolTelemetryCollector{
		Reader:     reader,
		Logger:     slog.New(slog.DiscardHandler),
		startTime:  time.Now(),
		lastClient: map[types.UID]map[string]float64{},
		scrape: func(context.Context, string) (*brokerSample, error) {
			if failed {
				return nil, fmt.Errorf("temporary scrape failure")
			}
			return &brokerSample{
				clientRequests:  map[string]float64{protocol.Version2025: value},
				upstreamServers: map[string]float64{protocol.Version2025: 1},
			}, nil
		},
	}
	return c, func(v float64) { value = v }, func(v bool) { failed = v }
}

func TestStartBaselinesCountersFromExistingBrokerPods(t *testing.T) {
	c, setValue, _ := telemetryCollectorForOnePod(t, time.Now().Add(-time.Hour))
	c.startTime = time.Time{}
	setValue(100)
	before := clientCount()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if got := clientCount() - before; got != 0 {
		t.Errorf("initial scrape counted historical requests: got %v, want 0", got)
	}
}
