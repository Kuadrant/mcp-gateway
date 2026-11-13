//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// brokerVersionSeries sums a broker metric by protocol_version label.
func brokerVersionSeries(ctx context.Context, name string) map[string]float64 {
	body, err := GetBrokerMetrics(ctx)
	Expect(err).NotTo(HaveOccurred())
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	Expect(err).NotTo(HaveOccurred())
	out := map[string]float64{}
	mf, ok := families[name]
	if !ok {
		return out
	}
	for _, m := range mf.GetMetric() {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == "protocol_version" {
				out[lp.GetValue()] += m.GetCounter().GetValue() + m.GetGauge().GetValue()
			}
		}
	}
	return out
}

// statefulRequests sums the labels a 2025-era client can land in.
func statefulRequests(series map[string]float64) float64 {
	return series["2025-11-25"] + series["older"]
}

var _ = Describe("Protocol version telemetry", func() {
	It("[Happy] broker reports client requests and upstreams by protocol version", func(ctx SpecContext) {
		// counters are shared with parallel specs, so assert on growth only
		before := brokerVersionSeries(ctx, "mcp_router_client_requests_total")

		stateful, err := NewStatefulClient(ctx, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = stateful.Close() })
		_, err = stateful.ListTools(ctx, nil)
		Expect(err).NotTo(HaveOccurred())

		stateless, err := NewStatelessClient(ctx, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { _ = stateless.Close() })
		_, err = stateless.ListTools(ctx, nil)
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			after := brokerVersionSeries(ctx, "mcp_router_client_requests_total")
			g.Expect(statefulRequests(after)).To(BeNumerically(">", statefulRequests(before)))
			g.Expect(after["2026-07-28"]).To(BeNumerically(">", before["2026-07-28"]))
		}).WithContext(ctx).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())

		upstreams := brokerVersionSeries(ctx, "mcp_broker_upstream_servers")
		var total float64
		for _, v := range upstreams {
			total += v
		}
		Expect(total).To(BeNumerically(">=", 1), "at least one registered upstream is reported")
		Expect(upstreams).To(HaveKey("unknown"), "every bounded label is reported, including zeros")
	})
})
