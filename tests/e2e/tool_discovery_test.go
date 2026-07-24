//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"sync"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func isBrokerMetaTool(name string) bool {
	return name == "discover_tools" || name == "select_tools" ||
		name == "list_tags" || name == "filter_tools_by_tags"
}

const (
	toolDiscExtName   = "tool-discovery-ext"
	toolDiscNamespace = "mcp-tool-discovery"
)

var _ = Describe("Tool Discovery", Ordered, func() {
	var (
		testResources    []client.Object
		mcpGatewayClient *NotifyingMCPClient
		toolDiscExt      *MCPGatewayExtensionSetup
		toolDiscURL      = ToolDiscoveryGatewayURL
	)

	BeforeAll(func() {
		By("Creating tool-discovery namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   toolDiscNamespace,
				Labels: map[string]string{"e2e": "test"},
			},
		}
		_ = k8sClient.Delete(ctx, ns)
		Eventually(func(g Gomega) {
			err := k8sClient.Create(ctx, ns)
			g.Expect(client.IgnoreAlreadyExists(err)).NotTo(HaveOccurred())
		}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

		By("Creating MCPGatewayExtension targeting tool-discovery listener")
		toolDiscExt = NewMCPGatewayExtensionSetup(k8sClient).
			WithName(toolDiscExtName).
			InNamespace(toolDiscNamespace).
			TargetingGateway(GatewayName, GatewayNamespace).
			WithSectionName(ToolDiscoveryListenerName).
			WithPublicHost(ToolDiscoveryPublicHost).
			Build()
		toolDiscExt.Clean(ctx).Register(ctx)

		By("Waiting for MCPGatewayExtension to become ready")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, toolDiscExtName, toolDiscNamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Waiting for broker/router deployment to be ready")
		Eventually(func(g Gomega) {
			err := WaitForDeploymentReady(ctx, toolDiscNamespace, "mcp-gateway")
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	AfterAll(func() {
		if toolDiscExt != nil {
			toolDiscExt.TearDown(ctx)
		}
	})

	newGatewayClient := func() {
		Eventually(func(g Gomega) {
			var err error
			mcpGatewayClient, err = NewMCPGatewayClientWithNotifications(ctx, toolDiscURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	}

	AfterEach(func() {
		if mcpGatewayClient != nil {
			_ = mcpGatewayClient.Close()
			mcpGatewayClient = nil
		}
		for _, obj := range testResources {
			CleanupResource(ctx, k8sClient, obj)
		}
		testResources = []client.Object{}
	})

	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			GinkgoWriter.Println("failure detected in tool discovery test")
		}
	})

	Context("discover_tools", func() {
		BeforeEach(func() {
			newGatewayClient()
		})

		It("returns correct metadata for registered servers with category and hint", func() {
			By("registering a server with category and hint")
			reg := NewTestResources("discover-metadata", k8sClient).
				InNamespace(toolDiscNamespace).
				WithBackendTarget("mcp-test-server2", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(ToolDiscoveryServerHost).
				WithPrefix("disc_meta_").
				WithCategory("messaging").
				WithHint("provides messaging tools").
				WithSectionName(ToolDiscoveryListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			By("waiting for the server to become ready")
			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("waiting for tools to appear via gateway")
			WaitForToolsWithPrefix(ctx, mcpGatewayClient, "disc_meta_")

			By("calling discover_tools and verifying metadata")
			sessionID := mcpGatewayClient.ID()
			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, toolDiscURL, sessionID, nil, nil)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(resp).NotTo(BeNil())

				var found *discoverServerInfo
				for i := range resp.Servers {
					for _, t := range resp.Servers[i].Tools {
						if strings.HasPrefix(t, "disc_meta_") {
							found = &resp.Servers[i]
							break
						}
					}
					if found != nil {
						break
					}
				}
				g.Expect(found).NotTo(BeNil(), "server with disc_meta_ tools should be in discover_tools response")
				g.Expect(found.Categories).To(ContainElement("messaging"))
				g.Expect(found.Hint).To(Equal("provides messaging tools"))
				g.Expect(found.Tools).NotTo(BeEmpty())
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())
		})

		It("filters servers by category: case-insensitive match, multi-category match by either value, no match for unknown category", func() {
			By("registering a multi-category server and a messaging server")
			regMulti := NewTestResources("multi-cat", k8sClient).
				InNamespace(toolDiscNamespace).
				WithBackendTarget("mcp-test-server2", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(ToolDiscoveryServerHost).
				WithPrefix("multicat_").
				WithCategory("Dining", "reservations").
				WithSectionName(ToolDiscoveryListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, regMulti.GetObjects()...)
			sMulti := regMulti.Register(ctx)

			regMsg := NewTestResources("cat-filter-msg", k8sClient).
				InNamespace(toolDiscNamespace).
				WithBackendTarget("mcp-test-server1", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(ToolDiscoveryServerHost).
				WithPrefix("catmsg_").
				WithCategory("messaging").
				WithSectionName(ToolDiscoveryListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, regMsg.GetObjects()...)
			sMsg := regMsg.Register(ctx)

			By("waiting for both servers to be ready")
			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, sMulti.Name, sMulti.Namespace)).To(Succeed())
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, sMsg.Name, sMsg.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			WaitForToolsWithPrefix(ctx, mcpGatewayClient, "multicat_")
			WaitForToolsWithPrefix(ctx, mcpGatewayClient, "catmsg_")

			sessionID := mcpGatewayClient.ID()
			discoverByCategory := func(g Gomega, category string) (hasMulti, hasMsg bool) {
				_, resp, err := mcpCallDiscoverTools(ctx, toolDiscURL, sessionID, map[string]any{"category": category}, nil)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(resp).NotTo(BeNil())
				for _, s := range resp.Servers {
					for _, t := range s.Tools {
						if strings.HasPrefix(t, "multicat_") {
							hasMulti = true
						}
						if strings.HasPrefix(t, "catmsg_") {
							hasMsg = true
						}
					}
				}
				return hasMulti, hasMsg
			}

			By("filtering by 'dining' (lowercase) should match the Dining category and exclude the messaging server")
			Eventually(func(g Gomega) {
				hasMulti, hasMsg := discoverByCategory(g, "dining")
				g.Expect(hasMulti).To(BeTrue(), "multi-category server should match 'dining' case-insensitively")
				g.Expect(hasMsg).To(BeFalse(), "messaging server tools should not be returned")
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

			By("filtering by 'reservations' should also match the multi-category server")
			Eventually(func(g Gomega) {
				hasMulti, _ := discoverByCategory(g, "reservations")
				g.Expect(hasMulti).To(BeTrue(), "server should match when filtering by 'reservations'")
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

			By("filtering by a non-existent category should match neither server")
			Eventually(func(g Gomega) {
				hasMulti, hasMsg := discoverByCategory(g, "nonexistent_xyz")
				g.Expect(hasMulti).To(BeFalse(), "no tools should match nonexistent category")
				g.Expect(hasMsg).To(BeFalse(), "no tools should match nonexistent category")
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())
		})

		It("[Full] respects auth filtering in discover_tools", func() {
			SetupTrustedHeadersAuthInNamespace(ctx, k8sClient, toolDiscNamespace, toolDiscExtName)

			By("registering a server")
			reg := NewTestResources("disc-auth", k8sClient).
				InNamespace(toolDiscNamespace).
				WithBackendTarget("mcp-test-server2", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(ToolDiscoveryServerHost).
				WithPrefix("discauth_").
				WithSectionName(ToolDiscoveryListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("waiting for tools to appear via gateway")
			WaitForToolsWithPrefix(ctx, mcpGatewayClient, "discauth_")

			By("creating a JWT that allows only one tool")
			allowedTools := map[string][]string{
				fmt.Sprintf("%s/%s", server.Namespace, server.Name): {"hello_world"},
			}
			jwtToken, err := CreateAuthorizedCapabilitiesJWT(allowedTools)
			Expect(err).NotTo(HaveOccurred())

			By("calling discover_tools with auth header")
			var sessionID string
			Eventually(func(g Gomega) {
				var initErr error
				sessionID, initErr = mcpInitialize(ctx, toolDiscURL, map[string]string{"X-Mcp-Authorized": jwtToken})
				g.Expect(initErr).NotTo(HaveOccurred())
				g.Expect(mcpNotifyInitialized(ctx, toolDiscURL, sessionID, map[string]string{"X-Mcp-Authorized": jwtToken})).To(Succeed())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, toolDiscURL, sessionID, nil, map[string]string{"X-Mcp-Authorized": jwtToken})
				g.Expect(err).NotTo(HaveOccurred())

				var serverTools []string
				for _, s := range resp.Servers {
					for _, t := range s.Tools {
						if strings.HasPrefix(t, "discauth_") {
							serverTools = append(serverTools, t)
						}
					}
				}
				g.Expect(serverTools).To(HaveLen(1), "only the authorised tool should appear")
				g.Expect(serverTools[0]).To(Equal("discauth_hello_world"))
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())
		})

		It("respects MCPVirtualServer scoping in discover_tools", func() {
			By("registering a server")
			reg := NewTestResources("disc-vs", k8sClient).
				InNamespace(toolDiscNamespace).
				WithBackendTarget("mcp-test-server2", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(ToolDiscoveryServerHost).
				WithPrefix("discvs_").
				WithParentGateway(GatewayName, GatewayNamespace).
				WithSectionName(ToolDiscoveryListenerName).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			WaitForToolsWithPrefix(ctx, mcpGatewayClient, "discvs_")

			By("creating a virtual server allowing only one tool")
			allowedTool := "discvs_hello_world"
			vs := BuildTestMCPVirtualServer("disc-vs-test", TestServerNameSpace, []string{allowedTool}).Build()
			testResources = append(testResources, vs)
			Expect(k8sClient.Create(ctx, vs)).To(Succeed())

			vsHeader := fmt.Sprintf("%s/%s", vs.Namespace, vs.Name)
			sessionID, err := mcpInitialize(ctx, toolDiscURL, map[string]string{"X-Mcp-Virtualserver": vsHeader})
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, toolDiscURL, sessionID, map[string]string{"X-Mcp-Virtualserver": vsHeader})).To(Succeed())

			By("discover_tools should only return the virtual server's allowed tools")
			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, toolDiscURL, sessionID, nil, map[string]string{"X-Mcp-Virtualserver": vsHeader})
				g.Expect(err).NotTo(HaveOccurred())

				var serverTools []string
				for _, s := range resp.Servers {
					for _, t := range s.Tools {
						if strings.HasPrefix(t, "discvs_") {
							serverTools = append(serverTools, t)
						}
					}
				}
				g.Expect(serverTools).To(HaveLen(1))
				g.Expect(serverTools[0]).To(Equal(allowedTool))
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		})

	})

	Context("select_tools", func() {
		It("scopes tools/list to the selection, replaces the scope on re-select, and resets on empty list", func() {
			By("registering a server")
			reg := NewTestResources("select-scope", k8sClient).
				InNamespace(toolDiscNamespace).
				WithBackendTarget("mcp-test-server2", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(ToolDiscoveryServerHost).
				WithPrefix("selscope_").
				WithSectionName(ToolDiscoveryListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("establishing a raw session")
			sessionID, err := mcpInitialize(ctx, toolDiscURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, toolDiscURL, sessionID, nil)).To(Succeed())

			By("waiting for tools to appear and recording the full tool count")
			var fullToolCount int
			Eventually(func(g Gomega) {
				_, tools, err := mcpListTools(ctx, toolDiscURL, sessionID, nil)
				g.Expect(err).NotTo(HaveOccurred())
				hasPrefix := false
				for _, t := range tools {
					if strings.HasPrefix(t, "selscope_") {
						hasPrefix = true
						break
					}
				}
				g.Expect(hasPrefix).To(BeTrue())
				fullToolCount = len(tools)
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			tool1 := "selscope_hello_world"
			tool2 := "selscope_time"

			By("calling select_tools with one tool")
			status, result, err := mcpCallSelectTools(ctx, toolDiscURL, sessionID, []string{tool1}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))
			Expect(result.Status).To(ContainSubstring("scope set"))

			By("verifying subsequent tools/list returns only the selected tool (plus meta-tools)")
			Eventually(func(g Gomega) {
				_, tools, err := mcpListTools(ctx, toolDiscURL, sessionID, nil)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(tools).To(ContainElement(tool1), "selected tool should be in the list")
				for _, t := range tools {
					if isBrokerMetaTool(t) {
						continue
					}
					g.Expect(t).To(Equal(tool1), "only selected tool and meta-tools should be returned")
				}
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

			By("re-scoping to a different tool")
			status, result, err = mcpCallSelectTools(ctx, toolDiscURL, sessionID, []string{tool2}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))
			Expect(result.Status).To(ContainSubstring("scope set"))

			By("verifying only the re-scoped tool (and meta-tools) is now in tools/list")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, toolDiscURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				g.Expect(tools).To(ContainElement(tool2), "re-scoped tool should be in the list")
				for _, t := range tools {
					if isBrokerMetaTool(t) {
						continue
					}
					g.Expect(t).To(Equal(tool2), "only re-scoped tool should be in the list")
				}
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

			By("resetting scope with empty list")
			status, result, err = mcpCallSelectTools(ctx, toolDiscURL, sessionID, []string{}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))
			Expect(result.Status).To(ContainSubstring("reset"))

			By("verifying full tool set is restored")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, toolDiscURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				g.Expect(len(tools)).To(Equal(fullToolCount), "full tool set should be restored after reset")
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())
		})

		It("returns error for invalid tool name (all-or-nothing)", func() {
			By("establishing a raw session")
			sessionID, err := mcpInitialize(ctx, toolDiscURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, toolDiscURL, sessionID, nil)).To(Succeed())

			By("calling select_tools with an invalid tool name")
			_, content, err := mcpCallTool(ctx, toolDiscURL, sessionID, "select_tools",
				map[string]any{"tools": []any{"nonexistent_tool_xyz_12345"}}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(content).NotTo(BeEmpty())
			Expect(content[0].Text).To(ContainSubstring("not available"))
		})

		It("fails entirely when partial valid list contains one invalid tool", func() {
			By("registering a server")
			reg := NewTestResources("select-partial", k8sClient).
				InNamespace(toolDiscNamespace).
				WithBackendTarget("mcp-test-server2", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(ToolDiscoveryServerHost).
				WithPrefix("selpart_").
				WithSectionName(ToolDiscoveryListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			sessionID, err := mcpInitialize(ctx, toolDiscURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, toolDiscURL, sessionID, nil)).To(Succeed())

			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, toolDiscURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				hasPrefix := false
				for _, t := range tools {
					if strings.HasPrefix(t, "selpart_") {
						hasPrefix = true
						break
					}
				}
				g.Expect(hasPrefix).To(BeTrue())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("calling select_tools with one valid and one invalid tool")
			_, content, err := mcpCallTool(ctx, toolDiscURL, sessionID, "select_tools",
				map[string]any{"tools": []any{"selpart_hello_world", "nonexistent_tool_abc"}}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(content).NotTo(BeEmpty())
			Expect(content[0].Text).To(ContainSubstring("not available"),
				"entire select should fail when any tool is invalid")
		})

	})

	Context("notifications", func() {
		It("delivers notifications/tools/list_changed over SSE after select_tools", func() {
			By("registering a server")
			reg := NewTestResources("notif-select", k8sClient).
				InNamespace(toolDiscNamespace).
				WithBackendTarget("mcp-test-server2", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(ToolDiscoveryServerHost).
				WithPrefix("notifsel_").
				WithSectionName(ToolDiscoveryListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			notifCh := make(chan struct{}, 1)
			client, err := NewMCPGatewayClientWithNotifications(ctx, toolDiscURL, func(method string) {
				if method == "notifications/tools/list_changed" {
					select {
					case notifCh <- struct{}{}:
					default:
					}
				}
			})
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			WaitForToolsWithPrefix(ctx, client, "notifsel_")

			sessionID := client.ID()
			status, _, selectErr := mcpCallSelectTools(ctx, toolDiscURL, sessionID, []string{"notifsel_hello_world"}, nil)
			Expect(selectErr).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))

			By("verifying notification was received")
			Eventually(notifCh, TestTimeoutShort, TestRetryInterval).Should(Receive(),
				"should have received notifications/tools/list_changed after select_tools")
		})
	})

	Context("flags", func() {
		It("[Full] hides meta-tools when discovery-tools-enabled=false", func() {
			deploymentName := "mcp-gateway"

			By("adding --discovery-tools-enabled=false flag to deployment")
			Expect(AddDeploymentCommandFlag(ctx, toolDiscNamespace, deploymentName, "--discovery-tools-enabled=false")).To(Succeed())

			DeferCleanup(func() {
				By("removing --discovery-tools-enabled=false flag (best-effort cleanup)")
				if err := RemoveDeploymentCommandFlag(ctx, toolDiscNamespace, deploymentName, "--discovery-tools-enabled=false"); err != nil {
					GinkgoLogr.Info("cleanup: failed to remove flag", "error", err)
					return
				}
				if err := WaitForDeploymentReady(ctx, toolDiscNamespace, deploymentName); err != nil {
					GinkgoLogr.Info("cleanup: deployment not ready after flag removal", "error", err)
				}
			})

			By("waiting for rollout to complete")
			Expect(WaitForDeploymentReady(ctx, toolDiscNamespace, deploymentName)).To(Succeed())

			By("connecting a fresh client after restart")
			var sessionID string
			Eventually(func(g Gomega) {
				var initErr error
				sessionID, initErr = mcpInitialize(ctx, toolDiscURL, nil)
				g.Expect(initErr).NotTo(HaveOccurred())
				g.Expect(mcpNotifyInitialized(ctx, toolDiscURL, sessionID, nil)).To(Succeed())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

			By("verifying discover_tools and select_tools are not in tools/list")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, toolDiscURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				g.Expect(tools).NotTo(ContainElement("discover_tools"),
					"discover_tools should not be listed when discovery is disabled")
				g.Expect(tools).NotTo(ContainElement("select_tools"),
					"select_tools should not be listed when discovery is disabled")
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

			By("calling discover_tools should return an error")
			_, respBody, _, err := mcpRawPost(ctx, toolDiscURL, sessionID,
				[]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"discover_tools"}}`), nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(respBody).To(Or(ContainSubstring(`"error"`), ContainSubstring(`"isError":true`)), "discover_tools should return an error when disabled")
		})

		It("[Full] threshold=0 means never hide (all tools visible alongside meta-tools)", func() {
			By("registering a server")
			reg := NewTestResources("thresh-zero", k8sClient).
				InNamespace(toolDiscNamespace).
				WithBackendTarget("mcp-test-server2", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(ToolDiscoveryServerHost).
				WithPrefix("thzero_").
				WithSectionName(ToolDiscoveryListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			sessionID, err := mcpInitialize(ctx, toolDiscURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, toolDiscURL, sessionID, nil)).To(Succeed())

			By("verifying both real tools and meta-tools are visible")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, toolDiscURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				g.Expect(tools).To(ContainElement("discover_tools"))
				g.Expect(tools).To(ContainElement("select_tools"))
				hasRealTool := false
				for _, t := range tools {
					if strings.HasPrefix(t, "thzero_") {
						hasRealTool = true
						break
					}
				}
				g.Expect(hasRealTool).To(BeTrue(), "real tools should be visible when threshold is 0")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
		})
	})

	Context("threshold", func() {
		It("[Full] above threshold: only meta-tools shown; below threshold: all tools visible", func() {
			deploymentName := "mcp-gateway"

			By("registering a server so we have tools to count")
			reg := NewTestResources("thresh-test", k8sClient).
				InNamespace(toolDiscNamespace).
				WithBackendTarget("mcp-test-server2", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(ToolDiscoveryServerHost).
				WithPrefix("thtest_").
				WithSectionName(ToolDiscoveryListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("setting --discovery-tool-threshold=1")
			Expect(AddDeploymentCommandFlag(ctx, toolDiscNamespace, deploymentName, "--discovery-tool-threshold=1")).To(Succeed())

			DeferCleanup(func() {
				By("removing threshold flag")
				Expect(RemoveDeploymentCommandFlag(ctx, toolDiscNamespace, deploymentName, "--discovery-tool-threshold=1")).To(Succeed())
				Expect(WaitForDeploymentReady(ctx, toolDiscNamespace, deploymentName)).To(Succeed())
			})

			Expect(WaitForDeploymentReady(ctx, toolDiscNamespace, deploymentName)).To(Succeed())

			var sessionID string
			Eventually(func(g Gomega) {
				var initErr error
				sessionID, initErr = mcpInitialize(ctx, toolDiscURL, nil)
				g.Expect(initErr).NotTo(HaveOccurred())
				g.Expect(mcpNotifyInitialized(ctx, toolDiscURL, sessionID, nil)).To(Succeed())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

			By("verifying only meta-tools are shown (above threshold)")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, toolDiscURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				g.Expect(tools).To(ContainElement("discover_tools"))
				g.Expect(tools).To(ContainElement("select_tools"))
				for _, t := range tools {
					if !isBrokerMetaTool(t) {
						g.Expect(t).To(BeEmpty(), "no real tools should be visible above threshold, found: "+t)
					}
				}
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

			By("using select_tools to scope down, tools should become visible")
			status, _, selectErr := mcpCallSelectTools(ctx, toolDiscURL, sessionID, []string{"thtest_hello_world"}, nil)
			Expect(selectErr).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))

			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, toolDiscURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				hasSelected := false
				for _, t := range tools {
					if t == "thtest_hello_world" {
						hasSelected = true
					}
				}
				g.Expect(hasSelected).To(BeTrue(), "selected tool should be visible after scoping")
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())
		})
	})

	Context("isolation and concurrency", func() {
		It("session scope does not leak across sessions", func() {
			By("registering a server")
			reg := NewTestResources("iso-test", k8sClient).
				InNamespace(toolDiscNamespace).
				WithBackendTarget("mcp-test-server2", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(ToolDiscoveryServerHost).
				WithPrefix("iso_").
				WithSectionName(ToolDiscoveryListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("creating two sessions")
			session1, err := mcpInitialize(ctx, toolDiscURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, toolDiscURL, session1, nil)).To(Succeed())

			session2, err := mcpInitialize(ctx, toolDiscURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, toolDiscURL, session2, nil)).To(Succeed())

			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, toolDiscURL, session1, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				hasPrefix := false
				for _, t := range tools {
					if strings.HasPrefix(t, "iso_") {
						hasPrefix = true
						break
					}
				}
				g.Expect(hasPrefix).To(BeTrue())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("scoping session1 to one tool")
			status, _, err := mcpCallSelectTools(ctx, toolDiscURL, session1, []string{"iso_hello_world"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))

			By("verifying session2 still has all tools")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, toolDiscURL, session2, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				isoTools := 0
				for _, t := range tools {
					if strings.HasPrefix(t, "iso_") {
						isoTools++
					}
				}
				g.Expect(isoTools).To(BeNumerically(">", 1),
					"session2 should still see all tools, not be affected by session1's scope")
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())
		})

		It("concurrent select_tools calls on same session do not corrupt state", func() {
			By("registering a server")
			reg := NewTestResources("conc-test", k8sClient).
				InNamespace(toolDiscNamespace).
				WithBackendTarget("mcp-test-server2", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(ToolDiscoveryServerHost).
				WithPrefix("conc_").
				WithSectionName(ToolDiscoveryListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			sessionID, err := mcpInitialize(ctx, toolDiscURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, toolDiscURL, sessionID, nil)).To(Succeed())

			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, toolDiscURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				hasPrefix := false
				for _, t := range tools {
					if strings.HasPrefix(t, "conc_") {
						hasPrefix = true
						break
					}
				}
				g.Expect(hasPrefix).To(BeTrue())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("firing concurrent select_tools calls")
			tool1 := "conc_hello_world"
			tool2 := "conc_time"
			var wg sync.WaitGroup
			wg.Add(2)

			go func() {
				defer wg.Done()
				defer GinkgoRecover()
				_, _, err := mcpCallSelectTools(ctx, toolDiscURL, sessionID, []string{tool1}, nil)
				Expect(err).NotTo(HaveOccurred())
			}()

			go func() {
				defer wg.Done()
				defer GinkgoRecover()
				_, _, err := mcpCallSelectTools(ctx, toolDiscURL, sessionID, []string{tool2}, nil)
				Expect(err).NotTo(HaveOccurred())
			}()

			wg.Wait()

			By("verifying tools/list returns a consistent state (one of the two scopes)")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, toolDiscURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())

				var nonMetaTools []string
				for _, t := range tools {
					if !isBrokerMetaTool(t) {
						nonMetaTools = append(nonMetaTools, t)
					}
				}
				g.Expect(nonMetaTools).To(HaveLen(1),
					"concurrent select_tools should result in a consistent single-tool scope")
				g.Expect(nonMetaTools[0]).To(Or(Equal(tool1), Equal(tool2)),
					"the winning tool should be one of the two selected tools")
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())
		})
	})

	Context("config changes", func() {
		BeforeEach(func() {
			newGatewayClient()
		})

		It("[Full] controller re-reconciles when category and hint are updated", func() {
			By("registering a server with initial category and hint")
			reg := NewTestResources("reconfig-test", k8sClient).
				InNamespace(toolDiscNamespace).
				WithBackendTarget("mcp-test-server2", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(ToolDiscoveryServerHost).
				WithPrefix("reconf_").
				WithCategory("initial-category").
				WithHint("initial hint").
				WithSectionName(ToolDiscoveryListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			WaitForToolsWithPrefix(ctx, mcpGatewayClient, "reconf_")

			sessionID := mcpGatewayClient.ID()

			By("verifying initial category and hint via discover_tools")
			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, toolDiscURL, sessionID, map[string]any{"category": "initial-category"}, nil)
				g.Expect(err).NotTo(HaveOccurred())
				hasTools := false
				for _, s := range resp.Servers {
					for _, t := range s.Tools {
						if strings.HasPrefix(t, "reconf_") {
							hasTools = true
							g.Expect(s.Hint).To(Equal("initial hint"))
						}
					}
				}
				g.Expect(hasTools).To(BeTrue(), "server should be discoverable with initial category")
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

			By("updating category and hint on the live MCPServerRegistration")
			Eventually(func(g Gomega) {
				fresh := &mcpv1.MCPServerRegistration{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{
					Name:      server.Name,
					Namespace: server.Namespace,
				}, fresh)).To(Succeed())
				fresh.Spec.Category = []string{"updated-category"}
				fresh.Spec.Hint = "updated hint"
				g.Expect(k8sClient.Update(ctx, fresh)).To(Succeed())
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

			By("waiting for reconciliation to propagate the updated metadata")
			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, toolDiscURL, sessionID, map[string]any{"category": "updated-category"}, nil)
				g.Expect(err).NotTo(HaveOccurred())
				hasTools := false
				for _, s := range resp.Servers {
					for _, t := range s.Tools {
						if strings.HasPrefix(t, "reconf_") {
							hasTools = true
							g.Expect(s.Hint).To(Equal("updated hint"))
						}
					}
				}
				g.Expect(hasTools).To(BeTrue(), "server should be discoverable with updated category after reconciliation")
			}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

			By("verifying old category no longer matches")
			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, toolDiscURL, sessionID, map[string]any{"category": "initial-category"}, nil)
				g.Expect(err).NotTo(HaveOccurred())
				for _, s := range resp.Servers {
					for _, t := range s.Tools {
						g.Expect(t).NotTo(HavePrefix("reconf_"),
							"old category should no longer match after update")
					}
				}
			}, TestTimeoutShort, TestRetryInterval).Should(Succeed())
		})
	})
})
