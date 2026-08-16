//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Resources/Read Routing", func() {
	It("[Happy] resources/read routing through Envoy with prefix rewriting", Serial, func() {
		By("Registering MCP server with resources and unique prefix")
		registration := NewMCPServerResources("resources-read", "resources-read.mcp-gateway.local", "mcp-test-server1", 9090, k8sClient).
			WithPrefix("docs_").
			Build()
		regObjects := registration.GetObjects()
		deferCleanupResources(&regObjects)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating MCP client for resources/read requests")
		client, err := NewStatefulClient(ctx, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		By("Sending resources/read for prefixed resource URI")
		// Gateway stores resources with prefix: "docs_example.com/file1.txt"
		// Client sends: "example.com/file1.txt" (original authority)
		// Router adds prefix before looking up in broker, then strips it from response
		resourceURI := "file://example.com/file1.txt"
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"%s"}}`, resourceURI)
		status, respBody, _, err := mcpRawPost(ctx, gatewayURL, client.ID(), []byte(body), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200))

		By("Verifying response contains resource content")
		var resp struct {
			Result struct {
				Contents []struct {
					URI  string `json:"uri"`
					Text string `json:"text"`
				} `json:"contents"`
			} `json:"result"`
		}
		Expect(json.Unmarshal([]byte(respBody), &resp)).To(Succeed())
		Expect(resp.Result.Contents).NotTo(BeEmpty())
		Expect(resp.Result.Contents[0].URI).To(Equal(resourceURI))

		By("Sending multiple resources/read requests sequentially")
		for i := 1; i <= 3; i++ {
			uri := fmt.Sprintf("file://example.com/file%d.txt", i)
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"resources/read","params":{"uri":"%s"}}`, i, uri)
			status, respBody, _, err := mcpRawPost(ctx, gatewayURL, client.ID(), []byte(body), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))

			var resp struct {
				Result struct {
					Contents []struct {
						Text string `json:"text"`
					} `json:"contents"`
				} `json:"result"`
			}
			Expect(json.Unmarshal([]byte(respBody), &resp)).To(Succeed())
			Expect(resp.Result.Contents).NotTo(BeEmpty(), "request %d should return content", i)
		}

		By("Verifying unrecognized resource URI returns error")
		unknownURI := "file://unknown.com/nonexistent.txt"
		body = fmt.Sprintf(`{"jsonrpc":"2.0","id":10,"method":"resources/read","params":{"uri":"%s"}}`, unknownURI)
		status, respBody, _, err = mcpRawPost(ctx, gatewayURL, client.ID(), []byte(body), nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200))

		var errResp struct {
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		Expect(json.Unmarshal([]byte(respBody), &errResp)).To(Succeed())
		Expect(errResp.Error.Code).To(Equal(-32603)) // Internal error for unrecognized prefix
	})

	It("[Happy] resource authorization filtering via JWT header", Serial, func() {
		By("Registering MCP server with resources and unique prefix")
		registration := NewMCPServerResources("resources-auth", "resources-auth.mcp-gateway.local", "mcp-test-server1", 9090, k8sClient).
			WithPrefix("app_").
			Build()
		regObjects := registration.GetObjects()
		deferCleanupResources(&regObjects)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating client without authorization header")
		client, err := NewStatefulClient(ctx, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client.Close() }()

		By("Sending resources/list without JWT should return all resources")
		body := `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`
		status, respBody, _, err := mcpRawPost(ctx, gatewayURL, client.ID(), []byte(body), nil)
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
		resourceCount := len(listResp.Result.Resources)
		Expect(resourceCount).To(BeNumerically(">", 0), "should return resources without JWT")

		By("Creating JWT claim allowing only example.com resources")
		jwtToken, err := CreateAuthorizedResourcesJWT(map[string][]string{
			registeredServer.Name: {"example.com"},
		})
		Expect(err).NotTo(HaveOccurred())

		By("Sending resources/list with JWT filter header")
		status, respBody, _, err = mcpRawPost(ctx, gatewayURL, client.ID(), []byte(body), map[string]string{
			"x-mcp-authorized": jwtToken,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200))

		Expect(json.Unmarshal([]byte(respBody), &listResp)).To(Succeed())
		Expect(len(listResp.Result.Resources)).To(BeNumerically("<=", resourceCount), "JWT filter should reduce resources")

		By("Verifying all remaining resources are from example.com authority")
		for _, resource := range listResp.Result.Resources {
			Expect(resource.URI).To(ContainSubstring("example.com"), "only example.com resources should remain")
		}
	})
})
