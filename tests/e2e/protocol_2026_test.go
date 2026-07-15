//go:build e2e

package e2e

import (
	"slices"
	"strings"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	protocol2026ExtName   = "protocol-2026-ext"
	protocol2026Namespace = "mcp-protocol-2026"
)

var _ = Describe("Protocol 2026-07-28", Ordered, func() {
	var (
		testResources []client.Object
		p2026Ext      *MCPGatewayExtensionSetup
		p2026URL      = Protocol2026GatewayURL
	)

	BeforeAll(func() {
		By("Creating protocol-2026 namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   protocol2026Namespace,
				Labels: map[string]string{"e2e": "test"},
			},
		}
		_ = k8sClient.Delete(ctx, ns)
		Eventually(func(g Gomega) {
			err := k8sClient.Create(ctx, ns)
			g.Expect(client.IgnoreAlreadyExists(err)).NotTo(HaveOccurred())
		}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

		By("Creating MCPGatewayExtension targeting protocol-2026 listener")
		p2026Ext = NewMCPGatewayExtensionSetup(k8sClient).
			WithName(protocol2026ExtName).
			InNamespace(protocol2026Namespace).
			TargetingGateway(GatewayName, GatewayNamespace).
			WithSectionName(Protocol2026ListenerName).
			WithPublicHost(Protocol2026PublicHost).
			WithProtocolMode(mcpv1.ProtocolModeStateless).
			Build()
		p2026Ext.Clean(ctx).Register(ctx)

		By("Waiting for MCPGatewayExtension to become ready")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, protocol2026ExtName, protocol2026Namespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Waiting for broker/router deployment to be ready")
		Eventually(func(g Gomega) {
			err := WaitForDeploymentReady(ctx, protocol2026Namespace, "mcp-gateway")
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	AfterAll(func() {
		if p2026Ext != nil {
			p2026Ext.TearDown(ctx)
		}
	})

	AfterEach(func() {
		for _, obj := range testResources {
			CleanupResource(ctx, k8sClient, obj)
		}
		testResources = []client.Object{}
	})

	// newStatelessClient creates an SDK client that auto-negotiates 2026-07-28
	// with the stateless broker. The SDK handles server/discover, _meta injection,
	// and protocol headers transparently.
	newStatelessClient := func() *mcp.ClientSession {
		var mcpClient *mcp.ClientSession
		Eventually(func(g Gomega) {
			var err error
			mcpClient, err = NewMCPGatewayClient(ctx, p2026URL)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		return mcpClient
	}

	Context("stateless tool calls", func() {
		It("[Happy,Protocol2026] server/discover returns capabilities", func() {
			By("connecting an SDK client (triggers server/discover automatically)")
			mcpClient := newStatelessClient()
			defer func() { _ = mcpClient.Close() }()

			By("verifying the negotiated protocol version is 2026-07-28")
			initResult := mcpClient.InitializeResult()
			Expect(initResult).NotTo(BeNil())
			Expect(initResult.ProtocolVersion).To(Equal("2026-07-28"))
		})

		It("[Happy,Protocol2026] tool call with prefix via stateless gateway", func() {
			By("registering an MCPServerRegistration pointing at the stateless test server")
			reg := NewTestResources("protocol2026-happy", k8sClient).
				InNamespace(protocol2026Namespace).
				WithBackendTarget("mcp-test-stateless-server", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname("server.protocol-2026.127-0-0-1.sslip.io").
				WithPrefix("p26_").
				WithSectionName(Protocol2026ListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			By("waiting for MCPServerRegistration to be ready")
			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			mcpClient := newStatelessClient()
			defer func() { _ = mcpClient.Close() }()

			By("waiting for tools to appear via tools/list")
			var listedTools []string
			Eventually(func(g Gomega) {
				result, err := mcpClient.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				for _, t := range result.Tools {
					listedTools = append(listedTools, t.Name)
				}
				g.Expect(slices.ContainsFunc(listedTools, func(t string) bool {
					return strings.HasPrefix(t, "p26_")
				})).To(BeTrue(), "tools with prefix p26_ should appear")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("verifying discovery tools are not present in stateless mode")
			Expect(slices.Contains(listedTools, "discover_tools")).To(BeFalse(), "discover_tools should not appear in stateless mode")
			Expect(slices.Contains(listedTools, "select_tools")).To(BeFalse(), "select_tools should not appear in stateless mode")

			By("calling the tool")
			result, err := mcpClient.CallTool(ctx, &mcp.CallToolParams{
				Name:      "p26_hello_world",
				Arguments: map[string]any{"name": "e2e"},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).NotTo(BeNil())
			Expect(result.Content).NotTo(BeEmpty())
			textContent, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())
			Expect(textContent.Text).To(ContainSubstring("Hello, e2e!"))
		})

		It("[Happy,Protocol2026] tool call without prefix via stateless gateway", func() {
			By("registering an MCPServerRegistration with no prefix")
			reg := NewTestResources("protocol2026-noprefix", k8sClient).
				InNamespace(protocol2026Namespace).
				WithBackendTarget("mcp-test-stateless-server", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname("noprefix.protocol-2026.127-0-0-1.sslip.io").
				WithSectionName(Protocol2026ListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			By("waiting for MCPServerRegistration to be ready")
			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			mcpClient := newStatelessClient()
			defer func() { _ = mcpClient.Close() }()

			By("waiting for tools to appear via tools/list")
			Eventually(func(g Gomega) {
				result, err := mcpClient.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				names := make([]string, len(result.Tools))
				for i, t := range result.Tools {
					names[i] = t.Name
				}
				g.Expect(slices.Contains(names, "hello_world")).To(BeTrue(), "hello_world tool should appear without prefix")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("calling the tool without prefix")
			result, err := mcpClient.CallTool(ctx, &mcp.CallToolParams{
				Name:      "hello_world",
				Arguments: map[string]any{"name": "no-prefix"},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).NotTo(BeNil())
			Expect(result.Content).NotTo(BeEmpty())
			textContent, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())
			Expect(textContent.Text).To(ContainSubstring("Hello, no-prefix!"))
		})

		It("[Protocol2026] header-body mismatch rejection", func() {
			By("registering an MCPServerRegistration")
			reg := NewTestResources("protocol2026-mismatch", k8sClient).
				InNamespace(protocol2026Namespace).
				WithBackendTarget("mcp-test-stateless-server", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname("server.protocol-2026.127-0-0-1.sslip.io").
				WithPrefix("p26_mismatch_").
				WithSectionName(Protocol2026ListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("waiting for tools to appear")
			mcpClient := newStatelessClient()
			defer func() { _ = mcpClient.Close() }()
			Eventually(func(g Gomega) {
				result, err := mcpClient.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				names := make([]string, len(result.Tools))
				for i, t := range result.Tools {
					names[i] = t.Name
				}
				g.Expect(slices.ContainsFunc(names, func(t string) bool {
					return strings.HasPrefix(t, "p26_mismatch_")
				})).To(BeTrue())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("sending a tool call where mcp-name header matches a real tool but body differs")
			// raw HTTP required: the SDK auto-sets mcp-name from the body so
			// header and body always match. To test the gateway's HeaderMismatch
			// rejection we must craft a request with a deliberate mismatch.
			body, err := mcp2026Payload("tools/call", map[string]any{
				"name": "p26_mismatch_time",
			})
			Expect(err).NotTo(HaveOccurred())

			status, respBody, err := mcp2026RawPost(ctx, p2026URL, body, mcp2026Headers("tools/call", "p26_mismatch_hello_world"))
			GinkgoWriter.Println("response body", respBody)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))
			Expect(respBody).To(ContainSubstring("HeaderMismatch"))
		})

		It("[Protocol2026] unknown tool error", func() {
			By("registering an MCPServerRegistration")
			reg := NewTestResources("protocol2026-notfound", k8sClient).
				InNamespace(protocol2026Namespace).
				WithBackendTarget("mcp-test-stateless-server", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname("server.protocol-2026.127-0-0-1.sslip.io").
				WithPrefix("p26_notfound_").
				WithSectionName(Protocol2026ListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("waiting for tools to appear")
			mcpClient := newStatelessClient()
			defer func() { _ = mcpClient.Close() }()
			Eventually(func(g Gomega) {
				result, err := mcpClient.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				names := make([]string, len(result.Tools))
				for i, t := range result.Tools {
					names[i] = t.Name
				}
				g.Expect(slices.ContainsFunc(names, func(t string) bool {
					return strings.HasPrefix(t, "p26_notfound_")
				})).To(BeTrue())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("calling a non-existent tool via SDK client")
			_, err := mcpClient.CallTool(ctx, &mcp.CallToolParams{
				Name: "p26_notfound_nonexistent_tool",
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unknown tool"))
		})
	})

	Context("stateless prompt routing", func() {
		It("[Happy,Protocol2026] prompt listing and get via stateless gateway", func() {
			By("registering an MCPServerRegistration pointing at the stateless test server")
			reg := NewTestResources("protocol2026-prompts", k8sClient).
				InNamespace(protocol2026Namespace).
				WithBackendTarget("mcp-test-stateless-server", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname("server.protocol-2026.127-0-0-1.sslip.io").
				WithPrefix("p26p_").
				WithSectionName(Protocol2026ListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			By("waiting for MCPServerRegistration to be ready")
			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			mcpClient := newStatelessClient()
			defer func() { _ = mcpClient.Close() }()

			By("waiting for prompts to appear via prompts/list")
			Eventually(func(g Gomega) {
				result, err := mcpClient.ListPrompts(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				names := make([]string, len(result.Prompts))
				for i, p := range result.Prompts {
					names[i] = p.Name
				}
				g.Expect(slices.ContainsFunc(names, func(p string) bool {
					return strings.HasPrefix(p, "p26p_")
				})).To(BeTrue(), "prompts with prefix p26p_ should appear")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("calling prompts/get")
			promptResult, err := mcpClient.GetPrompt(ctx, &mcp.GetPromptParams{
				Name:      "p26p_greeting",
				Arguments: map[string]string{"name": "e2e"},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(promptResult).NotTo(BeNil())
			Expect(promptResult.Messages).NotTo(BeEmpty())
			textContent, ok := promptResult.Messages[0].Content.(*mcp.TextContent)
			Expect(ok).To(BeTrue())
			Expect(textContent.Text).To(ContainSubstring("greet"))
			Expect(textContent.Text).To(ContainSubstring("e2e"))
		})
	})
})
