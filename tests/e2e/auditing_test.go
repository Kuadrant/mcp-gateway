//go:build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("MCP Gateway Auditing E2E", func() {
	var (
		testResources   = []client.Object{}
		auditGatewayExt *MCPGatewayExtensionSetup
		auditExtName    = "mcp-gateway-audit"
	)

	BeforeEach(func() {
		// Clean up default extension to avoid port conflicts on Gateway
		if defaultMCPGatewayExt != nil {
			defaultMCPGatewayExt.TearDown(ctx)
		}

		// Create a custom extension with auditing enabled
		auditGatewayExt = NewMCPGatewayExtensionSetup(k8sClient).
			WithName(auditExtName).
			InNamespace(SystemNamespace).
			TargetingGateway(GatewayName, GatewayNamespace).
			WithSectionName(GatewayListenerName).
			WithPublicHost(gatewayPublicHost).
			Build()

		// Enable auditing on spec
		auditGatewayExt.extension.Spec.Audit = &mcpv1alpha1.AuditConfig{
			ParameterLogging: mcpv1alpha1.ParameterLoggingEnabled,
			IdentityHeaders:  []string{"x-forwarded-email", "x-auth-user"},
		}

		auditGatewayExt.Clean(ctx).Register(ctx)

		By("Waiting for custom audit gateway extension to become ready")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, auditExtName, SystemNamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	AfterEach(func() {
		// Tear down custom audit extension
		if auditGatewayExt != nil {
			auditGatewayExt.TearDown(ctx)
		}

		// Re-register default extension for subsequent tests
		if defaultMCPGatewayExt != nil {
			defaultMCPGatewayExt.Clean(ctx).Register(ctx)
			Eventually(func(g Gomega) {
				err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, MCPExtensionName, SystemNamespace)
				g.Expect(err).NotTo(HaveOccurred())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		}

		// cleanup test resources
		for _, to := range testResources {
			CleanupResource(ctx, k8sClient, to)
		}
		testResources = []client.Object{}
	})

	It("[Happy, Auditing] Full correlation context, Multiple tool calls, Parameter logging, Fallback identity, Baggage sanitization, Parameter truncation", func() {
		By("Creating HTTPRoute and MCPServer for test-server1")
		registration := NewMCPServerResourcesWithDefaults("audit-test", k8sClient).
			WithPrefix("auditsrv").
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReadyWithToolsCount(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace, 7)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Invoking a tool call with correlation context and baggage")
		headers := map[string]string{
			"traceparent":       "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			"baggage":           "user.id=test-user,agent.id=test-agent",
			"x-forwarded-email": "test@example.com",
		}

		client, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, headers)
		Expect(err).NotTo(HaveOccurred())
		defer client.Close()

		toolName := fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "hello_world")

		_, err = client.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name: toolName,
				Arguments: map[string]any{
					"name": "AuditTester",
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())

		By("Verifying Envoy access logs expose audit metadata through dynamic metadata")
		Eventually(func(g Gomega) {
			cmd := exec.CommandContext(ctx, "kubectl", "logs", "-n", GatewayNamespace, "deployment/mcp-gateway", "-c", "istio-proxy", "--tail=50")
			logs, err := cmd.CombinedOutput()
			g.Expect(err).NotTo(HaveOccurred())

			logStr := string(logs)
			g.Expect(logStr).To(ContainSubstring(`"mcp_method":"tools/call"`))
			g.Expect(logStr).To(ContainSubstring(`"mcp_tool_name":"hello_world"`))
			g.Expect(logStr).To(ContainSubstring(`"mcp_server_name":"auditsrv"`))
			g.Expect(logStr).To(ContainSubstring(`"mcp_user_id":"test-user"`))
			g.Expect(logStr).To(ContainSubstring(`"mcp_agent_id":"test-agent"`))
			g.Expect(logStr).To(ContainSubstring(`"mcp_tool_params":"{\"name\":\"AuditTester\"}"`))
		}, 30*time.Second, 2*time.Second).Should(Succeed())
	})
})
