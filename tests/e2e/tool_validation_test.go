//go:build e2e

package e2e

import (
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Tool Schema Validation", func() {
	var (
		testResources    []client.Object
		mcpGatewayClient *NotifyingMCPClient
	)

	BeforeEach(func() {
		Eventually(func(g Gomega) {
			var err error
			mcpGatewayClient, err = NewMCPGatewayClientWithNotifications(ctx, gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	AfterEach(func() {
		if mcpGatewayClient != nil {
			mcpGatewayClient.Close()
			mcpGatewayClient = nil
		}
		for _, to := range testResources {
			CleanupResource(ctx, k8sClient, to)
		}
		testResources = []client.Object{}
	})

	It("[Happy] tool schema validation filters invalid tools", func() {
		By("Registering the custom-response-server which has an invalid tool schema")
		// the custom-response-server has a tool "custom response code" with inputSchema
		// property "responseCode" using "type": "int" which is not a valid JSON Schema type
		registration := NewTestResources("tool-validation", k8sClient).
			ForInternalService("mcp-custom-response", 9090).
			WithPrefix("custom_resp_").
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Waiting for MCPServerRegistration to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Verifying the invalid tool is NOT served through the gateway")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(hasToolWithName(toolsList, "custom_resp_custom response code")).To(BeFalse(),
				"tool with invalid schema should be filtered out")
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})
})

func hasToolWithName(toolsList *mcp.ListToolsResult, name string) bool {
	for _, tool := range toolsList.Tools {
		if tool.Name == name {
			return true
		}
	}
	return false
}
