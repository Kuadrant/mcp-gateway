//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	dualProtoExtName   = "dual-proto-ext"
	dualProtoNamespace = "mcp-dual-protocol"
)

// tests prove the gateway is protocol-agnostic: a single instance serves both
// 2025-11-25 and 2026-07-28 clients, filtering tools/list by protocol version.
var _ = Describe("Dual Protocol Gateway", Ordered, func() {
	var (
		testResources []client.Object
		dpExt         *MCPGatewayExtensionSetup
		dpURL         = Protocol2026GatewayURL
	)

	BeforeAll(func() {
		By("Creating dual-protocol namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   dualProtoNamespace,
				Labels: map[string]string{"e2e": "test"},
			},
		}
		_ = k8sClient.Delete(ctx, ns)
		Eventually(func(g Gomega) {
			err := k8sClient.Create(ctx, ns)
			g.Expect(client.IgnoreAlreadyExists(err)).NotTo(HaveOccurred())
		}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

		By("Creating MCPGatewayExtension (serves both protocols)")
		dpExt = NewMCPGatewayExtensionSetup(k8sClient).
			WithName(dualProtoExtName).
			InNamespace(dualProtoNamespace).
			TargetingGateway(GatewayName, GatewayNamespace).
			WithSectionName(Protocol2026ListenerName).
			WithPublicHost(Protocol2026PublicHost).
			Build()
		dpExt.Clean(ctx).Register(ctx)

		By("Waiting for MCPGatewayExtension to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPGatewayExtensionReady(ctx, k8sClient, dualProtoExtName, dualProtoNamespace)).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Waiting for deployment to be ready")
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, dualProtoNamespace, "mcp-gateway")).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Registering a 2025-only server (server1)")
		reg25 := NewTestResources("dp-stateful", k8sClient).
			InNamespace(dualProtoNamespace).
			WithBackendTarget("mcp-test-server1", 9090).
			WithBackendNamespace(TestServerNameSpace).
			WithHostname(protocol2026ServerHost("stateful")).
			WithPrefix("sf_").
			WithSectionName(Protocol2026ListenerName).
			WithParentGateway(GatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, reg25.GetObjects()...)
		server25 := reg25.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server25.Name, server25.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Registering a dual-protocol server (stateless-server, supports 2025+2026)")
		reg26 := NewTestResources("dp-stateless", k8sClient).
			InNamespace(dualProtoNamespace).
			WithBackendTarget("mcp-test-stateless-server", 9090).
			WithBackendNamespace(TestServerNameSpace).
			WithHostname(protocol2026ServerHost("stateless")).
			WithPrefix("sl_").
			WithSectionName(Protocol2026ListenerName).
			WithParentGateway(GatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, reg26.GetObjects()...)
		server26 := reg26.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server26.Name, server26.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	AfterAll(func() {
		// leave resources for post-failure debugging; BeforeAll Clean() handles cleanup on next run
	})

	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			DumpClusterState(ctx, dualProtoNamespace, GatewayNamespace)
		}
	})

	newStatelessClient := func() *mcp.ClientSession {
		var c *mcp.ClientSession
		Eventually(func(g Gomega) {
			var err error
			c, err = NewStatelessClient(ctx, dpURL)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		return c
	}

	newStatefulClient := func() *mcp.ClientSession {
		var c *mcp.ClientSession
		Eventually(func(g Gomega) {
			var err error
			c, err = NewStatefulClient(ctx, dpURL)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		return c
	}

	toolNames := func(tools []*mcp.Tool) []string {
		names := make([]string, len(tools))
		for i, t := range tools {
			names[i] = t.Name
		}
		return names
	}

	waitForToolsWithPrefix := func(c interface {
		ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
	}, prefix string) {
		Eventually(func(g Gomega) {
			result, err := c.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(slices.ContainsFunc(toolNames(result.Tools), func(n string) bool {
				return strings.HasPrefix(n, prefix)
			})).To(BeTrue(), "tools with prefix %s should appear", prefix)
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	}

	Context("protocol-filtered tools/list", func() {
		It("[Happy,DualProtocol] 2026 client sees only 2026-capable backend tools", func() {
			c := newStatelessClient()
			defer func() { _ = c.Close() }()

			var names []string
			Eventually(func(g Gomega) {
				result, err := c.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				names = toolNames(result.Tools)
				g.Expect(slices.ContainsFunc(names, func(n string) bool {
					return strings.HasPrefix(n, "sl_")
				})).To(BeTrue(), "2026 client should see sl_ tools")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			Expect(slices.ContainsFunc(names, func(n string) bool {
				return strings.HasPrefix(n, "sf_")
			})).To(BeFalse(), "2026 client should NOT see sf_ (stateful) tools")
		})

		It("[Happy,DualProtocol] 2025 client sees stateful and dual-protocol backend tools", func() {
			c := newStatefulClient()
			defer func() { _ = c.Close() }()

			var names []string
			Eventually(func(g Gomega) {
				result, err := c.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				names = toolNames(result.Tools)
				GinkgoWriter.Printf("ListTools err=%v tools=%d\n toolNames=%v\n", err, len(result.Tools), names)
				g.Expect(slices.ContainsFunc(names, func(n string) bool {
					return strings.HasPrefix(n, "sf_")
				})).To(BeTrue(), "2025 client should see sf_ tools")
				// stateless-server uses StreamableHTTP with Stateless:true, so the
				// SDK reports supportedVersions for both 2025 and 2026 — a 2025
				// client sees sl_ tools too.
				g.Expect(slices.ContainsFunc(names, func(n string) bool {
					return strings.HasPrefix(n, "sl_")
				})).To(BeTrue(), "2025 client should see sl_ tools (dual-protocol server)")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
		})

		It("[Happy,DualProtocol] 2025 client can call tools on stateful backend", func() {
			c := newStatefulClient()
			defer func() { _ = c.Close() }()

			waitForToolsWithPrefix(c, "sf_")

			Eventually(func(g Gomega) {
				result, err := c.CallTool(ctx, &mcp.CallToolParams{
					Name:      "sf_greet",
					Arguments: map[string]any{"name": "dual-test"},
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(result.Content).NotTo(BeEmpty())
				text, ok := result.Content[0].(*mcp.TextContent)
				g.Expect(ok).To(BeTrue())
				g.Expect(text.Text).To(ContainSubstring("dual-test"))
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		})

		It("[Happy,DualProtocol] 2026 client can call tools on stateless backend", func() {
			c := newStatelessClient()
			defer func() { _ = c.Close() }()

			waitForToolsWithPrefix(c, "sl_")

			Eventually(func(g Gomega) {
				result, err := c.CallTool(ctx, &mcp.CallToolParams{
					Name:      "sl_hello_world",
					Arguments: map[string]any{"name": "dual-test"},
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(result.Content).NotTo(BeEmpty())
				text, ok := result.Content[0].(*mcp.TextContent)
				g.Expect(ok).To(BeTrue())
				g.Expect(text.Text).To(ContainSubstring("Hello, dual-test!"))
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		})
	})

	It("[DualProtocol] dual-version server tools visible to both clients", func() {
		By("Registering a dual-protocol backend (user-specific test server, Stateless: true)")
		reg := NewTestResources("dp-dual", k8sClient).
			InNamespace(dualProtoNamespace).
			WithBackendTarget("mcp-test-user-specific-server", 9090).
			WithBackendNamespace(TestServerNameSpace).
			WithHostname(protocol2026ServerHost("dual")).
			WithPrefix("dual_").
			WithSectionName(Protocol2026ListenerName).
			WithParentGateway(GatewayName, GatewayNamespace).
			Build()
		defer func() {
			for _, obj := range reg.GetObjects() {
				CleanupResource(ctx, k8sClient, obj)
			}
		}()
		server := reg.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("2026 client sees dual_ tools")
		c26 := newStatelessClient()
		defer func() { _ = c26.Close() }()

		Eventually(func(g Gomega) {
			result, err := c26.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(slices.ContainsFunc(toolNames(result.Tools), func(n string) bool {
				return strings.HasPrefix(n, "dual_")
			})).To(BeTrue(), "2026 client should see dual_ tools")
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("2026 client can call dual_server_info")
		res26, err := c26.CallTool(ctx, &mcp.CallToolParams{Name: "dual_server_info"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res26.Content).NotTo(BeEmpty())
		text26, ok := res26.Content[0].(*mcp.TextContent)
		Expect(ok).To(BeTrue())
		Expect(text26.Text).To(ContainSubstring("server=user-specific-test-server"))

		By("2025 client sees dual_ tools")
		c25 := newStatefulClient()
		defer func() { _ = c25.Close() }()

		Eventually(func(g Gomega) {
			result, err := c25.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(slices.ContainsFunc(toolNames(result.Tools), func(n string) bool {
				return strings.HasPrefix(n, "dual_")
			})).To(BeTrue(), "2025 client should see dual_ tools")
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("2025 client can call dual_server_info")
		res25, err := c25.CallTool(ctx, &mcp.CallToolParams{Name: "dual_server_info"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res25.Content).NotTo(BeEmpty())
		text25, ok := res25.Content[0].(*mcp.TextContent)
		Expect(ok).To(BeTrue())
		Expect(text25.Text).To(ContainSubstring("server=user-specific-test-server"))
	})

	Context("broker meta-tools visibility", func() {
		It("[DualProtocol] 2025 client sees discover_tools and select_tools", func() {
			c := newStatefulClient()
			defer func() { _ = c.Close() }()

			Eventually(func(g Gomega) {
				result, err := c.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				names := toolNames(result.Tools)
				g.Expect(slices.Contains(names, "discover_tools")).To(BeTrue(), "2025 client should see discover_tools")
				g.Expect(slices.Contains(names, "select_tools")).To(BeTrue(), "2025 client should see select_tools")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
		})

		It("[DualProtocol] 2026 client does not see discover_tools or select_tools", func() {
			c := newStatelessClient()
			defer func() { _ = c.Close() }()

			Eventually(func(g Gomega) {
				result, err := c.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				names := toolNames(result.Tools)
				g.Expect(slices.ContainsFunc(names, func(n string) bool {
					return strings.HasPrefix(n, "sl_")
				})).To(BeTrue(), "should have tools loaded")
				g.Expect(slices.Contains(names, "discover_tools")).To(BeFalse(), "2026 client should NOT see discover_tools")
				g.Expect(slices.Contains(names, "select_tools")).To(BeFalse(), "2026 client should NOT see select_tools")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
		})
	})

	Context("user-specific tools by protocol", func() {
		It("[DualProtocol,UserSpecificList] 2026 client gets per-user tools from stateless server", func() {
			reg := NewTestResources("dp-uspec-sl", k8sClient).
				InNamespace(dualProtoNamespace).
				WithBackendTarget("mcp-test-stateless-server", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(protocol2026ServerHost("uspec-sl")).
				WithPrefix("usl_").
				WithUserSpecificList().
				WithSectionName(Protocol2026ListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			// leave resources for debugging
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("user-a sees list_repos via stateless client")
			var cA *mcp.ClientSession
			Eventually(func(g Gomega) {
				var err error
				cA, err = NewStatelessClientWithHeaders(ctx, dpURL, map[string]string{
					"Authorization": "Bearer user-a-token",
				})
				g.Expect(err).NotTo(HaveOccurred())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
			defer func() { _ = cA.Close() }()

			Eventually(func(g Gomega) {
				result, err := cA.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				names := toolNames(result.Tools)
				GinkgoWriter.Printf("tools/list returned: %v\n", names)
				g.Expect(slices.Contains(names, "usl_list_repos")).To(BeTrue(),
					"user-a should see usl_list_repos, got: %v", names)
				g.Expect(slices.Contains(names, "usl_run_pipeline")).To(BeFalse(),
					"user-a should NOT see usl_run_pipeline")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
		})

		It("[DualProtocol,UserSpecificList] 2025 client sees tools from dual-protocol UserSpecificList server", func() {
			reg := NewTestResources("dp-uspec-cross", k8sClient).
				InNamespace(dualProtoNamespace).
				WithBackendTarget("mcp-test-stateless-server", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(protocol2026ServerHost("uspec-cross")).
				WithPrefix("ucross_").
				WithUserSpecificList().
				WithSectionName(Protocol2026ListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			defer func() {
				for _, obj := range reg.GetObjects() {
					CleanupResource(ctx, k8sClient, obj)
				}
			}()
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("confirming ucross_ tools are visible via a 2026 client")
			slC := newStatelessClient()
			defer func() { _ = slC.Close() }()
			waitForToolsWithPrefix(slC, "ucross_")

			// stateless-server with Stateless:true reports supportedVersions for
			// both 2025 and 2026, so a 2025 client sees ucross_ tools too.
			By("2025 client sees ucross_ tools from dual-protocol server")
			c := newStatefulClient()
			defer func() { _ = c.Close() }()

			waitForToolsWithPrefix(c, "sf_")

			Eventually(func(g Gomega) {
				result, err := c.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				names := toolNames(result.Tools)
				g.Expect(slices.ContainsFunc(names, func(n string) bool {
					return strings.HasPrefix(n, "ucross_")
				})).To(BeTrue(), "2025 client should see ucross_ tools from dual-protocol server, got: %v", names)
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
		})
	})

	Context("2026 protocol features", func() {
		It("[Happy,Protocol2026] server/discover returns capabilities", func() {
			c := newStatelessClient()
			defer func() { _ = c.Close() }()

			initResult := c.InitializeResult()
			Expect(initResult).NotTo(BeNil())
			Expect(initResult.ProtocolVersion).To(Equal("2026-07-28"))
		})

		It("[Happy,Protocol2026] tool call without prefix via stateless gateway", func() {
			By("registering an MCPServerRegistration with no prefix")
			reg := NewTestResources("dp-noprefix", k8sClient).
				InNamespace(dualProtoNamespace).
				WithBackendTarget("mcp-test-stateless-server", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithHostname(protocol2026ServerHost("noprefix")).
				WithSectionName(Protocol2026ListenerName).
				WithParentGateway(GatewayName, GatewayNamespace).
				Build()
			defer func() {
				for _, obj := range reg.GetObjects() {
					CleanupResource(ctx, k8sClient, obj)
				}
			}()
			server := reg.Register(ctx)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server.Name, server.Namespace)).To(Succeed())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			c := newStatelessClient()
			defer func() { _ = c.Close() }()

			Eventually(func(g Gomega) {
				result, err := c.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(slices.Contains(toolNames(result.Tools), "hello_world")).To(BeTrue())
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			result, err := c.CallTool(ctx, &mcp.CallToolParams{
				Name:      "hello_world",
				Arguments: map[string]any{"name": "no-prefix"},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.Content).NotTo(BeEmpty())
			text, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())
			Expect(text.Text).To(ContainSubstring("Hello, no-prefix!"))
		})

		It("[Protocol2026] header-body mismatch rejection", func() {
			c := newStatelessClient()
			defer func() { _ = c.Close() }()

			waitForToolsWithPrefix(c, "sl_")

			body, err := mcp2026Payload("tools/call", map[string]any{
				"name": "sl_headers",
			})
			Expect(err).NotTo(HaveOccurred())

			status, respBody, err := mcp2026RawPost(ctx, dpURL, body, mcp2026Headers("tools/call", "sl_hello_world"))
			GinkgoWriter.Println("response body", respBody)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))
			Expect(respBody).To(ContainSubstring("HeaderMismatch"))
		})

		It("[Protocol2026] unknown tool error", func() {
			c := newStatelessClient()
			defer func() { _ = c.Close() }()

			waitForToolsWithPrefix(c, "sl_")

			_, err := c.CallTool(ctx, &mcp.CallToolParams{
				Name: "sl_nonexistent_tool",
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unknown tool"))
		})
	})

	Context("version-aware server/discover", func() {
		It("[Happy,DualProtocol] dual-protocol gateway negotiates 2026 with SDK client", func() {
			// standard SDK client (no legacy transport) on a gateway with both backends
			c := newStatelessClient()
			defer func() { _ = c.Close() }()

			initResult := c.InitializeResult()
			Expect(initResult).NotTo(BeNil())
			Expect(initResult.ProtocolVersion).To(Equal("2026-07-28"),
				"SDK should negotiate 2026 when gateway has 2026 backends")
		})
	})

	Context("protocol-specific routes", func() {
		statefulURL := strings.TrimSuffix(dpURL, "/mcp") + "/mcp/stateful"

		It("[Happy,DualProtocol] /mcp/stateful returns 2025-compatible tools", func() {
			var c *mcp.ClientSession
			Eventually(func(g Gomega) {
				var err error
				c, err = NewStatefulClient(ctx, statefulURL)
				g.Expect(err).NotTo(HaveOccurred())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
			defer func() { _ = c.Close() }()

			Eventually(func(g Gomega) {
				result, err := c.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				names := toolNames(result.Tools)
				g.Expect(slices.ContainsFunc(names, func(n string) bool {
					return strings.HasPrefix(n, "sf_")
				})).To(BeTrue(), "/mcp/stateful should return sf_ tools")
				// stateless-server is dual-protocol, so sl_ tools appear for 2025 clients too
				g.Expect(slices.ContainsFunc(names, func(n string) bool {
					return strings.HasPrefix(n, "sl_")
				})).To(BeTrue(), "/mcp/stateful should return sl_ tools (dual-protocol server)")
				g.Expect(slices.Contains(names, "discover_tools")).To(BeTrue(),
					"/mcp/stateful should include discover_tools")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
		})

		It("[DualProtocol] /mcp/stateful tools/call succeeds", func() {
			var c *mcp.ClientSession
			Eventually(func(g Gomega) {
				var err error
				c, err = NewStatefulClient(ctx, statefulURL)
				g.Expect(err).NotTo(HaveOccurred())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
			defer func() { _ = c.Close() }()

			waitForToolsWithPrefix(c, "sf_")

			Eventually(func(g Gomega) {
				result, err := c.CallTool(ctx, &mcp.CallToolParams{
					Name:      "sf_greet",
					Arguments: map[string]any{"name": "route-test"},
				})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(result.Content).NotTo(BeEmpty())
				text, ok := result.Content[0].(*mcp.TextContent)
				g.Expect(ok).To(BeTrue())
				g.Expect(text.Text).To(ContainSubstring("route-test"))
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		})
	})

	Context("2026 prompt routing", func() {
		It("[Happy,Protocol2026] prompt listing and get via stateless gateway", func() {
			c := newStatelessClient()
			defer func() { _ = c.Close() }()

			Eventually(func(g Gomega) {
				result, err := c.ListPrompts(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				names := make([]string, len(result.Prompts))
				for i, p := range result.Prompts {
					names[i] = p.Name
				}
				g.Expect(slices.ContainsFunc(names, func(p string) bool {
					return strings.HasPrefix(p, "sl_")
				})).To(BeTrue(), "prompts with prefix sl_ should appear")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			promptResult, err := c.GetPrompt(ctx, &mcp.GetPromptParams{
				Name:      "sl_greeting",
				Arguments: map[string]string{"name": "e2e"},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(promptResult).NotTo(BeNil())
			Expect(promptResult.Messages).NotTo(BeEmpty())
			text, ok := promptResult.Messages[0].Content.(*mcp.TextContent)
			Expect(ok).To(BeTrue())
			Expect(text.Text).To(ContainSubstring("greet"))
			Expect(text.Text).To(ContainSubstring("e2e"))
		})
	})

	Context("broker 2026 cache metadata", func() {
		promptNames := func(prompts []*mcp.Prompt) []string {
			names := make([]string, len(prompts))
			for i, p := range prompts {
				names[i] = p.Name
			}
			return names
		}

		It("[Happy,Broker2026] 2026 client tools/list returns ttlMs and cacheScope", func() {
			c := newStatelessClient()
			defer func() { _ = c.Close() }()

			waitForToolsWithPrefix(c, "sl_")

			Eventually(func(g Gomega) {
				result, err := c.ListTools(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(result.TTLMs).To(BeNumerically(">", 0),
					"2026 tools/list should include ttlMs > 0")
				g.Expect(result.CacheScope).NotTo(BeEmpty(),
					"2026 tools/list should include cacheScope")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
		})

		It("[Happy,Broker2026] 2025 client tools/list has no ttlMs or cacheScope", func() {
			c := newStatefulClient()
			defer func() { _ = c.Close() }()

			waitForToolsWithPrefix(c, "sf_")

			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/list"}`, jsonRPCID.Add(1))

			var respBody string
			Eventually(func(g Gomega) {
				status, rb, _, postErr := mcpRawPost(ctx, dpURL, c.ID(), []byte(body), nil)
				g.Expect(postErr).NotTo(HaveOccurred())
				g.Expect(status).To(Equal(200))

				var msg struct {
					Result json.RawMessage `json:"result"`
				}
				g.Expect(json.Unmarshal([]byte(rb), &msg)).To(Succeed(),
					"response should be valid JSON-RPC")
				g.Expect(msg.Result).NotTo(BeNil(),
					"response should contain a result field")

				var result struct {
					Tools []json.RawMessage `json:"tools"`
				}
				g.Expect(json.Unmarshal(msg.Result, &result)).To(Succeed())
				g.Expect(result.Tools).NotTo(BeEmpty(),
					"result should contain tools")

				respBody = string(msg.Result)
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

			Expect(respBody).NotTo(ContainSubstring(`"ttlMs"`),
				"2025 response should not contain ttlMs")
			Expect(respBody).NotTo(ContainSubstring(`"cacheScope"`),
				"2025 response should not contain cacheScope")
		})

		It("[Happy,Broker2026] 2026 client prompts/list excludes 2025-only prompts", func() {
			c := newStatelessClient()
			defer func() { _ = c.Close() }()

			Eventually(func(g Gomega) {
				result, err := c.ListPrompts(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				names := promptNames(result.Prompts)
				g.Expect(slices.ContainsFunc(names, func(n string) bool {
					return strings.HasPrefix(n, "sl_")
				})).To(BeTrue(), "2026 client should see sl_ prompts")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			result, err := c.ListPrompts(ctx, nil)
			Expect(err).NotTo(HaveOccurred())
			names := promptNames(result.Prompts)
			Expect(slices.ContainsFunc(names, func(n string) bool {
				return strings.HasPrefix(n, "sf_")
			})).To(BeFalse(), "2026 client should NOT see sf_ (2025-only) prompts, got: %v", names)
		})

		It("[Happy,Broker2026] 2026 client prompts/list returns cache metadata", func() {
			c := newStatelessClient()
			defer func() { _ = c.Close() }()

			Eventually(func(g Gomega) {
				result, err := c.ListPrompts(ctx, nil)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(slices.ContainsFunc(promptNames(result.Prompts), func(n string) bool {
					return strings.HasPrefix(n, "sl_")
				})).To(BeTrue(), "should have prompts loaded")
				g.Expect(result.TTLMs).To(BeNumerically(">", 0),
					"2026 prompts/list should include ttlMs > 0")
				g.Expect(result.CacheScope).NotTo(BeEmpty(),
					"2026 prompts/list should include cacheScope")
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
		})
	})

	Context("2026 upstream notifications via subscriptions/listen", func() {
		It("[Broker2026] upstream tool change propagates to broker and triggers client notification", func() {
			By("connecting a 2026 client with notification handler")
			notifCh := make(chan struct{}, 1)
			c, err := NewStatelessClientWithNotifications(ctx, dpURL, func(method string) {
				if method == "notifications/tools/list_changed" {
					select {
					case notifCh <- struct{}{}:
					default:
					}
				}
			})
			Expect(err).NotTo(HaveOccurred())
			defer func() { _ = c.Close() }()

			waitForToolsWithPrefix(c, "sl_")

			By("calling add_tool on the backend server to trigger notifications/tools/list_changed")
			dynamicTool := "e2e_dynamic_tool"
			addToolName := "sl_add_tool"
			var res *mcp.CallToolResult
			Eventually(func(g Gomega) {
				var callErr error
				res, callErr = c.CallTool(ctx, &mcp.CallToolParams{
					Name: addToolName,
					Arguments: map[string]any{
						"name":        dynamicTool,
						"description": "dynamically added tool for notification test",
					},
				})
				g.Expect(callErr).NotTo(HaveOccurred())
				g.Expect(res).NotTo(BeNil())
				g.Expect(res.IsError).To(BeFalse(), "add_tool should succeed")
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

			By("verifying the client receives a tools/list_changed notification")
			Eventually(notifCh, TestTimeoutMedium, TestRetryInterval).Should(Receive(),
				"2026 client should receive tools/list_changed after upstream adds a tool")

			By("verifying the new tool appears in tools/list")
			Eventually(func(g Gomega) {
				result, listErr := c.ListTools(ctx, nil)
				g.Expect(listErr).NotTo(HaveOccurred())
				g.Expect(slices.ContainsFunc(toolNames(result.Tools), func(n string) bool {
					return n == "sl_"+dynamicTool
				})).To(BeTrue(), "new tool sl_%s should appear in tools/list", dynamicTool)
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		})
	})
})
