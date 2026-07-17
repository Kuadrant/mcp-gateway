//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OAuth Protected Resource via CRD", func() {
	authServer := "https://keycloak.example.com/realms/mcp"
	deploymentName := "mcp-gateway"

	BeforeEach(func() {
		Expect(ClearOAuthProtectedResource(ctx, SystemNamespace, MCPExtensionName)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, SystemNamespace, deploymentName)).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	AfterEach(func() {
		Expect(ClearOAuthProtectedResource(ctx, SystemNamespace, MCPExtensionName)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, SystemNamespace, deploymentName)).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	// Serial: patches the shared MCPGatewayExtension and rolls the shared
	// mcp-gateway deployment; a mid-rollout gateway cannot serve parallel specs.
	It("[Happy] serves oauth-protected-resource metadata from CRD config and reverts to defaults after removal", Serial, func() {
		By("Capturing deployment generation before patching")
		gen, err := GetDeploymentGeneration(ctx, SystemNamespace, deploymentName)
		Expect(err).NotTo(HaveOccurred())

		By("Setting oauthProtectedResource on the MCPGatewayExtension")
		Expect(SetOAuthProtectedResource(ctx, SystemNamespace, MCPExtensionName, []string{authServer})).To(Succeed())

		By("Waiting for the deployment to roll out with OAUTH env vars")
		Expect(WaitForDeploymentReplicas(ctx, SystemNamespace, deploymentName, 1, gen)).To(Succeed())

		By("Verifying well-known endpoint serves configured metadata")
		wellKnownURL := strings.TrimSuffix(gatewayURL, "/mcp") + "/.well-known/oauth-protected-resource"
		Eventually(func(g Gomega) {
			resp, err := getMCPHTTPClient().Get(wellKnownURL)
			g.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = resp.Body.Close() }()
			g.Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body, err := io.ReadAll(resp.Body)
			g.Expect(err).NotTo(HaveOccurred())

			var metadata map[string]interface{}
			g.Expect(json.Unmarshal(body, &metadata)).To(Succeed())
			g.Expect(metadata).To(HaveKey("authorization_servers"))
			servers, ok := metadata["authorization_servers"].([]interface{})
			g.Expect(ok).To(BeTrue())
			g.Expect(servers).To(ContainElement(authServer))
			g.Expect(metadata).To(HaveKey("resource"))
			g.Expect(metadata).To(HaveKey("bearer_methods_supported"))
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Capturing deployment generation before removing config")
		gen, err = GetDeploymentGeneration(ctx, SystemNamespace, deploymentName)
		Expect(err).NotTo(HaveOccurred())

		By("Removing oauthProtectedResource from the MCPGatewayExtension")
		Expect(ClearOAuthProtectedResource(ctx, SystemNamespace, MCPExtensionName)).To(Succeed())

		By("Waiting for the deployment to roll out without OAUTH env vars")
		Expect(WaitForDeploymentReplicas(ctx, SystemNamespace, deploymentName, 1, gen)).To(Succeed())

		By("Verifying authorization_servers reverts to empty after config removal")
		Eventually(func(g Gomega) {
			resp, err := getMCPHTTPClient().Get(wellKnownURL)
			g.Expect(err).NotTo(HaveOccurred())
			defer func() { _ = resp.Body.Close() }()
			g.Expect(resp.StatusCode).To(Equal(http.StatusOK))

			body, err := io.ReadAll(resp.Body)
			g.Expect(err).NotTo(HaveOccurred())

			var metadata map[string]interface{}
			g.Expect(json.Unmarshal(body, &metadata)).To(Succeed())
			servers, ok := metadata["authorization_servers"].([]interface{})
			g.Expect(ok).To(BeTrue())
			g.Expect(servers).To(BeEmpty())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})
})
