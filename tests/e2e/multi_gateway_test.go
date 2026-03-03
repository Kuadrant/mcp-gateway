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

	It("[Happy] MCPGatewayExtension with invalid sectionName is rejected", func() {
		// Use TestServerNameSpace (mcp-test) which doesn't have an existing MCPGatewayExtension
		// We need to create a ReferenceGrant first to allow cross-namespace reference
		By("Creating a ReferenceGrant to allow cross-namespace reference")
		refGrant := NewReferenceGrantBuilder("allow-invalid-section-test", GatewayNamespace).
			FromNamespace(TestServerNameSpace).
			Build()
		testResources = append(testResources, refGrant)
		Expect(k8sClient.Create(ctx, refGrant)).To(Succeed())

		By("Creating an MCPGatewayExtension with a non-existent listener name")
		mcpExt := NewMCPGatewayExtensionBuilder("test-invalid-section", TestServerNameSpace).
			WithTarget(GatewayName, GatewayNamespace).
			WithSectionName("nonexistent-listener").
			Build()
		testResources = append(testResources, mcpExt)
		Expect(k8sClient.Create(ctx, mcpExt)).To(Succeed())

		By("Verifying MCPGatewayExtension status reports invalid sectionName")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionNotReadyWithReason(ctx, k8sClient, mcpExt.Name, mcpExt.Namespace, "Invalid")
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying the status message indicates listener not found")
		msg, err := GetMCPGatewayExtensionStatusMessage(ctx, k8sClient, mcpExt.Name, mcpExt.Namespace)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Println("MCPGatewayExtension status message:", msg)
		Expect(msg).To(ContainSubstring("listener"))
	})

	It("[Happy] Second MCPGatewayExtension in same namespace is rejected", func() {
		// The default MCPGatewayExtension in SystemNamespace (mcp-system) is already running
		// Creating a second one in the same namespace should fail

		By("Creating a second MCPGatewayExtension in the same namespace as the existing one")
		mcpExt := NewMCPGatewayExtensionBuilder("test-namespace-conflict", SystemNamespace).
			WithTarget(GatewayName, GatewayNamespace).
			WithSectionName(GatewayListenerName).
			Build()
		testResources = append(testResources, mcpExt)
		Expect(k8sClient.Create(ctx, mcpExt)).To(Succeed())

		By("Verifying MCPGatewayExtension status reports namespace conflict")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionNotReadyWithReason(ctx, k8sClient, mcpExt.Name, mcpExt.Namespace, "Invalid")
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying the status message indicates namespace conflict")
		msg, err := GetMCPGatewayExtensionStatusMessage(ctx, k8sClient, mcpExt.Name, mcpExt.Namespace)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Println("MCPGatewayExtension status message:", msg)
		Expect(msg).To(ContainSubstring("already has MCPGatewayExtension"))
	})

	It("[Happy] MCPGatewayExtension targeting non-existent Gateway should report invalid status", func() {
		By("Creating an MCPGatewayExtension targeting a non-existent Gateway")
		mcpExt := NewMCPGatewayExtensionBuilder("test-invalid-gateway", TestServerNameSpace).
			WithTarget("non-existent-gateway", GatewayNamespace).
			WithSectionName(GatewayListenerName).
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
			WithSectionName(GatewayListenerName).
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
			WithSectionName(E2E1ListenerName).
			WithPublicHost(E2E1PublicHost).
			Build()
		e2e1Setup.Clean(ctx).Register(ctx)
		defer e2e1Setup.TearDown(ctx)

		By("Verifying MCPGatewayExtension becomes ready")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, e2e1ExtName, e2e1ExtNamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying Gateway listener has MCPGatewayExtension condition")
		Eventually(func(g Gomega) {
			err := VerifyGatewayListenerHasMCPCondition(ctx, k8sClient, E2E1GatewayName, GatewayNamespace, E2E1ListenerName)
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

		By("Verifying Gateway listener no longer has MCPGatewayExtension condition")
		Eventually(func(g Gomega) {
			err := VerifyGatewayListenerNoMCPCondition(ctx, k8sClient, E2E1GatewayName, GatewayNamespace, E2E1ListenerName)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Recreating the MCPGatewayExtension")
		newSetup := NewMCPGatewayExtensionSetup(k8sClient).
			WithName(e2e1ExtName).
			InNamespace(e2e1ExtNamespace).
			TargetingGateway(E2E1GatewayName, GatewayNamespace).
			WithSectionName(E2E1ListenerName).
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
		defer e2e1Client.Close()

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
			WithSectionName(E2E1ListenerName).
			WithPublicHost(E2E1PublicHost).
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

	It("[multi-gateway] Shared Gateway with team isolation via sectionName", func() {
		// This test verifies that a single Gateway with multiple listeners can be used
		// by different teams, each with their own MCPGatewayExtension targeting their
		// specific listener. Each team should only see tools from their own registrations.
		const (
			teamAExtName = "team-a-ext"
			teamBExtName = "team-b-ext"
			teamAPrefix  = "team_a_"
			teamBPrefix  = "team_b_"
		)

		ctx := context.Background()

		By("Setting up MCPGatewayExtension for Team A on shared-gateway")
		teamASetup := NewMCPGatewayExtensionSetup(k8sClient).
			WithName(teamAExtName).
			InNamespace(TeamANamespace).
			WithNamespaceLabels(map[string]string{TeamANamespaceLabel: TeamANamespaceValue}).
			TargetingGateway(SharedGatewayName, GatewayNamespace).
			WithSectionName(TeamAMCPListenerName).
			WithPublicHost(TeamAPublicHost).
			Build()
		teamASetup.Clean(ctx).Register(ctx)
		defer teamASetup.TearDown(ctx)

		By("Setting up MCPGatewayExtension for Team B on shared-gateway")
		teamBSetup := NewMCPGatewayExtensionSetup(k8sClient).
			WithName(teamBExtName).
			InNamespace(TeamBNamespace).
			WithNamespaceLabels(map[string]string{TeamANamespaceLabel: TeamBNamespaceValue}).
			TargetingGateway(SharedGatewayName, GatewayNamespace).
			WithSectionName(TeamBMCPListenerName).
			WithPublicHost(TeamBPublicHost).
			Build()
		teamBSetup.Clean(ctx).Register(ctx)
		defer teamBSetup.TearDown(ctx)

		By("Verifying both MCPGatewayExtensions become ready")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, teamAExtName, TeamANamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, teamBExtName, TeamBNamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying Gateway has MCPGatewayExtension conditions on both listeners")
		Eventually(func(g Gomega) {
			err := VerifyGatewayListenerHasMCPCondition(ctx, k8sClient, SharedGatewayName, GatewayNamespace, TeamAMCPListenerName)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		Eventually(func(g Gomega) {
			err := VerifyGatewayListenerHasMCPCondition(ctx, k8sClient, SharedGatewayName, GatewayNamespace, TeamBMCPListenerName)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Waiting for broker/router deployments to be ready")
		Eventually(func(g Gomega) {
			deployment := &appsv1.Deployment{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: "mcp-gateway", Namespace: TeamANamespace}, deployment)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(deployment.Status.ReadyReplicas).To(BeNumerically(">=", 1))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		Eventually(func(g Gomega) {
			deployment := &appsv1.Deployment{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: "mcp-gateway", Namespace: TeamBNamespace}, deployment)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(deployment.Status.ReadyReplicas).To(BeNumerically(">=", 1))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating MCPServerRegistration for Team A (using server1)")
		teamAResources := NewTestResources("team-a-shared", k8sClient).
			InNamespace(TeamANamespace).
			WithToolPrefix(teamAPrefix).
			ForInternalService("mcp-test-server1", 9090).
			WithHostname("team-a-server.team-a.mcp.local").
			WithBackendNamespace(TestServerNameSpace).
			WithParentGateway(SharedGatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, teamAResources.GetObjects()...)
		teamAResources.Register(ctx)

		By("Creating MCPServerRegistration for Team B (using server2)")
		teamBResources := NewTestResources("team-b-shared", k8sClient).
			InNamespace(TeamBNamespace).
			WithToolPrefix(teamBPrefix).
			ForInternalService("mcp-test-server2", 9090).
			WithHostname("team-b-server.team-b.mcp.local").
			WithBackendNamespace(TestServerNameSpace).
			WithParentGateway(SharedGatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, teamBResources.GetObjects()...)
		teamBResources.Register(ctx)

		By("Waiting for Team A server to be registered")
		Eventually(func(g Gomega) {
			err := VerifyMCPServerRegistrationReady(ctx, k8sClient, teamAResources.GetMCPServer().Name, TeamANamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Waiting for Team B server to be registered")
		Eventually(func(g Gomega) {
			err := VerifyMCPServerRegistrationReady(ctx, k8sClient, teamBResources.GetMCPServer().Name, TeamBNamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Connecting to Team A gateway")
		var teamAClient *NotifyingMCPClient
		Eventually(func(g Gomega) {
			var clientErr error
			teamAClient, clientErr = NewMCPGatewayClientWithNotifications(ctx, TeamAGatewayURL, func(j mcp.JSONRPCNotification) {})
			g.Expect(clientErr).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())
		defer teamAClient.Close()

		By("Connecting to Team B gateway")
		var teamBClient *NotifyingMCPClient
		Eventually(func(g Gomega) {
			var clientErr error
			teamBClient, clientErr = NewMCPGatewayClientWithNotifications(ctx, TeamBGatewayURL, func(j mcp.JSONRPCNotification) {})
			g.Expect(clientErr).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())
		defer teamBClient.Close()

		By("Verifying Team A gateway sees only team_a_ tools")
		Eventually(func(g Gomega) {
			toolsList, err := teamAClient.ListTools(ctx, mcp.ListToolsRequest{})
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
			g.Expect(hasTeamATools).To(BeTrue(), "Team A gateway should have team_a_ tools")
			g.Expect(hasTeamBTools).To(BeFalse(), "Team A gateway should NOT have team_b_ tools")
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying Team B gateway sees only team_b_ tools")
		Eventually(func(g Gomega) {
			toolsList, err := teamBClient.ListTools(ctx, mcp.ListToolsRequest{})
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
			g.Expect(hasTeamBTools).To(BeTrue(), "Team B gateway should have team_b_ tools")
			g.Expect(hasTeamATools).To(BeFalse(), "Team B gateway should NOT have team_a_ tools")
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Invoking a tool on Team A gateway (team_a_greet from server1)")
		Eventually(func(g Gomega) {
			result, err := teamAClient.CallTool(ctx, mcp.CallToolRequest{
				Request: mcp.Request{},
				Params: mcp.CallToolParams{
					Name:      teamAPrefix + "greet",
					Arguments: map[string]any{"name": "TeamA"},
				},
			})
			GinkgoWriter.Println("tool call a ", "err", err)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).NotTo(BeNil())
			g.Expect(result.Content).NotTo(BeEmpty())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Invoking a tool on Team B gateway (team_b_hello_world from server2)")
		Eventually(func(g Gomega) {
			result, err := teamBClient.CallTool(ctx, mcp.CallToolRequest{
				Request: mcp.Request{},
				Params: mcp.CallToolParams{
					Name:      teamBPrefix + "hello_world",
					Arguments: map[string]any{},
				},
			})
			GinkgoWriter.Println("tool call b ", "err", err)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).NotTo(BeNil())
			g.Expect(result.Content).NotTo(BeEmpty())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying Team B cannot create an MCPGatewayExtension targeting Team A's listener")
		// This should fail because team-b namespace doesn't have the team-a label
		conflictExt := NewMCPGatewayExtensionBuilder("conflict-ext", TeamBNamespace).
			WithTarget(SharedGatewayName, GatewayNamespace).
			WithSectionName(TeamAMCPListenerName). // targeting Team A's listener
			Build()
		testResources = append(testResources, conflictExt)
		Expect(k8sClient.Create(ctx, conflictExt)).To(Succeed())

		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionNotReadyWithReason(ctx, k8sClient, conflictExt.Name, conflictExt.Namespace, "Invalid")
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying the status message indicates namespace not allowed")
		msg, err := GetMCPGatewayExtensionStatusMessage(ctx, k8sClient, conflictExt.Name, conflictExt.Namespace)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Println("Conflict MCPGatewayExtension status message:", msg)
		// The message should indicate the namespace is not allowed by the listener's allowedRoutes
		Expect(msg).To(Or(ContainSubstring("not allowed"), ContainSubstring("allowedRoutes"), ContainSubstring("conflict")))
	})

	It("[Happy] MCPGatewayExtension creates HTTPRoute for gateway access", func() {
		const (
			extName      = "httproute-test-ext"
			extNamespace = "httproute-test"
			routeName    = "mcp-gateway-route"
		)

		ctx := context.Background()

		By("Setting up MCPGatewayExtension targeting e2e-1 gateway")
		setup := NewMCPGatewayExtensionSetup(k8sClient).
			WithName(extName).
			InNamespace(extNamespace).
			TargetingGateway(E2E1GatewayName, GatewayNamespace).
			WithSectionName(E2E1ListenerName).
			WithPublicHost(E2E1PublicHost).
			Build()
		setup.Clean(ctx).Register(ctx)
		defer setup.TearDown(ctx)

		By("Verifying MCPGatewayExtension becomes ready")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, extName, extNamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		v := NewVerifier(ctx, k8sClient)

		By("Verifying HTTPRoute is created with correct hostname")
		Eventually(func(g Gomega) {
			g.Expect(v.HTTPRouteHasHostname(routeName, extNamespace, E2E1PublicHost)).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying HTTPRoute has correct parentRef with sectionName")
		Expect(v.HTTPRouteHasParentRef(routeName, extNamespace, E2E1GatewayName, E2E1ListenerName)).To(Succeed())

		By("Verifying HTTPRoute has owner reference to MCPGatewayExtension")
		Expect(v.HTTPRouteHasOwnerReference(routeName, extNamespace, extName)).To(Succeed())
	})

	It("[Happy] MCPGatewayExtension with disable-httproute annotation skips HTTPRoute creation", func() {
		const (
			extName      = "no-httproute-ext"
			extNamespace = "no-httproute-test"
			routeName    = "mcp-gateway-route"
		)

		ctx := context.Background()

		By("Setting up MCPGatewayExtension with disable-httproute annotation")
		setup := NewMCPGatewayExtensionSetup(k8sClient).
			WithName(extName).
			InNamespace(extNamespace).
			TargetingGateway(E2E1GatewayName, GatewayNamespace).
			WithSectionName(E2E1ListenerName).
			WithPublicHost(E2E1PublicHost).
			WithDisableHTTPRoute(true).
			Build()
		setup.Clean(ctx).Register(ctx)
		defer setup.TearDown(ctx)

		By("Verifying MCPGatewayExtension becomes ready")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, extName, extNamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying deployment is created (reconciliation proceeded)")
		Eventually(func(g Gomega) {
			deployment := &appsv1.Deployment{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: "mcp-gateway", Namespace: extNamespace}, deployment)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		v := NewVerifier(ctx, k8sClient)

		By("Verifying HTTPRoute does NOT exist")
		Expect(v.HTTPRouteNotFound(routeName, extNamespace)).To(Succeed())
	})

	It("[multi-gateway] Each MCPGatewayExtension gets its own HTTPRoute", func() {
		const (
			teamAExtName = "httproute-team-a"
			teamBExtName = "httproute-team-b"
			routeName    = "mcp-gateway-route"
		)

		ctx := context.Background()

		By("Setting up MCPGatewayExtension for Team A on shared-gateway")
		teamASetup := NewMCPGatewayExtensionSetup(k8sClient).
			WithName(teamAExtName).
			InNamespace(TeamANamespace).
			WithNamespaceLabels(map[string]string{TeamANamespaceLabel: TeamANamespaceValue}).
			TargetingGateway(SharedGatewayName, GatewayNamespace).
			WithSectionName(TeamAMCPListenerName).
			WithPublicHost(TeamAPublicHost).
			Build()
		teamASetup.Clean(ctx).Register(ctx)
		defer teamASetup.TearDown(ctx)

		By("Setting up MCPGatewayExtension for Team B on shared-gateway")
		teamBSetup := NewMCPGatewayExtensionSetup(k8sClient).
			WithName(teamBExtName).
			InNamespace(TeamBNamespace).
			WithNamespaceLabels(map[string]string{TeamANamespaceLabel: TeamBNamespaceValue}).
			TargetingGateway(SharedGatewayName, GatewayNamespace).
			WithSectionName(TeamBMCPListenerName).
			WithPublicHost(TeamBPublicHost).
			Build()
		teamBSetup.Clean(ctx).Register(ctx)
		defer teamBSetup.TearDown(ctx)

		By("Verifying both MCPGatewayExtensions become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPGatewayExtensionReady(ctx, k8sClient, teamAExtName, TeamANamespace)).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPGatewayExtensionReady(ctx, k8sClient, teamBExtName, TeamBNamespace)).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		v := NewVerifier(ctx, k8sClient)

		By("Verifying Team A HTTPRoute has correct hostname and parentRef")
		Eventually(func(g Gomega) {
			g.Expect(v.HTTPRouteHasHostname(routeName, TeamANamespace, TeamAPublicHost)).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())
		Expect(v.HTTPRouteHasParentRef(routeName, TeamANamespace, SharedGatewayName, TeamAMCPListenerName)).To(Succeed())

		By("Verifying Team B HTTPRoute has correct hostname and parentRef")
		Eventually(func(g Gomega) {
			g.Expect(v.HTTPRouteHasHostname(routeName, TeamBNamespace, TeamBPublicHost)).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())
		Expect(v.HTTPRouteHasParentRef(routeName, TeamBNamespace, SharedGatewayName, TeamBMCPListenerName)).To(Succeed())

		By("Verifying each HTTPRoute is owned by its respective MCPGatewayExtension")
		Expect(v.HTTPRouteHasOwnerReference(routeName, TeamANamespace, teamAExtName)).To(Succeed())
		Expect(v.HTTPRouteHasOwnerReference(routeName, TeamBNamespace, teamBExtName)).To(Succeed())
	})
})
