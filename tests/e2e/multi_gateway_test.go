//go:build e2e

package e2e

import (
	"context"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("MCP Gateway Multi-Gateway", func() {
	var (
		testResources = []client.Object{}
	)

	AfterEach(func() {
		// cleanup in reverse order
		for _, to := range testResources {
			CleanupResource(ctx, k8sClient, to)
		}
		testResources = []client.Object{}
	})

	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			GinkgoWriter.Println("failure detected")
		}
	})

	It("[Happy] MCPGatewayExtension targeting non-existent Gateway should report invalid status", func() {
		By("Creating an MCPGatewayExtension targeting a non-existent Gateway")
		mcpExt := NewMCPGatewayExtensionBuilder("test-invalid-gateway", SystemNamespace).
			WithTarget("non-existent-gateway", GatewayNamespace).
			Build()
		testResources = append(testResources, mcpExt)
		Expect(k8sClient.Create(ctx, mcpExt)).To(Succeed())

		By("Verifying MCPGatewayExtension status reports invalid configuration")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionNotReadyWithReason(ctx, k8sClient, mcpExt.Name, mcpExt.Namespace, "Invalid")
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying the status message indicates the issue")
		msg, err := GetMCPGatewayExtensionStatusMessage(ctx, k8sClient, mcpExt.Name, mcpExt.Namespace)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Println("MCPGatewayExtension status message:", msg)
		Expect(msg).To(ContainSubstring("invalid"))
	})

	It("[Happy] MCPGatewayExtension cross-namespace reference requires ReferenceGrant", func() {
		// Note: The existing MCPGatewayExtension in mcp-system already owns the gateway.
		// After adding a ReferenceGrant, this MCPGatewayExtension will get a conflict status
		// because only one MCPGatewayExtension can own a gateway (the oldest one wins).
		By("Creating an MCPGatewayExtension in mcp-test namespace targeting Gateway in gateway-system without ReferenceGrant")
		mcpExt := NewMCPGatewayExtensionBuilder("test-cross-ns", TestServerNameSpace).
			WithTarget(GatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, mcpExt)
		Expect(k8sClient.Create(ctx, mcpExt)).To(Succeed())

		By("Verifying MCPGatewayExtension status reports ReferenceGrant required")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionNotReadyWithReason(ctx, k8sClient, mcpExt.Name, mcpExt.Namespace, "ReferenceGrantRequired")
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Creating a ReferenceGrant in gateway-system to allow cross-namespace reference")
		refGrant := NewReferenceGrantBuilder("allow-mcp-test", GatewayNamespace).
			FromNamespace(TestServerNameSpace).
			Build()
		testResources = append(testResources, refGrant)
		Expect(k8sClient.Create(ctx, refGrant)).To(Succeed())

		By("Verifying MCPGatewayExtension gets conflict status (existing mcp-system MCPGatewayExtension owns the gateway)")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionNotReadyWithReason(ctx, k8sClient, mcpExt.Name, mcpExt.Namespace, "Invalid")
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying the status message indicates conflict")
		msg, err := GetMCPGatewayExtensionStatusMessage(ctx, k8sClient, mcpExt.Name, mcpExt.Namespace)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Println("MCPGatewayExtension status message:", msg)
		Expect(msg).To(ContainSubstring("conflict"))

		By("Deleting the ReferenceGrant")
		Expect(k8sClient.Delete(ctx, refGrant)).To(Succeed())

		By("Verifying MCPGatewayExtension returns to ReferenceGrant required status after ReferenceGrant is deleted")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionNotReadyWithReason(ctx, k8sClient, mcpExt.Name, mcpExt.Namespace, "ReferenceGrantRequired")
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())
	})

	It("[multi-gateway] Second MCPGatewayExtension deployment becomes ready and is accessible. Deletion removes the gateway extension and access", func() {
		// This test uses the dedicated e2e-1 gateway to avoid impacting other tests.
		// It verifies that deleting the MCPGatewayExtension removes the broker/router deployment.
		const (
			e2e1ExtName      = "e2e-1-ext"
			e2e1ExtNamespace = "e2e-deletion-test"
		)

		ctx := context.Background()

		By("Setting up MCPGatewayExtension targeting e2e-1 gateway")
		e2e1Setup := NewMCPGatewayExtensionSetup(k8sClient).
			WithName(e2e1ExtName).
			InNamespace(e2e1ExtNamespace).
			TargetingGateway(E2E1GatewayName, GatewayNamespace).
			WithPublicHost(E2E1PublicHost).
			WithHTTPRoute().
			Build()
		e2e1Setup.Clean(ctx).Register(ctx)
		defer e2e1Setup.TearDown(ctx)

		By("Verifying MCPGatewayExtension becomes ready")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, e2e1ExtName, e2e1ExtNamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Creating MCP client for e2e-1 gateway")
		var (
			e2e1Client *NotifyingMCPClient
			clientErr  error
		)
		Eventually(func(g Gomega) {
			e2e1Client, clientErr = NewMCPGatewayClientWithNotifications(ctx, E2E1GatewayURL, func(j mcp.JSONRPCNotification) {})
			Expect(clientErr).Error().NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying gateway is accessible")
		Eventually(func(g Gomega) {
			toolsList, err := e2e1Client.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())

		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Closing the MCP client before deleting MCPGatewayExtension")
		e2e1Client.Close()

		By("Deleting the MCPGatewayExtension")
		ext := e2e1Setup.GetExtension()
		Expect(k8sClient.Delete(ctx, ext)).To(Succeed())

		By("Verifying the broker/router deployment is removed")
		Eventually(func(g Gomega) {
			deployment := &appsv1.Deployment{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: "mcp-gateway", Namespace: e2e1ExtNamespace}, deployment)
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "deployment should be deleted")
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Recreating the MCPGatewayExtension")
		newSetup := NewMCPGatewayExtensionSetup(k8sClient).
			WithName(e2e1ExtName).
			InNamespace(e2e1ExtNamespace).
			TargetingGateway(E2E1GatewayName, GatewayNamespace).
			WithPublicHost(E2E1PublicHost).
			Build()
		newSetup.Register(ctx)

		By("Verifying MCPGatewayExtension becomes ready again")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, e2e1ExtName, e2e1ExtNamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying the broker/router deployment is recreated and ready")
		Eventually(func(g Gomega) {
			deployment := &appsv1.Deployment{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: "mcp-gateway", Namespace: e2e1ExtNamespace}, deployment)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(deployment.Status.ReadyReplicas).To(BeNumerically(">=", 1))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Re-establishing MCP client connection")
		e2e1Client, clientErr = NewMCPGatewayClientWithNotifications(ctx, E2E1GatewayURL, func(j mcp.JSONRPCNotification) {})
		Expect(clientErr).Error().NotTo(HaveOccurred())

		By("Verifying gateway is accessible again")
		Eventually(func(g Gomega) {
			toolsList, err := e2e1Client.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	It("[multi-gateway] clients see isolated tool lists per gateway and can invoke tools on each", func() {
		// This test verifies that when multiple MCPGatewayExtensions are deployed,
		// each gateway sees only the tools from MCPServerRegistrations that target it,
		// and that tool invocation routes to the correct backend.
		const (
			e2e1ExtName      = "multi-gw-ext"
			e2e1ExtNamespace = "e2e-multi-gw-test"
			teamAPrefix      = "team_a_"
			teamBPrefix      = "team_b_"
		)

		ctx := context.Background()

		By("Setting up MCPGatewayExtension for e2e-1 gateway (team B)")
		e2e1Setup := NewMCPGatewayExtensionSetup(k8sClient).
			WithName(e2e1ExtName).
			InNamespace(e2e1ExtNamespace).
			TargetingGateway(E2E1GatewayName, GatewayNamespace).
			WithPublicHost(E2E1PublicHost).
			WithHTTPRoute().
			Build()
		e2e1Setup.Clean(ctx).Register(ctx)
		defer e2e1Setup.TearDown(ctx)

		By("Verifying MCPGatewayExtension becomes ready")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, e2e1ExtName, e2e1ExtNamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Waiting for e2e-1 broker/router deployment to be ready")
		Eventually(func(g Gomega) {
			deployment := &appsv1.Deployment{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: "mcp-gateway", Namespace: e2e1ExtNamespace}, deployment)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(deployment.Status.ReadyReplicas).To(BeNumerically(">=", 1))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating MCPServerRegistration for team A (targeting mcp-gateway)")
		teamAResources := NewTestResources("team-a-server", k8sClient).
			WithToolPrefix(teamAPrefix).
			ForInternalService("mcp-test-server1", 9090).
			WithParentGateway(GatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, teamAResources.GetObjects()...)
		teamAResources.Register(ctx)

		By("Creating MCPServerRegistration for team B (targeting e2e-1 gateway)")
		teamBResources := NewTestResources("team-b-server", k8sClient).
			InNamespace(e2e1ExtNamespace).
			WithToolPrefix(teamBPrefix).
			ForInternalService("mcp-test-server2", 9090).
			WithHostname("team-b.e2e-1.mcp.local").
			WithBackendNamespace(TestServerNameSpace).
			WithParentGateway(E2E1GatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, teamBResources.GetObjects()...)
		teamBResources.Register(ctx)

		By("Waiting for team A server to be registered with main gateway")
		Eventually(func(g Gomega) {
			err := VerifyMCPServerRegistrationReady(ctx, k8sClient, teamAResources.GetMCPServer().Name, TestServerNameSpace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Waiting for team B server to be registered with e2e-1 gateway")
		Eventually(func(g Gomega) {
			err := VerifyMCPServerRegistrationReady(ctx, k8sClient, teamBResources.GetMCPServer().Name, e2e1ExtNamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Connecting to main gateway (team A)")
		var mainGatewayClient *NotifyingMCPClient
		Eventually(func(g Gomega) {
			var clientErr error
			mainGatewayClient, clientErr = NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(j mcp.JSONRPCNotification) {})
			g.Expect(clientErr).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())
		defer mainGatewayClient.Close()

		By("Connecting to e2e-1 gateway (team B)")
		var e2e1Client *NotifyingMCPClient
		Eventually(func(g Gomega) {
			var clientErr error
			e2e1Client, clientErr = NewMCPGatewayClientWithNotifications(ctx, E2E1GatewayURL, func(j mcp.JSONRPCNotification) {})
			g.Expect(clientErr).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())
		defer e2e1Client.Close()

		By("Verifying main gateway sees team_a_ tools and NOT team_b_ tools")
		Eventually(func(g Gomega) {
			toolsList, err := mainGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())

			hasTeamATools := false
			hasTeamBTools := false
			for _, tool := range toolsList.Tools {
				if strings.HasPrefix(tool.Name, teamAPrefix) {
					hasTeamATools = true
				}
				if strings.HasPrefix(tool.Name, teamBPrefix) {
					hasTeamBTools = true
				}
			}
			g.Expect(hasTeamATools).To(BeTrue(), "main gateway should have team_a_ tools")
			g.Expect(hasTeamBTools).To(BeFalse(), "main gateway should NOT have team_b_ tools")
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying e2e-1 gateway sees team_b_ tools and NOT team_a_ tools")
		Eventually(func(g Gomega) {
			toolsList, err := e2e1Client.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())

			hasTeamATools := false
			hasTeamBTools := false
			for _, tool := range toolsList.Tools {
				if strings.HasPrefix(tool.Name, teamAPrefix) {
					hasTeamATools = true
				}
				if strings.HasPrefix(tool.Name, teamBPrefix) {
					hasTeamBTools = true
				}
			}
			g.Expect(hasTeamBTools).To(BeTrue(), "e2e-1 gateway should have team_b_ tools")
			g.Expect(hasTeamATools).To(BeFalse(), "e2e-1 gateway should NOT have team_a_ tools")
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Invoking a tool on main gateway (team_a_greet from server1)")
		Eventually(func(g Gomega) {
			result, err := mainGatewayClient.CallTool(ctx, mcp.CallToolRequest{
				Request: mcp.Request{},
				Params: mcp.CallToolParams{
					Name:      teamAPrefix + "greet",
					Arguments: map[string]any{"name": "TeamA"},
				},
			})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).NotTo(BeNil())
			g.Expect(result.Content).NotTo(BeEmpty())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Invoking a tool on e2e-1 gateway (team_b_hello_world from server2)")
		Eventually(func(g Gomega) {
			result, err := e2e1Client.CallTool(ctx, mcp.CallToolRequest{
				Request: mcp.Request{},
				Params: mcp.CallToolParams{
					Name:      teamBPrefix + "hello_world",
					Arguments: map[string]any{},
				},
			})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).NotTo(BeNil())
			g.Expect(result.Content).NotTo(BeEmpty())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())
	})
})
