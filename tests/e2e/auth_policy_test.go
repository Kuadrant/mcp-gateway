//go:build e2e

// Package e2e contains end-to-end tests that exercise the gateway against a running cluster.
package e2e

import (
	"fmt"
	"net/http"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	goenv "github.com/caitlinelfring/go-env-default"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// auth tests use the main HTTPS gateway URL so HTTPRoutes attach to the mcp-tls
// listener where the AuthPolicy is enforced.
var authGatewayURL = goenv.GetDefault("AUTH_GATEWAY_URL", gatewayURL)

func authInitBody() []byte {
	return []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"e2e-auth","version":"0.0.1"}}}`)
}

var _ = Describe("AuthPolicy Authentication and Authorization", Ordered, func() {
	var authResources []client.Object

	BeforeAll(func() {
		if !IsAuthPolicyConfigured(ctx) {
			Skip("auth not configured - skipping AuthPolicy tests")
		}

		By("Enabling trusted headers on the MCPGatewayExtension")
		ext := &mcpv1.MCPGatewayExtension{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: MCPExtensionName, Namespace: SystemNamespace}, ext)).To(Succeed())
		if ext.Spec.TrustedHeadersKey == nil {
			patch := client.MergeFrom(ext.DeepCopy())
			ext.Spec.TrustedHeadersKey = &mcpv1.TrustedHeadersKey{
				SecretName: "trusted-headers-public-key",
				Generate:   mcpv1.KeyGenerationDisabled,
			}
			Expect(k8sClient.Patch(ctx, ext, patch)).To(Succeed())

			By("Waiting for gateway to roll out with trusted headers")
			Expect(WaitForDeploymentReady(ctx, SystemNamespace, "mcp-gateway")).To(Succeed())
		}

		By("Creating MCPServerRegistrations matching Keycloak client IDs")
		// registration names must match Keycloak client IDs (mcp-test/test-server1 etc.)
		// so the OPA rule and tool-access-check can map resource_access roles correctly
		reg1 := NewTestResources("auth-server1", k8sClient).
			ForInternalService("mcp-test-server1", 9090).
			WithPrefix("test1_").
			WithRegistrationName("test-server1").
			Build()

		reg2 := NewTestResources("auth-server2", k8sClient).
			ForInternalService("mcp-test-server2", 9090).
			WithPrefix("test2_").
			WithRegistrationName("test-server2").
			Build()

		authResources = append(authResources, reg1.GetObjects()...)
		server1 := reg1.Register(ctx)
		authResources = append(authResources, reg2.GetObjects()...)
		server2 := reg2.Register(ctx)

		By("Waiting for MCPServerRegistrations to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(BeNil())
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server2.Name, server2.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Waiting for Authorino to start enforcing auth (polling for 401)")
		Eventually(func(g Gomega) {
			status, _, _, err := mcpRawPost(ctx, authGatewayURL, "", authInitBody(), nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(status).To(Equal(http.StatusUnauthorized))
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	AfterAll(func() {
		for i := len(authResources) - 1; i >= 0; i-- {
			CleanupResource(ctx, k8sClient, authResources[i])
		}
	})

	It("[Auth] should return 401 for unauthenticated requests", func() {
		By("Sending an initialize request without Authorization header")
		status, respBody, respHeaders, err := mcpRawPost(ctx, authGatewayURL, "", authInitBody(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusUnauthorized))
		Expect(respBody).To(ContainSubstring("Authentication required"))
		Expect(respHeaders.Get("WWW-Authenticate")).To(ContainSubstring("Bearer"))
	})

	It("[Auth] should return 401 for malformed JWT", func() {
		By("Sending an initialize request with an invalid bearer token")
		headers := map[string]string{"Authorization": "Bearer not-a-real-jwt"}

		status, _, _, err := mcpRawPost(ctx, authGatewayURL, "", authInitBody(), headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusUnauthorized))
	})

	It("[Auth] should allow initialize and tools/list with valid JWT, filtered by user roles", func() {
		By("Obtaining a token from Keycloak")
		token, err := GetKeycloakUserToken(ctx, "mcp", "mcp")
		Expect(err).NotTo(HaveOccurred())
		headers := map[string]string{"Authorization": "Bearer " + token}

		By("Sending initialize request")
		sessionID, err := mcpInitialize(ctx, authGatewayURL, headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(sessionID).NotTo(BeEmpty())

		By("Sending notifications/initialized")
		Expect(mcpNotifyInitialized(ctx, authGatewayURL, sessionID, headers)).To(Succeed())

		By("Listing tools and checking role-based filtering")
		// mcp user is in accounting group:
		//   test-server1: greet (yes), time (no)
		//   test-server2: headers (yes), hello_world (no)
		var tools []string
		Eventually(func(g Gomega) {
			var listErr error
			_, tools, listErr = mcpListTools(ctx, authGatewayURL, sessionID, headers)
			g.Expect(listErr).NotTo(HaveOccurred())
			g.Expect(tools).To(ContainElement("test1_greet"), "accounting has greet for test-server1")
			g.Expect(tools).To(ContainElement("test2_headers"), "accounting has headers for test-server2")
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		Expect(tools).NotTo(ContainElement("test1_time"), "accounting does NOT have time for test-server1")
		Expect(tools).NotTo(ContainElement("test2_hello_world"), "accounting does NOT have hello_world for test-server2")
	})

	It("[Auth] should allow authorised tool call", func() {
		By("Obtaining a token and initialising a session")
		token, err := GetKeycloakUserToken(ctx, "mcp", "mcp")
		Expect(err).NotTo(HaveOccurred())
		headers := map[string]string{"Authorization": "Bearer " + token}

		sessionID, err := mcpInitialize(ctx, authGatewayURL, headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(ctx, authGatewayURL, sessionID, headers)).To(Succeed())

		By("Calling test1_greet which is in the accounting role")
		status, content, err := mcpCallTool(ctx, authGatewayURL, sessionID, "test1_greet", map[string]any{"name": "e2e"}, headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusOK))
		Expect(content).NotTo(BeEmpty())
	})

	It("[Auth] should reject unauthorised tool call", func() {
		By("Obtaining a token and initialising a session")
		token, err := GetKeycloakUserToken(ctx, "mcp", "mcp")
		Expect(err).NotTo(HaveOccurred())
		headers := map[string]string{"Authorization": "Bearer " + token}

		sessionID, err := mcpInitialize(ctx, authGatewayURL, headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(ctx, authGatewayURL, sessionID, headers)).To(Succeed())

		By("Calling test1_time which is NOT in the accounting role")
		status, _, callErr := mcpCallTool(ctx, authGatewayURL, sessionID, "test1_time", nil, headers)
		Expect(callErr).To(HaveOccurred())
		Expect(status).NotTo(Equal(http.StatusOK), "unauthorised tool call must not succeed")
	})

	It("[Auth] should filter prompts/list by JWT roles", func() {
		By("Obtaining a token and initialising a session")
		token, err := GetKeycloakUserToken(ctx, "mcp", "mcp")
		Expect(err).NotTo(HaveOccurred())
		headers := map[string]string{"Authorization": "Bearer " + token}

		sessionID, err := mcpInitialize(ctx, authGatewayURL, headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(ctx, authGatewayURL, sessionID, headers)).To(Succeed())

		By("Listing prompts — accounting has prompt:greet on test-server1")
		var prompts []string
		Eventually(func(g Gomega) {
			var listErr error
			_, prompts, listErr = mcpListPrompts(ctx, authGatewayURL, sessionID, headers)
			g.Expect(listErr).NotTo(HaveOccurred())
			g.Expect(prompts).NotTo(BeEmpty())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		Expect(prompts).To(ContainElement("test1_greet"), "accounting has prompt:greet for test-server1")
	})

	It("[Auth] should allow prompts/get with auth as first request to a server", func() {
		By("Obtaining a token and initialising a session")
		token, err := GetKeycloakUserToken(ctx, "mcp", "mcp")
		Expect(err).NotTo(HaveOccurred())
		headers := map[string]string{"Authorization": "Bearer " + token}

		sessionID, err := mcpInitialize(ctx, authGatewayURL, headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(ctx, authGatewayURL, sessionID, headers)).To(Succeed())

		By("Calling prompts/get for test1_greet without any prior tool call (triggers hairpin init)")
		status, respBody, err := mcpGetPrompt(ctx, authGatewayURL, sessionID, "test1_greet", map[string]string{"name": "reviewer"}, headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200))
		Expect(respBody).To(ContainSubstring("Say hi to reviewer"))
	})

	It("[Auth] should return empty prompts for combined JWT + VirtualServer with no intersection", func() {
		By("Registering the everything-server so its prompts are federated")
		evReg := NewTestResources("auth-everything", k8sClient).
			ForInternalService("everything-server", 9090).
			WithPrefix("everything_").
			Build()
		evObjects := evReg.GetObjects()
		evServer := evReg.Register(ctx)
		defer func() {
			for i := len(evObjects) - 1; i >= 0; i-- {
				CleanupResource(ctx, k8sClient, evObjects[i])
			}
			CleanupResource(ctx, k8sClient, evServer)
		}()

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, evServer.Name, evServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating a VirtualServer that allows only the everything-server prompt (user has no JWT role for it)")
		virtualServer := NewMCPVirtualServerBuilder("auth-prompt-combined-vs", TestServerNameSpace).
			WithTools([]string{"test1_greet"}).
			WithPrompts([]string{"everything_simple_prompt"}).Build()
		Expect(k8sClient.Create(ctx, virtualServer)).To(Succeed())
		defer func() {
			CleanupResource(ctx, k8sClient, virtualServer)
		}()

		By("Obtaining a token and initialising a session with VirtualServer header")
		token, err := GetKeycloakUserToken(ctx, "mcp", "mcp")
		Expect(err).NotTo(HaveOccurred())
		virtualServerHeader := fmt.Sprintf("%s/%s", virtualServer.Namespace, virtualServer.Name)
		headers := map[string]string{
			"Authorization":       "Bearer " + token,
			"X-Mcp-Virtualserver": virtualServerHeader,
		}

		sessionID, err := mcpInitialize(ctx, authGatewayURL, headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(ctx, authGatewayURL, sessionID, headers)).To(Succeed())

		By("Listing prompts — JWT allows test1_greet, VirtualServer allows everything_simple_prompt, intersection is empty")
		Eventually(func(g Gomega) {
			_, prompts, listErr := mcpListPrompts(ctx, authGatewayURL, sessionID, headers)
			g.Expect(listErr).NotTo(HaveOccurred())
			g.Expect(prompts).To(BeEmpty(), "intersection of JWT and VirtualServer should be empty")
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})
})
