//go:build e2e

package e2e

import (
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var userSpecificMCPTestServer = "mcp-test-user-specific-server"

var _ = Describe("MCP Gateway User-Specific Tool Lists", func() {
	var (
		testResources []client.Object
	)

	BeforeEach(func() {
		_ = ScaleDeployment(ctx, TestServerNameSpace, userSpecificMCPTestServer, 1)
	})

	AfterEach(func() {
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

	It("[Happy,UserSpecificList] user sees their own tools merged with cached tools", func() {
		By("Creating a standard (cached) server registration")
		stdReg := NewMCPServerResourcesWithDefaults("uspec-std", k8sClient).
			WithPrefix("std_").Build()
		testResources = append(testResources, stdReg.GetObjects()...)
		stdServer := stdReg.Register(ctx)

		By("Creating a user-specific server registration")
		uspecReg := NewMCPServerResourcesWithDefaults("uspec-merge", k8sClient).
			WithBackendTarget(userSpecificMCPTestServer, 9090).
			WithPrefix("uspec_").
			WithUserSpecificList().Build()
		testResources = append(testResources, uspecReg.GetObjects()...)
		uspecServer := uspecReg.Register(ctx)

		By("Waiting for standard server to be ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, stdServer.Name, stdServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Waiting for user-specific server to have a status condition")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationHasCondition(ctx, k8sClient, uspecServer.Name, uspecServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating a client with user-a auth")
		userAClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"Authorization": "Bearer user-a-token",
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = userAClient.Close() }()

		By("Verifying user-a sees both standard and user-specific tools")
		Eventually(func(g Gomega) {
			toolsList, err := userAClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(stdServer.Spec.Prefix, toolsList)).To(BeTrueBecause("standard server tools should be present"))
			g.Expect(verifyMCPServerRegistrationToolsPresent(uspecServer.Spec.Prefix, toolsList)).To(BeTrueBecause("user-specific tools should be present"))
			g.Expect(verifyMCPServerRegistrationToolPresent("uspec_list_repos", toolsList)).To(BeTrueBecause("user-a should see list_repos"))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	It("[UserSpecificList] different users get different tool lists", func() {
		By("Creating a user-specific server registration")
		uspecReg := NewMCPServerResourcesWithDefaults("uspec-diff-users", k8sClient).
			WithBackendTarget(userSpecificMCPTestServer, 9090).
			WithPrefix("uspec_").
			WithUserSpecificList().Build()
		testResources = append(testResources, uspecReg.GetObjects()...)
		uspecServer := uspecReg.Register(ctx)

		By("Waiting for user-specific server to have a status condition")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationHasCondition(ctx, k8sClient, uspecServer.Name, uspecServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating clients for user-a and user-b")
		userAClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"Authorization": "Bearer user-a-token",
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = userAClient.Close() }()

		userBClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"Authorization": "Bearer user-b-token",
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = userBClient.Close() }()

		By("Verifying user-a sees list_repos and create_issue but not run_pipeline")
		Eventually(func(g Gomega) {
			toolsList, err := userAClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolPresent("uspec_list_repos", toolsList)).To(BeTrueBecause("user-a should see list_repos"))
			g.Expect(verifyMCPServerRegistrationToolPresent("uspec_create_issue", toolsList)).To(BeTrueBecause("user-a should see create_issue"))
			g.Expect(verifyMCPServerRegistrationToolPresent("uspec_run_pipeline", toolsList)).To(BeFalseBecause("user-a should NOT see run_pipeline"))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying user-b sees run_pipeline but not list_repos or create_issue")
		Eventually(func(g Gomega) {
			toolsList, err := userBClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolPresent("uspec_run_pipeline", toolsList)).To(BeTrueBecause("user-b should see run_pipeline"))
			g.Expect(verifyMCPServerRegistrationToolPresent("uspec_list_repos", toolsList)).To(BeFalseBecause("user-b should NOT see list_repos"))
			g.Expect(verifyMCPServerRegistrationToolPresent("uspec_create_issue", toolsList)).To(BeFalseBecause("user-b should NOT see create_issue"))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying both users see common tools")
		Eventually(func(g Gomega) {
			toolsA, err := userAClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			toolsB, err := userBClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolPresent("uspec_server_info", toolsA)).To(BeTrueBecause("user-a should see server_info"))
			g.Expect(verifyMCPServerRegistrationToolPresent("uspec_server_info", toolsB)).To(BeTrueBecause("user-b should see server_info"))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	It("[UserSpecificList] user-specific tools are prefixed", func() {
		By("Creating a user-specific server registration with prefix")
		uspecReg := NewMCPServerResourcesWithDefaults("uspec-prefix", k8sClient).
			WithBackendTarget(userSpecificMCPTestServer, 9090).
			WithPrefix("myprefix_").
			WithUserSpecificList().Build()
		testResources = append(testResources, uspecReg.GetObjects()...)
		uspecServer := uspecReg.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationHasCondition(ctx, k8sClient, uspecServer.Name, uspecServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating a client with user-a auth")
		userAClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"Authorization": "Bearer user-a-token",
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = userAClient.Close() }()

		By("Verifying all user-specific tools have the prefix")
		Eventually(func(g Gomega) {
			toolsList, err := userAClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolPresent("myprefix_list_repos", toolsList)).To(BeTrueBecause("user-a tools should have myprefix_"))
			g.Expect(verifyMCPServerRegistrationToolPresent("myprefix_server_info", toolsList)).To(BeTrueBecause("common tools should have myprefix_"))
			// unprefixed versions should NOT exist
			g.Expect(verifyMCPServerRegistrationToolPresent("list_repos", toolsList)).To(BeFalseBecause("unprefixed tools should not exist"))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	It("[UserSpecificList] standard servers unaffected by userSpecificList", func() {
		By("Creating a standard server registration")
		stdReg := NewMCPServerResourcesWithDefaults("uspec-unaffected", k8sClient).
			WithPrefix("std_").Build()
		testResources = append(testResources, stdReg.GetObjects()...)
		stdServer := stdReg.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, stdServer.Name, stdServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Listing tools without user-specific server — record baseline")
		var baselineCount int
		mcpClient, err := NewMCPGatewayClient(ctx, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = mcpClient.Close() }()

		Eventually(func(g Gomega) {
			toolsList, err := mcpClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolsPresent(stdServer.Spec.Prefix, toolsList)).To(BeTrue())
			baselineCount = len(toolsList.Tools)
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Adding a user-specific server registration")
		uspecReg := NewMCPServerResourcesWithDefaults("uspec-noeffect", k8sClient).
			WithBackendTarget(userSpecificMCPTestServer, 9090).
			WithPrefix("uspec_").
			WithUserSpecificList().Build()
		testResources = append(testResources, uspecReg.GetObjects()...)
		uspecServer := uspecReg.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationHasCondition(ctx, k8sClient, uspecServer.Name, uspecServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying client without auth still sees same standard tools (no user-specific tools leak)")
		noAuthClient, err := NewMCPGatewayClient(ctx, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = noAuthClient.Close() }()

		Eventually(func(g Gomega) {
			toolsList, err := noAuthClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolsPresent(stdServer.Spec.Prefix, toolsList)).To(BeTrue())
			// without auth, the user-specific server returns only common tools (server_info, headers)
			// so uspec_ prefix tools may appear but user-a/user-b specific tools should not
			g.Expect(verifyMCPServerRegistrationToolPresent("uspec_list_repos", toolsList)).To(BeFalseBecause("user-a tools should not leak to unauthenticated clients"))
			g.Expect(verifyMCPServerRegistrationToolPresent("uspec_run_pipeline", toolsList)).To(BeFalseBecause("user-b tools should not leak to unauthenticated clients"))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		// standard tool count should be unchanged (common uspec_ tools like server_info/headers may be added)
		GinkgoWriter.Printf("baseline standard tool count: %d\n", baselineCount)
	})

	// Serial: scales the shared user-specific server (which backs the suite's
	// permanent registration) to 0. With no other registrations present the
	// broker reports not-ready, so parallel specs would see gateway-wide 500s.
	It("[UserSpecificList] user-specific server down does not break tools/list", Serial, func() {
		By("Creating a standard server registration")
		stdReg := NewMCPServerResourcesWithDefaults("uspec-degrade", k8sClient).
			WithPrefix("std_").Build()
		testResources = append(testResources, stdReg.GetObjects()...)
		stdServer := stdReg.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, stdServer.Name, stdServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Scaling down the user-specific server")
		Expect(ScaleDeployment(ctx, TestServerNameSpace, userSpecificMCPTestServer, 0)).To(Succeed())

		By("Creating a user-specific server registration pointing to the down server")
		uspecReg := NewMCPServerResourcesWithDefaults("uspec-down", k8sClient).
			WithBackendTarget(userSpecificMCPTestServer, 9090).
			WithPrefix("uspec_").
			WithUserSpecificList().Build()
		testResources = append(testResources, uspecReg.GetObjects()...)
		uspecReg.Register(ctx)

		By("Creating a client with auth")
		userAClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"Authorization": "Bearer user-a-token",
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = userAClient.Close() }()

		By("Verifying tools/list returns standard tools without error")
		Eventually(func(g Gomega) {
			toolsList, err := userAClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(stdServer.Spec.Prefix, toolsList)).To(BeTrueBecause("standard tools should still be present"))
			g.Expect(verifyMCPServerRegistrationToolsPresent("uspec_", toolsList)).To(BeFalseBecause("user-specific tools should not be present when server is down"))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Scaling user-specific server back up for other tests")
		Expect(ScaleDeployment(ctx, TestServerNameSpace, userSpecificMCPTestServer, 1)).To(Succeed())
		// wait for the backend before handing over to the next spec; manager
		// backoff otherwise keeps the broker not-ready well past pod start
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, TestServerNameSpace, userSpecificMCPTestServer)).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	It("[UserSpecificList] tool call routing works for user-specific tools", func() {
		By("Creating a user-specific server registration")
		uspecReg := NewMCPServerResourcesWithDefaults("uspec-call", k8sClient).
			WithBackendTarget(userSpecificMCPTestServer, 9090).
			WithPrefix("uspec_").
			WithUserSpecificList().Build()
		testResources = append(testResources, uspecReg.GetObjects()...)
		uspecServer := uspecReg.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationHasCondition(ctx, k8sClient, uspecServer.Name, uspecServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating a client with user-a auth")
		userAClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"Authorization": "Bearer user-a-token",
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = userAClient.Close() }()

		By("Waiting for user-specific tools to appear")
		Eventually(func(g Gomega) {
			toolsList, err := userAClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolPresent("uspec_server_info", toolsList)).To(BeTrue())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Calling uspec_server_info tool")
		res, err := userAClient.CallTool(ctx, &mcp.CallToolParams{Name: "uspec_server_info"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(len(res.Content)).To(BeNumerically(">=", 1))
		content, ok := res.Content[0].(*mcp.TextContent)
		Expect(ok).To(BeTrue())
		Expect(content.Text).To(ContainSubstring("server=user-specific-test-server"))
		Expect(content.Text).To(ContainSubstring("user=user-a-token"))
	})

	It("[Security,UserSpecificList] internal headers not forwarded to upstream", func() {
		By("Creating a user-specific server registration")
		uspecReg := NewMCPServerResourcesWithDefaults("uspec-security", k8sClient).
			WithBackendTarget(userSpecificMCPTestServer, 9090).
			WithPrefix("uspec_").
			WithUserSpecificList().Build()
		testResources = append(testResources, uspecReg.GetObjects()...)
		uspecServer := uspecReg.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationHasCondition(ctx, k8sClient, uspecServer.Name, uspecServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating a client with auth and a virtual server header")
		userClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"Authorization":       "Bearer user-a-token",
			"X-Mcp-Virtualserver": "test/vs",
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = userClient.Close() }()

		By("Waiting for tools to appear")
		Eventually(func(g Gomega) {
			toolsList, err := userClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolPresent("uspec_headers", toolsList)).To(BeTrue())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Calling uspec_headers to inspect forwarded headers")
		res, err := userClient.CallTool(ctx, &mcp.CallToolParams{Name: "uspec_headers"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(len(res.Content)).To(BeNumerically(">=", 1))
		content, ok := res.Content[0].(*mcp.TextContent)
		Expect(ok).To(BeTrue())

		headerText := strings.ToLower(content.Text)
		GinkgoWriter.Println("headers echoed by upstream:", content.Text)

		By("Verifying no internal x-mcp-* headers were forwarded")
		Expect(headerText).NotTo(ContainSubstring("x-mcp-virtualserver"))
		Expect(headerText).NotTo(ContainSubstring("x-mcp-authorized"))

		By("Verifying the user's own Authorization header was forwarded")
		Expect(headerText).To(ContainSubstring("authorization"))
	})

	It("[UserSpecificList] virtual server filter applies to user-specific tools", func() {
		By("Creating a user-specific server registration")
		uspecReg := NewMCPServerResourcesWithDefaults("uspec-vs", k8sClient).
			WithBackendTarget(userSpecificMCPTestServer, 9090).
			WithPrefix("uspec_").
			WithUserSpecificList().Build()
		testResources = append(testResources, uspecReg.GetObjects()...)
		uspecServer := uspecReg.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationHasCondition(ctx, k8sClient, uspecServer.Name, uspecServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating an MCPVirtualServer that only allows uspec_server_info")
		allowedTool := "uspec_server_info"
		virtualServer := BuildTestMCPVirtualServer("uspec-vs-filter", TestServerNameSpace, []string{allowedTool}).Build()
		testResources = append(testResources, virtualServer)
		Expect(k8sClient.Create(ctx, virtualServer)).To(Succeed())

		By("Creating a client with user-a auth and virtual server header")
		virtualServerHeader := fmt.Sprintf("%s/%s", virtualServer.Namespace, virtualServer.Name)
		vsClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"Authorization":       "Bearer user-a-token",
			"X-Mcp-Virtualserver": virtualServerHeader,
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = vsClient.Close() }()

		By("Verifying only the allowed tool is returned")
		Eventually(func(g Gomega) {
			toolsList, err := vsClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(len(toolsList.Tools)).To(Equal(1), "expected exactly 1 tool from virtual server filter")
			g.Expect(toolsList.Tools[0].Name).To(Equal(allowedTool))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})
})
