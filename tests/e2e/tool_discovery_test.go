//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/broker"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Progressive tool discovery", Serial, Ordered, func() {
	var (
		testResources []client.Object
		prevGen       string
		cl            *NotifyingMCPClient
	)

	BeforeAll(func() {
		var err error
		prevGen, err = GetDeploymentGeneration(SystemNamespace, "mcp-gateway")
		Expect(err).NotTo(HaveOccurred())
		Expect(AddDeploymentCommandFlag(SystemNamespace, "mcp-gateway", "--discovery-tool-threshold=1")).To(Succeed())
		Expect(WaitForDeploymentReplicas(SystemNamespace, "mcp-gateway", 1, prevGen)).To(Succeed())
	})

	AfterAll(func() {
		gen, err := GetDeploymentGeneration(SystemNamespace, "mcp-gateway")
		Expect(err).NotTo(HaveOccurred())
		Expect(RemoveDeploymentCommandFlag(SystemNamespace, "mcp-gateway", "--discovery-tool-threshold=1")).To(Succeed())
		Expect(WaitForDeploymentReplicas(SystemNamespace, "mcp-gateway", 1, gen)).To(Succeed())
	})

	BeforeEach(func() {
		_ = ScaleDeployment(TestServerNameSpace, scaledMCPTestServer, 1)
		Eventually(func(g Gomega) {
			var err error
			cl, err = NewMCPGatewayClientWithNotifications(ctx, gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	AfterEach(func() {
		if cl != nil {
			cl.Close()
			cl = nil
		}
		for i := len(testResources) - 1; i >= 0; i-- {
			CleanupResource(ctx, k8sClient, testResources[i])
		}
		testResources = nil
	})

	It("[Discovery] discover_tools and select_tools scope tools/list", func() {
		By("registering one MCP server")
		reg := NewMCPServerResourcesWithDefaults("discovery-flow", k8sClient).
			WithPrefix("disc_").
			Build()
		testResources = append(testResources, reg.GetObjects()...)
		mcpsr := reg.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReadyWithToolsCount(ctx, k8sClient, mcpsr.Name, mcpsr.Namespace, 7)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		toolTime := fmt.Sprintf("%stime", mcpsr.Spec.Prefix)

		By("listing tools with discovery forced (meta-tools only)")
		var toolsList *mcp.ListToolsResult
		Eventually(func(g Gomega) {
			var err error
			toolsList, err = cl.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			names := toolNames(toolsList.Tools)
			g.Expect(names).To(ContainElements(broker.ToolDiscoverTools, broker.ToolSelectTools))
			g.Expect(names).NotTo(ContainElement(toolTime))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("calling discover_tools")
		discRes, err := cl.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name:      broker.ToolDiscoverTools,
				Arguments: map[string]any{},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(discRes).NotTo(BeNil())
		Expect(discRes.IsError).To(BeFalseBecause("discover_tools should succeed"))
		Expect(len(discRes.Content)).To(BeNumerically(">=", 1))
		raw := textFromToolResult(discRes)
		Expect(raw).NotTo(BeEmpty())
		Expect(strings.Contains(raw, toolTime)).To(BeTrueBecause("catalog should mention federated tool %s", toolTime))

		By("calling select_tools")
		selRes, err := cl.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name: broker.ToolSelectTools,
				Arguments: map[string]any{
					"tools": []any{toolTime},
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(selRes.IsError).To(BeFalseBecause("select_tools should succeed"))

		By("listing scoped tools")
		Eventually(func(g Gomega) {
			list, err := cl.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			names := toolNames(list.Tools)
			g.Expect(names).To(ContainElements(broker.ToolDiscoverTools, broker.ToolSelectTools, toolTime))
			g.Expect(len(names)).To(Equal(3))
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("resetting selection with empty tools")
		_, err = cl.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name:      broker.ToolSelectTools,
				Arguments: map[string]any{"tools": []any{}},
			},
		})
		Expect(err).NotTo(HaveOccurred())

		Eventually(func(g Gomega) {
			list, err := cl.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			names := toolNames(list.Tools)
			g.Expect(names).To(ContainElements(broker.ToolDiscoverTools, broker.ToolSelectTools))
			g.Expect(names).To(ContainElement(toolTime))
			g.Expect(len(names)).To(BeNumerically(">", 3))
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	It("[Discovery] MCPServerRegistration category appears in discover_tools", func() {
		reg := NewMCPServerResourcesWithDefaults("discovery-meta", k8sClient).
			WithPrefix("dcat_").
			Build()
		testResources = append(testResources, reg.GetObjects()...)
		mcpsr := reg.Register(ctx)
		fresh := &mcpv1alpha1.MCPServerRegistration{}
		Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Namespace: mcpsr.Namespace, Name: mcpsr.Name}, fresh)).To(Succeed())
		fresh.Spec.Category = []string{"e2e-discovery-category"}
		fresh.Spec.Hint = "used only by e2e discovery test"
		Expect(k8sClient.Update(ctx, fresh)).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReadyWithToolsCount(ctx, k8sClient, fresh.Name, fresh.Namespace, 7)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		discRes, err := cl.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name: broker.ToolDiscoverTools,
				Arguments: map[string]any{
					"category": "e2e-discovery",
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(discRes.IsError).To(BeFalseBecause("discover_tools should succeed"))
		raw := textFromToolResult(discRes)
		var cat struct {
			Servers []struct {
				Name       string   `json:"name"`
				Categories []string `json:"categories"`
			} `json:"servers"`
		}
		Expect(json.Unmarshal([]byte(raw), &cat)).To(Succeed())
		found := false
		for _, s := range cat.Servers {
			if !strings.Contains(s.Name, fresh.Name) {
				continue
			}
			for _, c := range s.Categories {
				if c == "e2e-discovery-category" {
					found = true
					break
				}
			}
		}
		Expect(found).To(BeTrueBecause("expected server with discovery category in catalog"))
	})
})

func toolNames(tools []mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for i := range tools {
		out = append(out, tools[i].Name)
	}
	return out
}

func textFromToolResult(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}
