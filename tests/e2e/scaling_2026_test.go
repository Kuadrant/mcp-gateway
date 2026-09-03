//go:build e2e

package e2e

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
)

// The 2026-07-28 router is stateless and header-based: every request carries
// its own routing intent, so any replica serves any request without shared
// session state. This spec proves a 2026-only gateway scales horizontally with
// no Redis (sessionStore) by scaling the shared deployment to two replicas and
// driving repeated stateless tools/call through Envoy, asserting every call
// succeeds and no CACHE_CONNECTION_STRING is configured.
var _ = Describe("2026 horizontal scaling", func() {
	It("[Full,Protocol2026] 2026-only traffic served across replicas without Redis", Serial, func() {
		const iterations = 20
		deploymentName := GatewayName

		testResources := []client.Object{}
		deferCleanupResources(&testResources)

		By("asserting the shared gateway has no session store configured")
		ext := &mcpv1.MCPGatewayExtension{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: MCPExtensionName, Namespace: SystemNamespace}, ext)).To(Succeed())
		Expect(ext.Spec.SessionStore).To(BeNil(), "test requires a 2026-only, Redis-free gateway")

		By("asserting the broker-router deployment has no CACHE_CONNECTION_STRING env")
		dep := &appsv1.Deployment{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: deploymentName, Namespace: SystemNamespace}, dep)).To(Succeed())
		Expect(dep.Spec.Template.Spec.Containers).NotTo(BeEmpty())
		var envNames []string
		for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
			envNames = append(envNames, e.Name)
		}
		Expect(envNames).NotTo(ContainElement("CACHE_CONNECTION_STRING"),
			"a 2026-only gateway must scale without a Redis session store")

		By("registering a 2026-capable backend on the shared gateway")
		registration := NewMCPServerResourcesWithDefaults("scale-2026", k8sClient).
			WithBackendTarget("mcp-test-stateless-server", 9090).
			WithPrefix("scale2026_").
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("confirming a stateless client negotiates 2026-07-28 and sees scale2026_ tools")
		Eventually(func(g Gomega) {
			c, err := NewStatelessClient(ctx, gatewayURL)
			g.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = c.Close() }()

			init := c.InitializeResult()
			g.Expect(init).NotTo(BeNil())
			g.Expect(init.ProtocolVersion).To(Equal("2026-07-28"),
				"gateway with a 2026-capable backend must negotiate 2026-07-28")

			result, err := c.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolsPresent("scale2026_", result)).To(BeTrue())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("scaling the gateway to 2 replicas")
		gen, err := GetDeploymentGeneration(ctx, SystemNamespace, deploymentName)
		Expect(err).NotTo(HaveOccurred())
		Expect(ScaleDeployment(ctx, SystemNamespace, deploymentName, 2)).To(Succeed())
		// register scale-down now that the spec is at 2, so cleanup runs on any later failure
		DeferCleanup(func() {
			By("scaling the gateway back to 1 replica")
			downGen, dErr := GetDeploymentGeneration(ctx, SystemNamespace, deploymentName)
			Expect(dErr).NotTo(HaveOccurred())
			Expect(ScaleDeployment(ctx, SystemNamespace, deploymentName, 1)).To(Succeed())
			Expect(WaitForDeploymentReplicas(ctx, SystemNamespace, deploymentName, 1, downGen)).To(Succeed())
		})
		Expect(WaitForDeploymentReplicas(ctx, SystemNamespace, deploymentName, 2, gen)).To(Succeed())

		// A replica passing its readiness probe does not mean it already serves
		// scale2026_ tools: IsReady() reports ready when any upstream is ready
		// (or while config is still syncing), and Envoy adds the pod to its LB
		// pool the moment it is ready. A single successful poll could hit the
		// warm replica while the other is still cold. Require several consecutive
		// fresh-client successes (> 2x replicas) so round-robin exercises both
		// replicas warm before the strict loop, which has no per-call retry.
		By("warming up: requiring consecutive fresh clients to see scale2026_ tools across replicas")
		const warmStreak = 6
		Eventually(func(g Gomega) {
			for range warmStreak {
				c, err := NewStatelessClient(ctx, gatewayURL)
				g.Expect(err).NotTo(HaveOccurred())
				result, err := c.ListTools(ctx, nil)
				_ = c.Close()
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(verifyMCPServerRegistrationToolsPresent("scale2026_", result)).To(BeTrue(),
					"every replica must serve scale2026_ tools before the strict loop")
			}
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By(fmt.Sprintf("driving %d stateless tools/call across replicas — every call must succeed", iterations))
		for i := range iterations {
			name := fmt.Sprintf("scale-%d", i)

			c, err := NewStatelessClient(ctx, gatewayURL)
			Expect(err).NotTo(HaveOccurred(), "iteration %d: connect", i)

			result, err := c.CallTool(ctx, &mcp.CallToolParams{
				Name:      "scale2026_hello_world",
				Arguments: map[string]any{"name": name},
			})
			Expect(err).NotTo(HaveOccurred(), "iteration %d: tools/call", i)
			Expect(result.Content).NotTo(BeEmpty(), "iteration %d: empty content", i)
			text, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue(), "iteration %d: expected text content", i)
			Expect(text.Text).To(ContainSubstring(fmt.Sprintf("Hello, %s!", name)), "iteration %d", i)

			Expect(c.Close()).To(Succeed())
		}
	})
})
