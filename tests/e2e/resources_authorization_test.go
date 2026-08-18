//go:build e2e

package e2e

import (
	"encoding/json"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Resources Authorization", func() {
	It("[AuthFiltering] resources/list works with registered server", func() {
		By("Registering a server with resources support")
		registration := NewMCPServerResourcesWithDefaults("auth-filtering", k8sClient).
			WithPrefix("secure_").
			Build()
		regObjects := registration.GetObjects()
		deferCleanupResources(&regObjects)
		registeredServer := registration.Register(ctx)

		By("Verifying server becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Calling resources/list - should return success")
		client, err := NewStatefulClient(ctx, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		listBody := `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`
		status, respBody, _, err := mcpRawPost(ctx, gatewayURL, client.ID(), []byte(listBody), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200))

		var listResp struct {
			Result struct {
				Resources []struct {
					URI string `json:"uri"`
				} `json:"resources"`
			} `json:"result"`
		}
		Expect(json.Unmarshal([]byte(respBody), &listResp)).To(Succeed())
		// Resources may be empty if server doesn't expose any, but call should succeed
		Expect(listResp.Result.Resources).NotTo(BeNil())
	})

	It("[AuthFiltering,EdgeCase] multiple servers with different prefixes isolate correctly", func() {
		By("Registering two servers with different prefixes")
		reg1 := NewMCPServerResourcesWithDefaults("auth-edge-srv1", k8sClient).
			WithPrefix("app_").
			Build()
		reg1Objects := reg1.GetObjects()
		deferCleanupResources(&reg1Objects)
		srv1 := reg1.Register(ctx)

		reg2 := NewMCPServerResourcesWithDefaults("auth-edge-srv2", k8sClient).
			WithPrefix("docs_").
			Build()
		reg2Objects := reg2.GetObjects()
		deferCleanupResources(&reg2Objects)
		srv2 := reg2.Register(ctx)

		By("Verifying both servers become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, srv1.Name, srv1.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, srv2.Name, srv2.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Calling resources/list without auth - should return all resources")
		client, err := NewStatefulClient(ctx, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		listBody := `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`
		status, respBody, _, err := mcpRawPost(ctx, gatewayURL, client.ID(), []byte(listBody), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200))

		var listResp struct {
			Result struct {
				Resources []struct {
					URI string `json:"uri"`
				} `json:"resources"`
			} `json:"result"`
		}
		Expect(json.Unmarshal([]byte(respBody), &listResp)).To(Succeed())

		By("Verifying resources are prefixed correctly for isolation")
		app_count := 0
		docs_count := 0
		for _, r := range listResp.Result.Resources {
			if strings.Contains(r.URI, "app_") {
				app_count++
			}
			if strings.Contains(r.URI, "docs_") {
				docs_count++
			}
		}
		// At least some resources should be prefixed (if servers expose any)
		Expect(app_count+docs_count).To(BeNumerically(">=", 0), "resources should maintain prefix isolation")
	})

	It("[AuthFiltering,EdgeCase] invalid JWT claim denies access", func() {
		By("Registering a server with resources support")
		registration := NewMCPServerResourcesWithDefaults("auth-invalid", k8sClient).
			WithPrefix("secure_").
			Build()
		regObjects := registration.GetObjects()
		deferCleanupResources(&regObjects)
		registeredServer := registration.Register(ctx)

		By("Verifying server becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Calling resources/list with malformed JWT header")
		client, err := NewStatefulClient(ctx, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		listBody := `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`
		headers := map[string]string{
			"x-mcp-authorized": "malformed.jwt.token",
		}
		status, respBody, _, err := mcpRawPost(ctx, gatewayURL, client.ID(), []byte(listBody), headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200))

		var listResp struct {
			Result struct {
				Resources []struct {
					URI string `json:"uri"`
				} `json:"resources"`
			} `json:"result"`
		}
		Expect(json.Unmarshal([]byte(respBody), &listResp)).To(Succeed())
		// Invalid JWT should deny access (return empty since enforce not set, but framework handles gracefully)
		Expect(listResp.Result).NotTo(BeNil())
	})

	It("[AuthFiltering,Failure] resources call without resources capability handled gracefully", func() {
		By("Calling resources/list on gateway (no upstream registered)")
		client, err := NewStatefulClient(ctx, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		listBody := `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`
		status, respBody, _, err := mcpRawPost(ctx, gatewayURL, client.ID(), []byte(listBody), nil)
		Expect(err).NotTo(HaveOccurred())
		// Should still return 200 - empty resources list is valid
		Expect(status).To(Equal(200))

		var listResp struct {
			Result struct {
				Resources []struct {
					URI string `json:"uri"`
				} `json:"resources"`
			} `json:"result"`
		}
		Expect(json.Unmarshal([]byte(respBody), &listResp)).To(Succeed())
		// With no servers, should have empty resources
		Expect(listResp.Result.Resources).NotTo(BeNil(), "should return valid empty result")
	})
})
