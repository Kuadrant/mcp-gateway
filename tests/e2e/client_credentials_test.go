//go:build e2e

package e2e

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
)

const (
	// the client the tls-server fixture is started with (--oauth-clients)
	oauthCCClientID     = "e2e-client"
	oauthCCClientSecret = "e2e-secret"
	// plain-HTTP listener on the same fixture, 401s without a token it issued
	oauthCCBackendPort = int32(9090)
)

// the broker's upstream credential is used only for broker -> upstream traffic
// (initialize, tools/list, health pings). a client's tools/call is routed
// client -> Envoy -> backend and carries no broker token, so calling
// oauth_cc_whoami from here would hit the 401 guard. proof that a real token
// was minted is the pair of facts that the upstream rejects unauthenticated
// calls and the federated whoami description names the bound client_id.
var _ = Describe("OAuth2 Client Credentials", func() {
	tokenURL := fmt.Sprintf("https://%s.%s.svc.cluster.local:%d/token",
		tlsServerName, TestServerNameSpace, tlsServerPort)

	Describe("valid credentials", Ordered, func() {
		var (
			testResources    []client.Object
			mcpGatewayClient *NotifyingMCPClient
			registeredServer *mcpv1.MCPServerRegistration
		)

		BeforeAll(func() {
			deferCleanupResources(&testResources)
			mcpGatewayClient = newTestGatewayClient()

			creds := BuildClientCredentialsSecret(UniqueName("oauth-cc-good"), oauthCCClientID, oauthCCClientSecret)
			_ = k8sClient.Delete(ctx, creds)
			Expect(k8sClient.Create(ctx, creds)).To(Succeed())
			testResources = append(testResources, creds)

			registration := NewTestResources("oauth-cc", k8sClient).
				ForInternalService(tlsServerName, oauthCCBackendPort).
				WithHostname("oauth-cc.mcp-gateway.local").
				WithPrefix("oauth_cc_").
				WithOAuth2ClientCredentials(creds.Name, tokenURL).
				Build()
			testResources = append(testResources, registration.GetObjects()...)
			registeredServer = registration.Register(ctx)
		})

		It("[Auth] federates tools from an upstream the broker authenticates to with a minted token", func() {
			By("Verifying the MCPServerRegistration becomes ready")
			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient,
					registeredServer.Name, registeredServer.Namespace)).To(Succeed())
			}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

			By("Verifying the whoami description names the client_id the token was issued to")
			Eventually(func(g Gomega) {
				toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(toolsList).NotTo(BeNil())
				g.Expect(toolsList.Tools).To(ContainElement(SatisfyAll(
					HaveField("Name", "oauth_cc_whoami"),
					HaveField("Description", ContainSubstring(oauthCCClientID)),
				)), "the upstream binds the whoami description to the presented token's client")
			}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
		})

		It("[Auth] reports the auth method on /status without leaking the credential", func() {
			serverName := registeredServer.Namespace + "/" + registeredServer.Name

			By("Verifying /status reports oauth2ClientCredentials for this server")
			var rawStatus []byte
			Eventually(func(g Gomega) {
				status, raw, err := GetBrokerStatus(ctx)
				g.Expect(err).NotTo(HaveOccurred())
				rawStatus = raw
				g.Expect(status.Servers).To(ContainElement(SatisfyAll(
					HaveField("Name", serverName),
					HaveField("AuthMethod", "oauth2ClientCredentials"),
				)))
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

			By("Verifying the /status body carries no secret material")
			Expect(string(rawStatus)).NotTo(ContainSubstring(oauthCCClientSecret))
			Expect(string(rawStatus)).NotTo(ContainSubstring("access_token"))

			// only the client secret: it is unique to this suite. a generic
			// token like "access_token" would couple this assertion to whatever
			// else is writing to the shared broker log under --procs
			By("Verifying the broker logs carry no secret material")
			logs, err := GetDeploymentLogs(ctx, SystemNamespace, "mcp-gateway")
			Expect(err).NotTo(HaveOccurred())
			Expect(logs).NotTo(ContainSubstring(oauthCCClientSecret))
		})
	})

	Describe("credential rotation", Ordered, func() {
		var (
			testResources    []client.Object
			mcpGatewayClient *NotifyingMCPClient
			registeredServer *mcpv1.MCPServerRegistration
			creds            *corev1.Secret
		)

		BeforeAll(func() {
			deferCleanupResources(&testResources)
			mcpGatewayClient = newTestGatewayClient()

			creds = BuildClientCredentialsSecret(UniqueName("oauth-cc-bad"), oauthCCClientID, "wrong-secret")
			_ = k8sClient.Delete(ctx, creds)
			Expect(k8sClient.Create(ctx, creds)).To(Succeed())
			testResources = append(testResources, creds)

			registration := NewTestResources("oauth-cc-rotate", k8sClient).
				ForInternalService(tlsServerName, oauthCCBackendPort).
				WithHostname("oauth-cc-rotate.mcp-gateway.local").
				WithPrefix("oauth_cc_rot_").
				WithOAuth2ClientCredentials(creds.Name, tokenURL).
				Build()
			testResources = append(testResources, registration.GetObjects()...)
			registeredServer = registration.Register(ctx)
		})

		It("[Auth] federates no tools when the client secret is wrong", func() {
			// the controller resolves the Secret fine; the token request fails
			// broker-side, so the registration stays Ready as it does for a
			// bad per-server CA
			By("Verifying the MCPServerRegistration becomes ready (token errors are broker-side)")
			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient,
					registeredServer.Name, registeredServer.Namespace)).To(Succeed())
			}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

			// an empty tool list is true the instant the registration lands, so
			// wait for the broker to report the token failure first: that proves
			// it tried and the absence below means something
			By("Verifying the broker reports a token failure for this server")
			Eventually(func(g Gomega) {
				status, _, err := GetBrokerStatus(ctx)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(status.Servers).To(ContainElement(SatisfyAll(
					HaveField("Name", registeredServer.Namespace+"/"+registeredServer.Name),
					HaveField("Ready", false),
					HaveField("Message", ContainSubstring("failed to obtain access token")),
				)))
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

			By("Verifying no tools with the oauth_cc_rot_ prefix appear")
			WaitForToolsWithPrefixAbsent(ctx, mcpGatewayClient, "oauth_cc_rot_")
		})

		It("[Auth] federates tools after the client secret is rotated to the correct value", func() {
			By("Patching the secret with the correct client secret")
			patch := client.MergeFrom(creds.DeepCopy())
			creds.StringData = map[string]string{"clientSecret": oauthCCClientSecret}
			Expect(k8sClient.Patch(ctx, creds, patch)).To(Succeed())

			By("Verifying tools appear without restarting the broker")
			WaitForToolsWithPrefix(ctx, mcpGatewayClient, "oauth_cc_rot_")
		})
	})
})
