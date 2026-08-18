//go:build e2e

package e2e

import (
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// resourceURIs extracts the URI of each resource, for use with ContainElement assertions.
func resourceURIs(resources []*mcp.Resource) []string {
	uris := make([]string, len(resources))
	for i, r := range resources {
		uris[i] = r.URI
	}
	return uris
}

const (
	resourcesFedExtName   = "resources-federation-ext"
	resourcesFedNamespace = "mcp-resources-fed"
)

var _ = Describe("Resources Federation", Ordered, func() {
	var (
		testResources    []client.Object
		mcpGatewayClient *NotifyingMCPClient
		resourcesFedExt  *MCPGatewayExtensionSetup
		resourcesFedURL  string
	)

	BeforeAll(func() {
		By("Creating resources-federation namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   resourcesFedNamespace,
				Labels: map[string]string{"e2e": "test"},
			},
		}
		_ = k8sClient.Delete(ctx, ns)
		Eventually(func(g Gomega) {
			err := k8sClient.Create(ctx, ns)
			g.Expect(client.IgnoreAlreadyExists(err)).NotTo(HaveOccurred())
		}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

		By("Creating MCPGatewayExtension for resources federation")
		resourcesFedExt = NewMCPGatewayExtensionSetup(k8sClient).
			WithName(resourcesFedExtName).
			InNamespace(resourcesFedNamespace).
			TargetingGateway(GatewayName, GatewayNamespace).
			WithSectionName(ResourcesFederationListenerName).
			WithPublicHost(ResourcesFederationPublicHost).
			Build()
		resourcesFedExt.Clean(ctx).Register(ctx)

		By("Waiting for MCPGatewayExtension to become ready")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, resourcesFedExtName, resourcesFedNamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Waiting for broker/router deployment to be ready")
		Eventually(func(g Gomega) {
			err := WaitForDeploymentReady(ctx, resourcesFedNamespace, "mcp-gateway")
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		resourcesFedURL = ResourcesFederationGatewayURL
	})

	AfterAll(func() {
		if resourcesFedExt != nil {
			resourcesFedExt.TearDown(ctx)
		}
	})

	AfterEach(func() {
		if mcpGatewayClient != nil {
			_ = mcpGatewayClient.Close()
			mcpGatewayClient = nil
		}
		for _, obj := range testResources {
			CleanupResource(ctx, k8sClient, obj)
		}
		testResources = nil
	})

	newGatewayClient := func() {
		Eventually(func(g Gomega) {
			var err error
			mcpGatewayClient, err = NewStatefulClientWithNotifications(ctx, resourcesFedURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	}

	It("[Happy,ResourcesFederation] JWT resources claim filters resources/list per server", func() {
		SetupTrustedHeadersAuthInNamespace(ctx, k8sClient, resourcesFedNamespace, resourcesFedExtName)

		By("Creating MCPServerRegistration for server1")
		reg := NewTestResourcesWithDefaults("jwt-filter-test", k8sClient).
			ForInternalService("mcp-test-server1", 9090).
			WithPrefix("jwttest_").
			WithSectionName(ResourcesFederationListenerName).
			WithHostname("jwtserver.resources-federation.127-0-0-1.sslip.io").
			Build()
		regObjects := reg.GetObjects()
		testResources = append(testResources, regObjects...)

		regServer := reg.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, regServer.Name, regServer.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		serverKey := fmt.Sprintf("%s/%s", regServer.Namespace, regServer.Name)
		wantURI := "ui://" + regServer.Spec.Prefix + "widget.html"
		excludedURI := "ui://" + regServer.Spec.Prefix + "gadget.html"

		By("Creating a JWT that allows widget.html but not gadget.html for this server")
		jwtToken, err := CreateAuthorizedResourcesJWT(map[string][]string{
			serverKey: {"widget.html"},
		})
		Expect(err).NotTo(HaveOccurred())

		By("Creating a client with the x-mcp-authorized header and listing resources")
		var authorizedClient *mcp.ClientSession
		Eventually(func(g Gomega) {
			var connErr error
			authorizedClient, connErr = NewStatefulClientWithHeaders(ctx, resourcesFedURL, map[string]string{"X-Mcp-Authorized": jwtToken})
			g.Expect(connErr).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer func() { _ = authorizedClient.Close() }()

		Eventually(func(g Gomega) {
			listResult, listErr := authorizedClient.ListResources(ctx, nil)
			g.Expect(listErr).NotTo(HaveOccurred())
			g.Expect(listResult).NotTo(BeNil())
			g.Expect(resourceURIs(listResult.Resources)).To(ContainElement(wantURI),
				"widget.html should be visible when the JWT claim allows it for this server")
			g.Expect(resourceURIs(listResult.Resources)).NotTo(ContainElement(excludedURI),
				"gadget.html should be excluded since it's not in the JWT claim for this server")
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	It("[Security,ResourcesFederation] Empty resources claim denies all resources", func() {
		SetupTrustedHeadersAuthInNamespace(ctx, k8sClient, resourcesFedNamespace, resourcesFedExtName)

		By("Creating MCPServerRegistration for server1")
		reg := NewTestResourcesWithDefaults("empty-claim-test", k8sClient).
			ForInternalService("mcp-test-server1", 9090).
			WithPrefix("emptyclaim_").
			WithSectionName(ResourcesFederationListenerName).
			WithHostname("emptyclaimserver.resources-federation.127-0-0-1.sslip.io").
			Build()
		regObjects := reg.GetObjects()
		testResources = append(testResources, regObjects...)

		regServer := reg.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, regServer.Name, regServer.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		deniedURI := "ui://" + regServer.Spec.Prefix + "widget.html"

		By("Creating a JWT with an empty (present but empty) resources claim")
		jwtToken, err := CreateAuthorizedResourcesJWT(map[string][]string{})
		Expect(err).NotTo(HaveOccurred())

		By("Creating a client with the empty-claim JWT and listing resources")
		var deniedClient *mcp.ClientSession
		Eventually(func(g Gomega) {
			var connErr error
			deniedClient, connErr = NewStatefulClientWithHeaders(ctx, resourcesFedURL, map[string]string{"X-Mcp-Authorized": jwtToken})
			g.Expect(connErr).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer func() { _ = deniedClient.Close() }()

		Eventually(func(g Gomega) {
			listResult, listErr := deniedClient.ListResources(ctx, nil)
			g.Expect(listErr).NotTo(HaveOccurred())
			g.Expect(listResult).NotTo(BeNil())
			g.Expect(resourceURIs(listResult.Resources)).NotTo(ContainElement(deniedURI),
				"an empty resources claim should deny widget.html even though the server is registered")
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	It("[Happy,Elicitation] Elicitation rewriting works after sseRewriter rename", func() {
		By("Creating MCPServerRegistration for elicitation testing")
		reg := NewTestResourcesWithDefaults("elicitation-rename-test", k8sClient).
			ForInternalService("everything-server", 9090).
			WithPrefix("elicit_").
			WithSectionName(ResourcesFederationListenerName).
			WithHostname("elicitserver.resources-federation.127-0-0-1.sslip.io").
			Build()
		regObjects := reg.GetObjects()
		testResources = append(testResources, regObjects...)

		regServer := reg.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, regServer.Name, regServer.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		toolName := regServer.Spec.Prefix + "trigger-elicitation-request"
		handler := acceptElicitHandler(map[string]any{"name": "e2e-test-user"})

		By("Creating an elicitation-capable MCP client")
		var elicitClient *mcp.ClientSession
		Eventually(func(g Gomega) {
			var err error
			elicitClient, err = NewStatefulClientWithElicitation(ctx, resourcesFedURL, handler)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer func() { _ = elicitClient.Close() }()

		By("Verifying the trigger-elicitation-request tool is visible")
		Eventually(func(g Gomega) {
			toolsList, err := elicitClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolPresent(toolName, toolsList)).To(BeTrueBecause("%s should exist", toolName))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Calling trigger-elicitation-request tool to exercise the renamed elicitationRewriter end to end")
		Eventually(func(g Gomega) {
			res, err := elicitClient.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: map[string]any{}})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(res).NotTo(BeNil())
			g.Expect(len(res.Content)).To(BeNumerically(">=", 1))

			responseText := ""
			for _, c := range res.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					responseText += tc.Text
				}
			}
			g.Expect(responseText).To(ContainSubstring("User provided the requested information"))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	It("[Happy,ResourcesFederation] tools/call response resourceUri is rewritten to prefixed form", func() {
		By("Creating MCPServerRegistration for server1")
		reg := NewTestResourcesWithDefaults("resourceuri-rewrite-test", k8sClient).
			ForInternalService("mcp-test-server1", 9090).
			WithPrefix("widget_").
			WithSectionName(ResourcesFederationListenerName).
			WithHostname("widgetserver.resources-federation.127-0-0-1.sslip.io").
			Build()
		regObjects := reg.GetObjects()
		testResources = append(testResources, regObjects...)

		regServer := reg.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, regServer.Name, regServer.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Creating MCP client and connecting to gateway")
		newGatewayClient()

		toolName := regServer.Spec.Prefix + "show_widget"

		By("Calling show_widget and verifying the resourceUri comes back prefixed")
		Eventually(func(g Gomega) {
			res, err := mcpGatewayClient.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: map[string]any{}})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(res).NotTo(BeNil())

			ui, ok := res.Meta["ui"].(map[string]any)
			g.Expect(ok).To(BeTrueBecause("result._meta.ui should be present"))
			g.Expect(ui["resourceUri"]).To(Equal("ui://" + regServer.Spec.Prefix + "widget.html"))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	It("[ResourcesFederation] Multiple servers confirm prefix isolation in resourceUri rewrite", func() {
		By("Creating two MCPServerRegistrations for server1 with different prefixes")
		regA := NewTestResourcesWithDefaults("resourceuri-isolation-a", k8sClient).
			ForInternalService("mcp-test-server1", 9090).
			WithPrefix("widgeta_").
			WithSectionName(ResourcesFederationListenerName).
			WithHostname("widgetservera.resources-federation.127-0-0-1.sslip.io").
			Build()
		testResources = append(testResources, regA.GetObjects()...)
		serverA := regA.Register(ctx)

		regB := NewTestResourcesWithDefaults("resourceuri-isolation-b", k8sClient).
			ForInternalService("mcp-test-server1", 9090).
			WithPrefix("widgetb_").
			WithSectionName(ResourcesFederationListenerName).
			WithHostname("widgetserverb.resources-federation.127-0-0-1.sslip.io").
			Build()
		testResources = append(testResources, regB.GetObjects()...)
		serverB := regB.Register(ctx)

		By("Verifying both MCPServerRegistrations become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, serverA.Name, serverA.Namespace)).To(Succeed())
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, serverB.Name, serverB.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Creating MCP client and connecting to gateway")
		newGatewayClient()

		By("Calling show_widget on server A and verifying its own prefix, not server B's")
		Eventually(func(g Gomega) {
			res, err := mcpGatewayClient.CallTool(ctx, &mcp.CallToolParams{Name: serverA.Spec.Prefix + "show_widget", Arguments: map[string]any{}})
			g.Expect(err).NotTo(HaveOccurred())
			ui, ok := res.Meta["ui"].(map[string]any)
			g.Expect(ok).To(BeTrueBecause("result._meta.ui should be present"))
			g.Expect(ui["resourceUri"]).To(Equal("ui://" + serverA.Spec.Prefix + "widget.html"))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Calling show_widget on server B and verifying its own prefix, not server A's")
		Eventually(func(g Gomega) {
			res, err := mcpGatewayClient.CallTool(ctx, &mcp.CallToolParams{Name: serverB.Spec.Prefix + "show_widget", Arguments: map[string]any{}})
			g.Expect(err).NotTo(HaveOccurred())
			ui, ok := res.Meta["ui"].(map[string]any)
			g.Expect(ok).To(BeTrueBecause("result._meta.ui should be present"))
			g.Expect(ui["resourceUri"]).To(Equal("ui://" + serverB.Spec.Prefix + "widget.html"))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	It("[ResourcesFederation] Ordinary tool calls with no resourceUri are unaffected by the rewriter", func() {
		By("Creating MCPServerRegistration for server1")
		reg := NewTestResourcesWithDefaults("resourceuri-passthrough-test", k8sClient).
			ForInternalService("mcp-test-server1", 9090).
			WithPrefix("plain_").
			WithSectionName(ResourcesFederationListenerName).
			WithHostname("plainserver.resources-federation.127-0-0-1.sslip.io").
			Build()
		regObjects := reg.GetObjects()
		testResources = append(testResources, regObjects...)

		regServer := reg.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, regServer.Name, regServer.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Creating MCP client and connecting to gateway")
		newGatewayClient()

		toolName := regServer.Spec.Prefix + "greet"

		By("Calling an ordinary tool with no _meta.ui and verifying the response is unaffected")
		Eventually(func(g Gomega) {
			res, err := mcpGatewayClient.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: map[string]any{"name": "e2e"}})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(res).NotTo(BeNil())
			g.Expect(res.IsError).To(BeFalse())
			g.Expect(len(res.Content)).To(BeNumerically(">=", 1))

			responseText := ""
			for _, c := range res.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					responseText += tc.Text
				}
			}
			g.Expect(responseText).To(Equal("Hi e2e"))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	It("[ResourcesFederation] Non-ui:// resourceUri values are left untouched", func() {
		By("Creating MCPServerRegistration for server1")
		reg := NewTestResourcesWithDefaults("resourceuri-nonui-test", k8sClient).
			ForInternalService("mcp-test-server1", 9090).
			WithPrefix("nonui_").
			WithSectionName(ResourcesFederationListenerName).
			WithHostname("nonuiserver.resources-federation.127-0-0-1.sslip.io").
			Build()
		regObjects := reg.GetObjects()
		testResources = append(testResources, regObjects...)

		regServer := reg.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, regServer.Name, regServer.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Creating MCP client and connecting to gateway")
		newGatewayClient()

		toolName := regServer.Spec.Prefix + "show_external_widget"

		By("Calling show_external_widget and verifying the non-ui:// resourceUri is untouched")
		Eventually(func(g Gomega) {
			res, err := mcpGatewayClient.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: map[string]any{}})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(res).NotTo(BeNil())

			ui, ok := res.Meta["ui"].(map[string]any)
			g.Expect(ok).To(BeTrueBecause("result._meta.ui should be present"))
			g.Expect(ui["resourceUri"]).To(Equal("https://example.com/widget.html"))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})
})
