//go:build e2e

package e2e

import (
	"fmt"
	"net/http"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	goenv "github.com/caitlinelfring/go-env-default"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// auth tests must target the gateway via hostname, not localhost.
// AuthPolicy (Authorino) matches on Host header; localhost bypasses enforcement.
var authGatewayURL = goenv.GetDefault("AUTH_GATEWAY_URL", fmt.Sprintf("%s://%s:8001/mcp", e2eScheme, gatewayPublicHost))

func authInitBody() []byte {
	return []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"e2e-auth","version":"0.0.1"}}}`)
}

var _ = Describe("AuthPolicy Authentication and Authorization", Ordered, func() {
	var authResources []client.Object

	BeforeAll(func() {
		if !IsAuthPolicyConfigured() {
			Skip("auth not configured - skipping AuthPolicy tests")
		}

		By("Enabling trusted headers on the MCPGatewayExtension")
		ext := &mcpv1alpha1.MCPGatewayExtension{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: MCPExtensionName, Namespace: SystemNamespace}, ext)).To(Succeed())
		if ext.Spec.TrustedHeadersKey == nil {
			patch := client.MergeFrom(ext.DeepCopy())
			ext.Spec.TrustedHeadersKey = &mcpv1alpha1.TrustedHeadersKey{
				SecretName: "trusted-headers-public-key",
				Generate:   mcpv1alpha1.KeyGenerationDisabled,
			}
			Expect(k8sClient.Patch(ctx, ext, patch)).To(Succeed())

			By("Waiting for gateway to roll out with trusted headers")
			Expect(WaitForDeploymentReady(SystemNamespace, "mcp-gateway", 1)).To(Succeed())
		}

		By("Creating MCPServerRegistrations matching Keycloak client IDs")
		// registration names must match Keycloak client IDs (mcp-test/test-server1 etc.)
		// so the OPA rule and tool-access-check can map resource_access roles correctly
		reg1 := NewTestResources("auth-server1", k8sClient).
			ForInternalService("mcp-test-server1", 9090).
			WithToolPrefix("test1_").
			WithRegistrationName("test-server1").
			Build()

		reg2 := NewTestResources("auth-server2", k8sClient).
			ForInternalService("mcp-test-server2", 9090).
			WithToolPrefix("test2_").
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
			status, _, _, err := mcpRawPost(authGatewayURL, "", authInitBody(), nil)
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
		status, respBody, respHeaders, err := mcpRawPost(authGatewayURL, "", authInitBody(), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusUnauthorized))
		Expect(respBody).To(ContainSubstring("Authentication required"))
		Expect(respHeaders.Get("WWW-Authenticate")).To(ContainSubstring("Bearer"))
	})

	It("[Auth] should return 401 for malformed JWT", func() {
		By("Sending an initialize request with an invalid bearer token")
		headers := map[string]string{"Authorization": "Bearer not-a-real-jwt"}

		status, _, _, err := mcpRawPost(authGatewayURL, "", authInitBody(), headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusUnauthorized))
	})

	It("[Auth] should allow initialize and tools/list with valid JWT, filtered by user roles", func() {
		By("Obtaining a token from Keycloak")
		token, err := GetKeycloakUserToken("mcp", "mcp")
		Expect(err).NotTo(HaveOccurred())
		headers := map[string]string{"Authorization": "Bearer " + token}

		By("Sending initialize request")
		sessionID, err := mcpInitialize(authGatewayURL, headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(sessionID).NotTo(BeEmpty())

		By("Sending notifications/initialized")
		Expect(mcpNotifyInitialized(authGatewayURL, sessionID, headers)).To(Succeed())

		By("Listing tools and checking role-based filtering")
		// mcp user is in accounting group:
		//   test-server1: greet (yes), time (no)
		//   test-server2: headers (yes), hello_world (no)
		var tools []string
		Eventually(func(g Gomega) {
			var listErr error
			_, tools, listErr = mcpListTools(authGatewayURL, sessionID, headers)
			g.Expect(listErr).NotTo(HaveOccurred())
			g.Expect(tools).NotTo(BeEmpty())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		Expect(tools).To(ContainElement("test1_greet"), "accounting has greet for test-server1")
		Expect(tools).NotTo(ContainElement("test1_time"), "accounting does NOT have time for test-server1")
		Expect(tools).To(ContainElement("test2_headers"), "accounting has headers for test-server2")
		Expect(tools).NotTo(ContainElement("test2_hello_world"), "accounting does NOT have hello_world for test-server2")
	})

	It("[Auth] should allow authorised tool call", func() {
		By("Obtaining a token and initialising a session")
		token, err := GetKeycloakUserToken("mcp", "mcp")
		Expect(err).NotTo(HaveOccurred())
		headers := map[string]string{"Authorization": "Bearer " + token}

		sessionID, err := mcpInitialize(authGatewayURL, headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(authGatewayURL, sessionID, headers)).To(Succeed())

		By("Calling test1_greet which is in the accounting role")
		status, content, err := mcpCallTool(authGatewayURL, sessionID, "test1_greet", map[string]any{"name": "e2e"}, headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusOK))
		Expect(content).NotTo(BeEmpty())
	})

	It("[Auth] should reject unauthorised tool call", func() {
		By("Obtaining a token and initialising a session")
		token, err := GetKeycloakUserToken("mcp", "mcp")
		Expect(err).NotTo(HaveOccurred())
		headers := map[string]string{"Authorization": "Bearer " + token}

		sessionID, err := mcpInitialize(authGatewayURL, headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(authGatewayURL, sessionID, headers)).To(Succeed())

		By("Calling test1_time which is NOT in the accounting role")
		status, _, callErr := mcpCallTool(authGatewayURL, sessionID, "test1_time", nil, headers)
		Expect(callErr).To(HaveOccurred())
		Expect(status).NotTo(Equal(http.StatusOK), "unauthorised tool call must not succeed")
	})
})
