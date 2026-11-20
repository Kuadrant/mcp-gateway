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
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// brokerVersionSeries sums a broker metric by protocol_version label.
func brokerVersionSeries(ctx context.Context, g Gomega, name string) map[string]float64 {
	body, err := GetBrokerMetrics(ctx)
	g.Expect(err).NotTo(HaveOccurred())
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(body))
	g.Expect(err).NotTo(HaveOccurred())
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
	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			DumpClusterState(ctx, SystemNamespace, GatewayNamespace, TestServerNameSpace)
		}
	})

	It("[Happy] broker reports client requests and upstreams by protocol version", func(ctx SpecContext) {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)

		// the suite removes all registrations up front, so register an upstream
		// for this spec rather than relying on other specs
		By("Registering an upstream server")
		registration := NewTestResources("protocol-telemetry", k8sClient).
			ForInternalService(sharedMCPTestServer1, 9090).
			WithPrefix("telemetry_").
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Checking the broker reports the upstream by protocol version")
		// other specs register and remove servers in parallel, so only a lower
		// bound is stable
		Eventually(func(g Gomega) {
			upstreams := brokerVersionSeries(ctx, g, "mcp_broker_upstream_servers")
			var total float64
			for _, v := range upstreams {
				total += v
			}
			g.Expect(total).To(BeNumerically(">=", 1), "the registered upstream is reported")
			for _, l := range []string{"2026-07-28", "2025-11-25", "older", "unknown"} {
				g.Expect(upstreams).To(HaveKey(l), "every bounded label is reported, including zeros")
			}
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Sending requests from a stateful and a stateless client")
		// counters are shared with parallel specs, so assert on growth only
		var before map[string]float64
		Eventually(func(g Gomega) {
			before = brokerVersionSeries(ctx, g, "mcp_router_client_requests_total")
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

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

		By("Checking the broker counted both protocol versions")
		Eventually(func(g Gomega) {
			after := brokerVersionSeries(ctx, g, "mcp_router_client_requests_total")
			g.Expect(statefulRequests(after)).To(BeNumerically(">", statefulRequests(before)))
			g.Expect(after["2026-07-28"]).To(BeNumerically(">", before["2026-07-28"]))
		}).WithContext(ctx).WithTimeout(30 * time.Second).WithPolling(2 * time.Second).Should(Succeed())
	})
})
