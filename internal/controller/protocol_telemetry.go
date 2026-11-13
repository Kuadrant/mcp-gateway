package controller

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/protocol"
)

// +kubebuilder:rbac:groups="",resources=pods,verbs=list

// broker-side metric names as exposed by the OTel prometheus exporter
const (
	brokerClientRequestsMetric  = "mcp_router_client_requests_total"
	brokerUpstreamServersMetric = "mcp_broker_upstream_servers"
	protocolVersionLabel        = "protocol_version"

	// cap on a scraped metrics body; the broker exposes a handful of families
	maxBrokerMetricsBytes = 4 << 20
)

var (
	clientRequestsByVersion = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "mcp_gateway_client_requests_total",
		Help: "Client MCP requests across all gateways in the cluster by declared protocol version",
	}, []string{protocolVersionLabel})

	upstreamServersByVersion = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "mcp_gateway_upstream_servers",
		Help: "Enabled upstream MCP servers across all gateways in the cluster by newest supported protocol version",
	}, []string{protocolVersionLabel})

	telemetryScrapeErrors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "mcp_gateway_telemetry_scrape_errors_total",
		Help: "Failed scrapes of broker metrics endpoints by the protocol version collector",
	})
)

func init() {
	ctrlmetrics.Registry.MustRegister(clientRequestsByVersion, upstreamServersByVersion, telemetryScrapeErrors)
	for _, l := range protocol.MetricLabels() {
		clientRequestsByVersion.WithLabelValues(l)
		upstreamServersByVersion.WithLabelValues(l)
	}
}

// brokerSample is the protocol version data scraped from one broker pod.
type brokerSample struct {
	clientRequests  map[string]float64
	upstreamServers map[string]float64
}

// podTarget is a broker pod to scrape.
type podTarget struct {
	uid       types.UID
	namespace string
	addr      string
	started   time.Time
}

// ProtocolTelemetryCollector periodically scrapes broker pods and publishes
// cluster-wide protocol version usage on the controller metrics endpoint, which
// sits in the operator namespace where platform monitoring can scrape it.
type ProtocolTelemetryCollector struct {
	// Reader must be uncached: listing pods through the manager cache would
	// start a cluster-wide pod informer
	Reader   client.Reader
	Logger   *slog.Logger
	Interval time.Duration
	HTTP     *http.Client

	// scrape is overridable in tests
	scrape func(ctx context.Context, addr string) (*brokerSample, error)

	startTime time.Time
	// last client request counter values per pod, for delta accounting
	lastClient map[types.UID]map[string]float64
}

var _ manager.Runnable = &ProtocolTelemetryCollector{}
var _ manager.LeaderElectionRunnable = &ProtocolTelemetryCollector{}

// NeedLeaderElection keeps a single collector active so counts are not duplicated.
func (c *ProtocolTelemetryCollector) NeedLeaderElection() bool { return true }

