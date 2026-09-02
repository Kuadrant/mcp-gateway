//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	statelessScalingExtName   = "stateless-scaling-ext"
	statelessScalingNamespace = "mcp-stateless-scaling"
	statelessScalingPrefix    = "scale_"
	statelessScalingReplicas  = 2
)

// statelessScalingServerHost returns a hostname for the protocol-2026 listener.
func statelessScalingServerHost(subdomain string) string {
	return subdomain + ".protocol-2026." + e2eDomain
}

// tests prove that a 2026-only gateway can scale horizontally without Redis.
// requests succeed across replicas because the protocol is stateless.
var _ = Describe("Stateless Scaling", Ordered, func() {
	var (
		testResources []client.Object
		ssExt         *MCPGatewayExtensionSetup
		ssURL         = Protocol2026GatewayURL
	)

	BeforeAll(func() {
		By("Creating stateless-scaling namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   statelessScalingNamespace,
				Labels: map[string]string{"e2e": "test"},
			},
		}
		_ = k8sClient.Delete(ctx, ns)
		Eventually(func(g Gomega) {
			err := k8sClient.Create(ctx, ns)
			g.Expect(client.IgnoreAlreadyExists(err)).NotTo(HaveOccurred())
		}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

		By("Creating MCPGatewayExtension (no sessionStore — no Redis)")
		ssExt = NewMCPGatewayExtensionSetup(k8sClient).
			WithName(statelessScalingExtName).
			InNamespace(statelessScalingNamespace).
			TargetingGateway(GatewayName, GatewayNamespace).
			WithSectionName(Protocol2026ListenerName).
			WithPublicHost(Protocol2026PublicHost).
			Build()
		ssExt.Clean(ctx).Register(ctx)

		By("Waiting for MCPGatewayExtension to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPGatewayExtensionReady(ctx, k8sClient, statelessScalingExtName, statelessScalingNamespace)).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Waiting for deployment to be ready")
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, statelessScalingNamespace, "mcp-gateway")).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Registering a 2026-only stateless server")
		reg := NewTestResources("ss-stateless", k8sClient).
			InNamespace(statelessScalingNamespace).
			WithBackendTarget("mcp-test-stateless-server", 9090).
			WithBackendNamespace(TestServerNameSpace).
			WithHostname(statelessScalingServerHost("scaling")).
			WithPrefix(statelessScalingPrefix).
			WithSectionName(Protocol2026ListenerName).
			WithParentGateway(GatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, reg.GetObjects()...)
		server := reg.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Waiting for tools to appear via a 2026 client")
		c, err := NewStatelessClient(ctx, ssURL)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = c.Close() }()

		Eventually(func(g Gomega) {
			result, listErr := c.ListTools(ctx, nil)
			g.Expect(listErr).NotTo(HaveOccurred())
			g.Expect(result).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(statelessScalingPrefix, result)).To(BeTrue(),
				"tools with prefix %q should exist", statelessScalingPrefix)
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By(fmt.Sprintf("Scaling deployment to %d replicas", statelessScalingReplicas))
		Expect(ScaleDeployment(ctx, statelessScalingNamespace, "mcp-gateway", statelessScalingReplicas)).To(Succeed())

		By("Waiting for all replicas to be ready")
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, statelessScalingNamespace, "mcp-gateway")).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	AfterAll(func() {
		By("Scaling back to 1 replica")
		_ = ScaleDeployment(ctx, statelessScalingNamespace, "mcp-gateway", 1)

		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, statelessScalingNamespace, "mcp-gateway")).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			DumpClusterState(ctx, statelessScalingNamespace, GatewayNamespace)
		}
	})

	Context("2026-only requests across replicas", func() {
		It("[Happy,StatelessScaling] tools/list succeeds consistently across replicas", func() {
			for i := 0; i < 10; i++ {
				c, err := NewStatelessClient(ctx, ssURL)
				Expect(err).NotTo(HaveOccurred(), "request %d: connect failed", i)

				result, err := c.ListTools(ctx, nil)
				Expect(err).NotTo(HaveOccurred(), "request %d: tools/list failed", i)
				Expect(result).NotTo(BeNil())
				Expect(verifyMCPServerRegistrationToolsPresent(statelessScalingPrefix, result)).To(BeTrue(),
					"request %d: tools with prefix %q should exist", i, statelessScalingPrefix)

				_ = c.Close()
			}
		})

		It("[Happy,StatelessScaling] tools/call succeeds consistently across replicas", func() {
			toolName := statelessScalingPrefix + "echo"
			for i := 0; i < 5; i++ {
				c, err := NewStatelessClient(ctx, ssURL)
				Expect(err).NotTo(HaveOccurred(), "request %d: connect failed", i)

				result, err := c.CallTool(ctx, &mcp.CallToolParams{
					Name:      toolName,
					Arguments: map[string]any{"message": fmt.Sprintf("ping-%d", i)},
				})
				Expect(err).NotTo(HaveOccurred(), "request %d: tools/call failed", i)
				Expect(result).NotTo(BeNil())
				Expect(result.IsError).To(BeFalse(), "request %d: tool returned error", i)

				_ = c.Close()
			}
		})

		It("[Happy,StatelessScaling] no session errors in gateway logs", func() {
			cmd := exec.CommandContext(ctx, "kubectl", "logs",
				"deployment/mcp-gateway",
				"-n", statelessScalingNamespace,
				"--all-containers",
				"--since=5m",
			)
			output, err := cmd.CombinedOutput()
			Expect(err).NotTo(HaveOccurred(), "failed to fetch gateway logs")

			logs := string(output)
			for _, errMsg := range []string{
				"session not found",
				"session validation failed",
				"failed to get remote session",
			} {
				Expect(strings.Contains(logs, errMsg)).To(BeFalse(),
					"gateway logs should not contain %q, but found it in:\n%s", errMsg, logs)
			}
		})
	})
})
