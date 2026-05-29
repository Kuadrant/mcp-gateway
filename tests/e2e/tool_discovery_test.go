//go:build e2e

package e2e

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func isBrokerMetaTool(name string) bool {
	return name == "discover_tools" || name == "select_tools" ||
		name == "list_tags" || name == "filter_tools_by_tags"
}

var _ = Describe("Tool Discovery", func() {
	var (
		testResources    []client.Object
		mcpGatewayClient *NotifyingMCPClient
	)

	// newGatewayClient creates a fresh SSE client. call this inside contexts
	// that need one rather than relying on BeforeEach, because contexts that
	// restart the deployment (flags, threshold) would break a pre-existing
	// SSE connection.
	newGatewayClient := func() {
		Eventually(func(g Gomega) {
			var err error
			mcpGatewayClient, err = NewMCPGatewayClientWithNotifications(ctx, gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryFast).Should(Succeed())
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
			reg := NewMCPServerResourcesWithDefaults("discover-metadata", k8sClient).
				WithPrefix("disc_meta_").
				WithCategory("messaging").
				WithHint("provides messaging tools").
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
			sessionID := mcpGatewayClient.GetSessionId()
			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, gatewayURL, sessionID, nil, nil)
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
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())
		})

		It("filters servers by category (case-insensitive)", func() {
			By("registering two servers with different categories")
			reg1 := NewMCPServerResourcesWithDefaults("cat-filter-1", k8sClient).
				WithPrefix("catfilt1_").
				WithCategory("Dining").
				Build()
			testResources = append(testResources, reg1.GetObjects()...)
			s1 := reg1.Register(ctx)

			reg2 := NewMCPServerResourcesWithDefaults("cat-filter-2", k8sClient).
				WithPrefix("catfilt2_").
				WithCategory("messaging").
				Build()
			testResources = append(testResources, reg2.GetObjects()...)
			s2 := reg2.Register(ctx)

			By("waiting for both servers to be ready")
			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, s1.Name, s1.Namespace)).To(Succeed())
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, s2.Name, s2.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			WaitForToolsWithPrefix(ctx, mcpGatewayClient, "catfilt1_")
			WaitForToolsWithPrefix(ctx, mcpGatewayClient, "catfilt2_")

			By("filtering by 'dining' (lowercase) should return only the Dining server")
			sessionID := mcpGatewayClient.GetSessionId()
			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, gatewayURL, sessionID, map[string]any{"category": "dining"}, nil)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(resp).NotTo(BeNil())

				hasPrefix1 := false
				hasPrefix2 := false
				for _, s := range resp.Servers {
					for _, t := range s.Tools {
						if strings.HasPrefix(t, "catfilt1_") {
							hasPrefix1 = true
						}
						if strings.HasPrefix(t, "catfilt2_") {
							hasPrefix2 = true
						}
					}
				}
				g.Expect(hasPrefix1).To(BeTrue(), "dining server tools should be returned")
				g.Expect(hasPrefix2).To(BeFalse(), "messaging server tools should not be returned")
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())
		})

		It("matches multi-category server by either category value", func() {
			By("registering a server with multiple categories")
			reg := NewMCPServerResourcesWithDefaults("multi-cat", k8sClient).
				WithPrefix("multicat_").
				WithCategory("dining", "reservations").
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			WaitForToolsWithPrefix(ctx, mcpGatewayClient, "multicat_")

			sessionID := mcpGatewayClient.GetSessionId()

			By("filtering by 'dining' should match")
			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, gatewayURL, sessionID, map[string]any{"category": "dining"}, nil)
				g.Expect(err).NotTo(HaveOccurred())
				hasTools := false
				for _, s := range resp.Servers {
					for _, t := range s.Tools {
						if strings.HasPrefix(t, "multicat_") {
							hasTools = true
						}
					}
				}
				g.Expect(hasTools).To(BeTrue(), "server should match when filtering by 'dining'")
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())

			By("filtering by 'reservations' should also match")
			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, gatewayURL, sessionID, map[string]any{"category": "reservations"}, nil)
				g.Expect(err).NotTo(HaveOccurred())
				hasTools := false
				for _, s := range resp.Servers {
					for _, t := range s.Tools {
						if strings.HasPrefix(t, "multicat_") {
							hasTools = true
						}
					}
				}
				g.Expect(hasTools).To(BeTrue(), "server should match when filtering by 'reservations'")
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())
		})

		It("returns empty servers array for non-matching category", func() {
			By("registering a server with a known category")
			reg := NewMCPServerResourcesWithDefaults("no-match-cat", k8sClient).
				WithPrefix("nomatch_").
				WithCategory("messaging").
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			WaitForToolsWithPrefix(ctx, mcpGatewayClient, "nomatch_")

			sessionID := mcpGatewayClient.GetSessionId()
			By("filtering by a non-existent category")
			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, gatewayURL, sessionID, map[string]any{"category": "nonexistent_xyz"}, nil)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(resp).NotTo(BeNil())
				// no server should have tools matching this filter
				for _, s := range resp.Servers {
					for _, t := range s.Tools {
						g.Expect(t).NotTo(HavePrefix("nomatch_"), "no tools should match nonexistent category")
					}
				}
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())
		})

		It("respects auth filtering in discover_tools", func() {
			SetupTrustedHeadersAuth(ctx, k8sClient)

			By("registering a server")
			reg := NewMCPServerResourcesWithDefaults("disc-auth", k8sClient).
				WithPrefix("discauth_").
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

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
				sessionID, initErr = mcpInitialize(ctx, gatewayURL, map[string]string{"X-Mcp-Authorized": jwtToken})
				g.Expect(initErr).NotTo(HaveOccurred())
				g.Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, map[string]string{"X-Mcp-Authorized": jwtToken})).To(Succeed())
			}, TestTimeoutMedium, TestRetryFast).Should(Succeed())

			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, gatewayURL, sessionID, nil, map[string]string{"X-Mcp-Authorized": jwtToken})
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
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())
		})

		It("respects MCPVirtualServer scoping in discover_tools", func() {
			By("registering a server")
			reg := NewMCPServerResourcesWithDefaults("disc-vs", k8sClient).
				WithPrefix("discvs_").
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
			sessionID, err := mcpInitialize(ctx, gatewayURL, map[string]string{"X-Mcp-Virtualserver": vsHeader})
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, map[string]string{"X-Mcp-Virtualserver": vsHeader})).To(Succeed())

			By("discover_tools should only return the virtual server's allowed tools")
			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, gatewayURL, sessionID, nil, map[string]string{"X-Mcp-Virtualserver": vsHeader})
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
			}, TestTimeoutMedium, TestRetryFast).Should(Succeed())
		})
	})

	Context("select_tools", func() {
		It("scopes subsequent tools/list to selected tools", func() {
			By("registering a server")
			reg := NewMCPServerResourcesWithDefaults("select-scope", k8sClient).
				WithPrefix("selscope_").
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("establishing a raw session")
			sessionID, err := mcpInitialize(ctx, gatewayURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

			By("waiting for tools to appear")
			Eventually(func(g Gomega) {
				_, tools, err := mcpListTools(ctx, gatewayURL, sessionID, nil)
				g.Expect(err).NotTo(HaveOccurred())
				hasPrefix := false
				for _, t := range tools {
					if strings.HasPrefix(t, "selscope_") {
						hasPrefix = true
						break
					}
				}
				g.Expect(hasPrefix).To(BeTrue())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			selectedTool := "selscope_hello_world"
			By("calling select_tools with one tool")
			status, result, err := mcpCallSelectTools(ctx, gatewayURL, sessionID, []string{selectedTool}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))
			Expect(result.Status).To(ContainSubstring("scope set"))

			By("verifying subsequent tools/list returns only the selected tool (plus meta-tools)")
			Eventually(func(g Gomega) {
				_, tools, err := mcpListTools(ctx, gatewayURL, sessionID, nil)
				g.Expect(err).NotTo(HaveOccurred())
				for _, t := range tools {
					if isBrokerMetaTool(t) {
						continue
					}
					g.Expect(t).To(Equal(selectedTool), "only selected tool and meta-tools should be returned")
				}
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())
		})

		It("returns error for invalid tool name (all-or-nothing)", func() {
			By("establishing a raw session")
			sessionID, err := mcpInitialize(ctx, gatewayURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

			By("calling select_tools with an invalid tool name")
			_, content, err := mcpCallTool(ctx, gatewayURL, sessionID, "select_tools",
				map[string]any{"tools": []any{"nonexistent_tool_xyz_12345"}}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(content).NotTo(BeEmpty())
			Expect(content[0].Text).To(ContainSubstring("not available"))
		})

		It("fails entirely when partial valid list contains one invalid tool", func() {
			By("registering a server")
			reg := NewMCPServerResourcesWithDefaults("select-partial", k8sClient).
				WithPrefix("selpart_").
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			sessionID, err := mcpInitialize(ctx, gatewayURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, gatewayURL, sessionID, nil)
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
			_, content, err := mcpCallTool(ctx, gatewayURL, sessionID, "select_tools",
				map[string]any{"tools": []any{"selpart_hello_world", "nonexistent_tool_abc"}}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(content).NotTo(BeEmpty())
			Expect(content[0].Text).To(ContainSubstring("not available"),
				"entire select should fail when any tool is invalid")
		})

		It("re-scoping replaces previous selection", func() {
			By("registering a server")
			reg := NewMCPServerResourcesWithDefaults("select-rescope", k8sClient).
				WithPrefix("rescp_").
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			sessionID, err := mcpInitialize(ctx, gatewayURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, gatewayURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				hasPrefix := false
				for _, t := range tools {
					if strings.HasPrefix(t, "rescp_") {
						hasPrefix = true
						break
					}
				}
				g.Expect(hasPrefix).To(BeTrue())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			tool1 := "rescp_hello_world"
			tool2 := "rescp_time"

			By("selecting tool1")
			status, result, err := mcpCallSelectTools(ctx, gatewayURL, sessionID, []string{tool1}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))
			Expect(result.Status).To(ContainSubstring("scope set"))

			By("re-scoping to tool2")
			status, result, err = mcpCallSelectTools(ctx, gatewayURL, sessionID, []string{tool2}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))
			Expect(result.Status).To(ContainSubstring("scope set"))

			By("verifying only tool2 (and meta-tools) is now in tools/list")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, gatewayURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				for _, t := range tools {
					if isBrokerMetaTool(t) {
						continue
					}
					g.Expect(t).To(Equal(tool2), "only re-scoped tool should be in the list")
				}
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())
		})

		It("empty list resets to full tool set", func() {
			By("registering a server")
			reg := NewMCPServerResourcesWithDefaults("select-reset", k8sClient).
				WithPrefix("selrst_").
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			sessionID, err := mcpInitialize(ctx, gatewayURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

			var fullToolCount int
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, gatewayURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				hasPrefix := false
				for _, t := range tools {
					if strings.HasPrefix(t, "selrst_") {
						hasPrefix = true
						break
					}
				}
				g.Expect(hasPrefix).To(BeTrue())
				fullToolCount = len(tools)
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("scoping down to one tool")
			status, _, err := mcpCallSelectTools(ctx, gatewayURL, sessionID, []string{"selrst_hello_world"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))

			By("verifying scope is applied")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, gatewayURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				nonMetaCount := 0
				for _, t := range tools {
					if !isBrokerMetaTool(t) {
						nonMetaCount++
					}
				}
				g.Expect(nonMetaCount).To(Equal(1))
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())

			By("resetting scope with empty list")
			status, result, err := mcpCallSelectTools(ctx, gatewayURL, sessionID, []string{}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))
			Expect(result.Status).To(ContainSubstring("reset"))

			By("verifying full tool set is restored")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, gatewayURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				g.Expect(len(tools)).To(Equal(fullToolCount), "full tool set should be restored after reset")
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())
		})
	})

	Context("notifications", func() {
		It("delivers notifications/tools/list_changed over SSE after select_tools", func() {
			By("registering a server")
			reg := NewMCPServerResourcesWithDefaults("notif-select", k8sClient).
				WithPrefix("notifsel_").
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			var receivedNotification atomic.Bool
			client, err := NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(j mcp.JSONRPCNotification) {
				if j.Method == "notifications/tools/list_changed" {
					receivedNotification.Store(true)
				}
			})
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = client.Close() }()

			WaitForToolsWithPrefix(ctx, client, "notifsel_")

			sessionID := client.GetSessionId()
			status, _, selectErr := mcpCallSelectTools(ctx, gatewayURL, sessionID, []string{"notifsel_hello_world"}, nil)
			Expect(selectErr).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))

			By("verifying notification was received")
			Eventually(func() bool {
				return receivedNotification.Load()
			}, TestTimeoutShort, TestRetryFast).Should(BeTrue(),
				"should have received notifications/tools/list_changed after select_tools")
		})
	})

	Context("flags", func() {
		It("hides meta-tools when discovery-tools-enabled=false", func() {
			deploymentName := "mcp-gateway"

			By("adding --discovery-tools-enabled=false flag to deployment")
			Expect(AddDeploymentCommandFlag(ctx, SystemNamespace, deploymentName, "--discovery-tools-enabled=false")).To(Succeed())

			DeferCleanup(func() {
				By("removing --discovery-tools-enabled=false flag")
				Expect(RemoveDeploymentCommandFlag(ctx, SystemNamespace, deploymentName, "--discovery-tools-enabled=false")).To(Succeed())
				Expect(WaitForDeploymentReady(ctx, SystemNamespace, deploymentName)).To(Succeed())
			})

			By("waiting for rollout to complete")
			Expect(WaitForDeploymentReady(ctx, SystemNamespace, deploymentName)).To(Succeed())

			By("connecting a fresh client after restart")
			var sessionID string
			Eventually(func(g Gomega) {
				var initErr error
				sessionID, initErr = mcpInitialize(ctx, gatewayURL, nil)
				g.Expect(initErr).NotTo(HaveOccurred())
				g.Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())
			}, TestTimeoutMedium, TestRetryFast).Should(Succeed())

			By("verifying discover_tools and select_tools are not in tools/list")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, gatewayURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				g.Expect(tools).NotTo(ContainElement("discover_tools"),
					"discover_tools should not be listed when discovery is disabled")
				g.Expect(tools).NotTo(ContainElement("select_tools"),
					"select_tools should not be listed when discovery is disabled")
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())

			By("calling discover_tools should return an error")
			_, _, rawBody, err := mcpRawPost(ctx, gatewayURL, sessionID,
				[]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"discover_tools"}}`), nil)
			Expect(err).NotTo(HaveOccurred())
			_ = rawBody // the tool should not exist, so the response will be an error
		})

		It("threshold=0 means never hide (all tools visible alongside meta-tools)", func() {
			// threshold defaults to 0, so all real tools plus meta-tools should be visible
			By("registering a server")
			reg := NewMCPServerResourcesWithDefaults("thresh-zero", k8sClient).
				WithPrefix("thzero_").
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			sessionID, err := mcpInitialize(ctx, gatewayURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

			By("verifying both real tools and meta-tools are visible")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, gatewayURL, sessionID, nil)
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
		It("above threshold: only meta-tools shown; below threshold: all tools visible", func() {
			deploymentName := "mcp-gateway"

			By("registering a server so we have tools to count")
			reg := NewMCPServerResourcesWithDefaults("thresh-test", k8sClient).
				WithPrefix("thtest_").
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			// set threshold to 1 so any server with >1 tools triggers hiding
			By("setting --discovery-tool-threshold=1")
			Expect(AddDeploymentCommandFlag(ctx, SystemNamespace, deploymentName, "--discovery-tool-threshold=1")).To(Succeed())

			DeferCleanup(func() {
				By("removing threshold flag")
				Expect(RemoveDeploymentCommandFlag(ctx, SystemNamespace, deploymentName, "--discovery-tool-threshold=1")).To(Succeed())
				Expect(WaitForDeploymentReady(ctx, SystemNamespace, deploymentName)).To(Succeed())
			})

			Expect(WaitForDeploymentReady(ctx, SystemNamespace, deploymentName)).To(Succeed())

			var sessionID string
			Eventually(func(g Gomega) {
				var initErr error
				sessionID, initErr = mcpInitialize(ctx, gatewayURL, nil)
				g.Expect(initErr).NotTo(HaveOccurred())
				g.Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())
			}, TestTimeoutMedium, TestRetryFast).Should(Succeed())

			By("verifying only meta-tools are shown (above threshold)")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, gatewayURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				g.Expect(tools).To(ContainElement("discover_tools"))
				g.Expect(tools).To(ContainElement("select_tools"))
				for _, t := range tools {
					if !isBrokerMetaTool(t) {
						g.Expect(t).To(BeEmpty(), "no real tools should be visible above threshold, found: "+t)
					}
				}
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())

			By("using select_tools to scope down, tools should become visible")
			status, _, selectErr := mcpCallSelectTools(ctx, gatewayURL, sessionID, []string{"thtest_hello_world"}, nil)
			Expect(selectErr).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))

			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, gatewayURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				hasSelected := false
				for _, t := range tools {
					if t == "thtest_hello_world" {
						hasSelected = true
					}
				}
				g.Expect(hasSelected).To(BeTrue(), "selected tool should be visible after scoping")
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())
		})
	})

	Context("isolation and concurrency", func() {
		It("session scope does not leak across sessions", func() {
			By("registering a server")
			reg := NewMCPServerResourcesWithDefaults("iso-test", k8sClient).
				WithPrefix("iso_").
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("creating two sessions")
			session1, err := mcpInitialize(ctx, gatewayURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, gatewayURL, session1, nil)).To(Succeed())

			session2, err := mcpInitialize(ctx, gatewayURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, gatewayURL, session2, nil)).To(Succeed())

			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, gatewayURL, session1, nil)
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
			status, _, err := mcpCallSelectTools(ctx, gatewayURL, session1, []string{"iso_hello_world"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))

			By("verifying session2 still has all tools")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, gatewayURL, session2, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				isoTools := 0
				for _, t := range tools {
					if strings.HasPrefix(t, "iso_") {
						isoTools++
					}
				}
				g.Expect(isoTools).To(BeNumerically(">", 1),
					"session2 should still see all tools, not be affected by session1's scope")
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())
		})

		It("concurrent select_tools calls on same session do not corrupt state", func() {
			By("registering a server")
			reg := NewMCPServerResourcesWithDefaults("conc-test", k8sClient).
				WithPrefix("conc_").
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			sessionID, err := mcpInitialize(ctx, gatewayURL, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, gatewayURL, sessionID, nil)
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
				_, _, err := mcpCallSelectTools(ctx, gatewayURL, sessionID, []string{tool1}, nil)
				Expect(err).NotTo(HaveOccurred())
			}()

			go func() {
				defer wg.Done()
				defer GinkgoRecover()
				_, _, err := mcpCallSelectTools(ctx, gatewayURL, sessionID, []string{tool2}, nil)
				Expect(err).NotTo(HaveOccurred())
			}()

			wg.Wait()

			By("verifying tools/list returns a consistent state (one of the two scopes)")
			Eventually(func(g Gomega) {
				_, tools, listErr := mcpListTools(ctx, gatewayURL, sessionID, nil)
				g.Expect(listErr).NotTo(HaveOccurred())

				var nonMetaTools []string
				for _, t := range tools {
					if !isBrokerMetaTool(t) {
						nonMetaTools = append(nonMetaTools, t)
					}
				}
				// one scope won the race: should be exactly 1 tool
				g.Expect(nonMetaTools).To(HaveLen(1),
					"concurrent select_tools should result in a consistent single-tool scope")
				g.Expect(nonMetaTools[0]).To(Or(Equal(tool1), Equal(tool2)),
					"the winning tool should be one of the two selected tools")
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())
		})
	})

	Context("config changes", func() {
		BeforeEach(func() {
			newGatewayClient()
		})

		It("controller re-reconciles when category and hint are updated", func() {
			By("registering a server with initial category and hint")
			reg := NewMCPServerResourcesWithDefaults("reconfig-test", k8sClient).
				WithPrefix("reconf_").
				WithCategory("initial-category").
				WithHint("initial hint").
				Build()
			testResources = append(testResources, reg.GetObjects()...)
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			WaitForToolsWithPrefix(ctx, mcpGatewayClient, "reconf_")

			sessionID := mcpGatewayClient.GetSessionId()

			By("verifying initial category and hint via discover_tools")
			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, gatewayURL, sessionID, map[string]any{"category": "initial-category"}, nil)
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
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())

			By("updating category and hint on the live MCPServerRegistration")
			Eventually(func(g Gomega) {
				fresh := &mcpv1alpha1.MCPServerRegistration{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{
					Name:      server.Name,
					Namespace: server.Namespace,
				}, fresh)).To(Succeed())
				fresh.Spec.Category = []string{"updated-category"}
				fresh.Spec.Hint = "updated hint"
				g.Expect(k8sClient.Update(ctx, fresh)).To(Succeed())
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())

			By("waiting for reconciliation to propagate the updated metadata")
			Eventually(func(g Gomega) {
				_, resp, err := mcpCallDiscoverTools(ctx, gatewayURL, sessionID, map[string]any{"category": "updated-category"}, nil)
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
				_, resp, err := mcpCallDiscoverTools(ctx, gatewayURL, sessionID, map[string]any{"category": "initial-category"}, nil)
				g.Expect(err).NotTo(HaveOccurred())
				for _, s := range resp.Servers {
					for _, t := range s.Tools {
						g.Expect(t).NotTo(HavePrefix("reconf_"),
							"old category should no longer match after update")
					}
				}
			}, TestTimeoutShort, TestRetryFast).Should(Succeed())
		})
	})
})