// Start runs the collection loop until ctx is cancelled.
func (c *ProtocolTelemetryCollector) Start(ctx context.Context) error {
	if c.Interval <= 0 {
		c.Interval = 5 * time.Minute
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if c.scrape == nil {
		c.scrape = c.scrapeBroker
	}
	c.startTime = time.Now()
	c.lastClient = map[types.UID]map[string]float64{}
	c.Logger.Info("starting protocol version telemetry collector", "interval", c.Interval)

	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	for {
		c.collect(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (c *ProtocolTelemetryCollector) collect(ctx context.Context) {
	targets, err := c.targets(ctx)
	if err != nil {
		c.Logger.Error("protocol telemetry: failed to list broker pods", "error", err)
		return
	}

	// upstreams: replicas in a namespace share one config, so take the max per
	// namespace then sum across namespaces
	perNamespace := map[string]map[string]float64{}
	seen := make(map[types.UID]bool, len(targets))
	for i := range targets {
		t := &targets[i]
		sample, err := c.scrape(ctx, t.addr)
		if err != nil {
			telemetryScrapeErrors.Inc()
			c.Logger.Debug("protocol telemetry: scrape failed", "namespace", t.namespace, "addr", t.addr, "error", err)
			continue
		}
		seen[t.uid] = true
		c.addClientDeltas(t, sample.clientRequests)

		ns := perNamespace[t.namespace]
		if ns == nil {
			ns = map[string]float64{}
			perNamespace[t.namespace] = ns
		}
		for l, v := range sample.upstreamServers {
			ns[l] = max(ns[l], v)
		}
	}
	for uid := range c.lastClient {
		if !seen[uid] {
			delete(c.lastClient, uid)
		}
	}

	totals := map[string]float64{}
	for _, ns := range perNamespace {
		for l, v := range ns {
			totals[l] += v
		}
	}
	for _, l := range protocol.MetricLabels() {
		upstreamServersByVersion.WithLabelValues(l).Set(totals[l])
	}
	c.Logger.Debug("protocol telemetry: collected", "targets", len(targets), "scraped", len(seen))
}

// addClientDeltas adds the increase of each pod counter since the last scrape.
// a pod already running when the collector started is baselined, not added,
// so a controller restart does not re-count historical requests.
func (c *ProtocolTelemetryCollector) addClientDeltas(t *podTarget, current map[string]float64) {
	last, known := c.lastClient[t.uid]
	if !known && t.started.Before(c.startTime) {
		c.lastClient[t.uid] = current
		return
	}
	for l, v := range current {
		delta := v - last[l]
		if delta < 0 {
			// broker counter reset (container restart)
			delta = v
		}
		if delta > 0 {
			clientRequestsByVersion.WithLabelValues(l).Add(delta)
		}
	}
	c.lastClient[t.uid] = current
}

// targets lists running broker pods in namespaces that have an MCPGatewayExtension.
func (c *ProtocolTelemetryCollector) targets(ctx context.Context) ([]podTarget, error) {
	var exts mcpv1.MCPGatewayExtensionList
	if err := c.Reader.List(ctx, &exts); err != nil {
		return nil, fmt.Errorf("list MCPGatewayExtensions: %w", err)
	}
	namespaces := map[string]bool{}
	for i := range exts.Items {
		namespaces[exts.Items[i].Namespace] = true
	}
	var out []podTarget
	for ns := range namespaces {
		var pods corev1.PodList
		if err := c.Reader.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels(brokerRouterLabels())); err != nil {
			return nil, fmt.Errorf("list broker pods in %s: %w", ns, err)
		}
		for i := range pods.Items {
			p := &pods.Items[i]
			if p.Status.Phase != corev1.PodRunning || p.Status.PodIP == "" {
				continue
			}
			started := p.CreationTimestamp.Time
			if p.Status.StartTime != nil {
				started = p.Status.StartTime.Time
			}
			out = append(out, podTarget{
				uid:       p.UID,
				namespace: ns,
				addr:      net.JoinHostPort(p.Status.PodIP, strconv.Itoa(brokerMetricsPort)),
				started:   started,
			})
		}
	}
	return out, nil
}

func (c *ProtocolTelemetryCollector) scrapeBroker(ctx context.Context, addr string) (*brokerSample, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", string(expfmt.NewFormat(expfmt.TypeTextPlain)))
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return parseBrokerMetrics(io.LimitReader(resp.Body, maxBrokerMetricsBytes))
}

// parseBrokerMetrics extracts protocol version series from a broker metrics
// body. label values are re-bounded since the endpoint is unauthenticated.
func parseBrokerMetrics(r io.Reader) (*brokerSample, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(r)
	if err != nil {
		return nil, fmt.Errorf("parse metrics: %w", err)
	}
	return &brokerSample{
		clientRequests:  sumByVersion(families[brokerClientRequestsMetric]),
		upstreamServers: sumByVersion(families[brokerUpstreamServersMetric]),
	}, nil
}

func sumByVersion(mf *dto.MetricFamily) map[string]float64 {
	out := map[string]float64{}
	if mf == nil {
		return out
	}
	for _, m := range mf.GetMetric() {
		version := ""
		for _, lp := range m.GetLabel() {
			if lp.GetName() == protocolVersionLabel {
				version = lp.GetValue()
				break
			}
		}
		var v float64
		switch {
		case m.GetCounter() != nil:
			v = m.GetCounter().GetValue()
		case m.GetGauge() != nil:
			v = m.GetGauge().GetValue()
		case m.GetUntyped() != nil:
			v = m.GetUntyped().GetValue()
		}
		if v < 0 {
			continue
		}
		out[protocol.MetricLabel(version)] += v
	}
	return out
}
