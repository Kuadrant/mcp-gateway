//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// these can be used across many tests
var sharedMCPTestServer1 = "mcp-test-server1"
var sharedMCPTestServer2 = "mcp-test-server2"

// this should only be used by one test as the tests run in parallel.
var scaledMCPTestServer = "mcp-test-server3"

var _ = Describe("MCP Gateway Registration Happy Path", func() {
	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			GinkgoWriter.Println("failure detected")
		}
	})

	It("[Happy] basic registration tool invocation and unregistration", func() {
		mcpGatewayClient := newTestGatewayClient()

		By("Creating HTTPRoutes and MCP Servers")
		registration1 := NewMCPServerResourcesWithDefaults("basic-registration", k8sClient).WithPrefix("server1").Build()
		httpRoute1Name := registration1.GetHTTPRouteName()
		reg1Objects := registration1.GetObjects()
		deferCleanupResources(&reg1Objects)
		registeredServer1 := registration1.Register(ctx)

		registration2 := NewMCPServerResourcesWithDefaults("basic-registration", k8sClient).WithPrefix("server2").Build()
		reg2Objects := registration2.GetObjects()
		deferCleanupResources(&reg2Objects)
		registeredServer2 := registration2.Register(ctx)

		By("Verifying MCPServerRegistrations become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer1.Name, registeredServer1.Namespace)).To(BeNil())
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer2.Name, registeredServer2.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying HTTPRoute has Programmed condition set")
		Eventually(func(g Gomega) {
			err := VerifyHTTPRouteHasProgrammedCondition(ctx, k8sClient, httpRoute1Name, TestServerNameSpace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying MCPServerRegistrations tools are present")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer1.Spec.Prefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer1.Spec.Prefix))
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer2.Spec.Prefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer2.Spec.Prefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		toolName := fmt.Sprintf("%s%s", registeredServer1.Spec.Prefix, "hello_world")
		By("Invoking a tool")
		res, err := mcpGatewayClient.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: map[string]string{
			"name": "e2e",
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(len(res.Content)).To(BeNumerically("==", 1))
		content, ok := res.Content[0].(*mcp.TextContent)
		Expect(ok).To(BeTrue())
		Expect(content.Text).To(Equal("Hello, e2e!"))

		By("unregistering an MCPServerRegistration by Deleting the resource")
		Expect(k8sClient.Delete(ctx, registeredServer1)).To(Succeed())

		By("Verifying broker removes the deleted server")
		// do tools call check tools no longer present
		Eventually(func(g Gomega) {
			err := VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer1.Name, registeredServer1.Namespace)
			g.Expect(err).NotTo(BeNil())
			g.Expect(err.Error()).Should(ContainSubstring("not found"))
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer1.Spec.Prefix, toolsList)).To(BeFalse())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying HTTPRoute no longer has Programmed condition after MCPServerRegistration deletion")
		Eventually(func(g Gomega) {
			err := VerifyHTTPRouteNoProgrammedCondition(ctx, k8sClient, httpRoute1Name, TestServerNameSpace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

	})

	// Regression test: an HTTP backend with an explicit sectionName targeting the
	// HTTPS listener must still get an http:// URL in the broker config. The gateway
	// listener protocol (HTTPS) must not bleed into the upstream service URL.
	It("[Happy] HTTP backend with explicit HTTPS listener sectionName", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		By("Registering a plain HTTP server with sectionName targeting the HTTPS listener")
		registration := NewTestResources("https-listener-http-backend", k8sClient).
			ForInternalService(sharedMCPTestServer1, 9090).
			WithPrefix("httpback_").
			WithSectionName(GatewayListenerName).
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying tools are accessible")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolsPresent("httpback_", toolsList)).To(BeTrue())
		}, TestTimeoutConfigSync, TestRetryInterval).To(Succeed())

		By("Calling a tool to verify the broker connected over HTTP")
		res, err := mcpGatewayClient.CallTool(ctx, &mcp.CallToolParams{Name: "httpback_greet", Arguments: map[string]string{"name": "test"}})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(res.Content).NotTo(BeEmpty())
	})

	It("[Happy] should register mcp server with non-default spec.path", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		By("Registering an MCP server with a non-default path")
		registration := NewTestResources("non-default-path", k8sClient).
			ForInternalService("mcp-custom-path-server", 8080).
			WithPrefix("custompath_").
			WithPath("/v1/special/mcp").
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying tools are accessible")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, &mcp.ListToolsParams{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolsPresent("custompath_", toolsList)).To(BeTrue())
		}, TestTimeoutConfigSync, TestRetryInterval).To(Succeed())

		By("Calling a tool to verify the broker connected via the non-default path")
		res, err := mcpGatewayClient.CallTool(ctx, &mcp.CallToolParams{Name: "custompath_path_info"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(res.Content).NotTo(BeEmpty())
	})

	It("[Happy] should register mcp server with credential with the gateway and make the tools available", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		cred := BuildCredentialSecret("mcp-credential", "test-api-key-secret-toke")
		registration := NewMCPServerResourcesWithDefaults("credentials", k8sClient).
			WithCredential(cred, "token").WithBackendTarget("mcp-api-key-server", 9090).WithPrefix("auth").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)
		By("ensuring broker has failed authentication and the mcp server is not registered and the tools don't exist")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).Error().To(Not(BeNil()))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying MCPServerRegistrations tools are not present")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer.Spec.Prefix, toolsList)).To(BeFalseBecause("%s should NOT exist", registeredServer.Spec.Prefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("updating the secret to a valid value the server should be registered and the tools should exist")
		patch := client.MergeFrom(cred.DeepCopy())
		cred.StringData = map[string]string{
			"token": "Bearer test-api-key-secret-token",
		}
		Expect(k8sClient.Patch(ctx, cred, patch)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).Error().To(BeNil())
		}, TestTimeoutConfigSync, TestRetryInterval).To(Succeed())

		By("Verifying MCPServerRegistrations tools are present")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer.Spec.Prefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer.Spec.Prefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

	})

	It("[Happy] should use and re-use a backend MCP session", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)

		registration := NewMCPServerResourcesWithDefaults("sessions", k8sClient).WithPrefix("sess_").Build()
		// Important as we need to make sure to clean up
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("creating a new client")
		var mcpClient *mcp.ClientSession
		Eventually(func(g Gomega) {
			var err error
			mcpClient, err = NewMCPGatewayClient(context.Background(), gatewayURL)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		clientSession := mcpClient.ID()
		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
		By("Ensuring the gateway has the tools")
		Eventually(func(g Gomega) {
			toolsList, err := mcpClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer.Spec.Prefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer.Spec.Prefix))
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		toolName := fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "headers")
		By("Invoking a tool")
		var mcpsessionid string
		Eventually(func(g Gomega) {
			currentSessionID := ""
			res, err := mcpClient.CallTool(ctx, &mcp.CallToolParams{Name: toolName})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(res).NotTo(BeNil())
			for _, cont := range res.Content {
				textContent, ok := cont.(*mcp.TextContent)
				g.Expect(ok).To(BeTrue())
				if strings.HasPrefix(textContent.Text, "Mcp-Session-Id") {
					GinkgoWriter.Println(textContent.Text)
					currentSessionID = textContent.Text
				}
			}
			g.Expect(currentSessionID).To(ContainSubstring("Mcp-Session-Id"))
			mcpsessionid = currentSessionID
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Invoking the headers tool again")
		res, err := mcpClient.CallTool(ctx, &mcp.CallToolParams{Name: toolName})
		Expect(err).Error().NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		for _, cont := range res.Content {
			textContent, ok := cont.(*mcp.TextContent)
			Expect(ok).To(BeTrue())
			if strings.HasPrefix(textContent.Text, "Mcp-Session-Id") {
				Expect(textContent.Text).To(ContainSubstring("Mcp-Session-Id"))
				Expect(mcpsessionid).To(Equal(textContent.Text))
				// the session for the gateway should not be the same as the session for the MCP server
				Expect(mcpsessionid).NotTo(ContainSubstring(clientSession))
			}
		}

		By("deleting the session it should get a new backend session")
		Expect(mcpClient.Close()).Error().NotTo(HaveOccurred())
		// closing the client triggers a delete and cancelling of the context so we need a new client
		mcpClient, err = NewMCPGatewayClient(context.Background(), gatewayURL)
		Expect(err).Error().NotTo(HaveOccurred())
		By("invoking headers tool with new client")
		res, err = mcpClient.CallTool(ctx, &mcp.CallToolParams{Name: toolName})
		Expect(err).Error().NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		for _, cont := range res.Content {
			textContent, ok := cont.(*mcp.TextContent)
			Expect(ok).To(BeTrue())
			if strings.HasPrefix(textContent.Text, "Mcp-Session-Id") {
				GinkgoWriter.Println(textContent.Text)
				Expect(textContent.Text).To(ContainSubstring("Mcp-Session-Id"))
				Expect(mcpsessionid).To(Not(Equal(textContent.Text)))
				Expect(textContent.Text).To(Not(ContainSubstring(mcpClient.ID())))
			}
		}
	})

	It("[Happy] concurrent tool calls on a fresh session should create only one backend session", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		By("Registering an MCPServerRegistration")
		registration := NewMCPServerResourcesWithDefaults("concurrent-session", k8sClient).WithPrefix("conc_").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Ensuring the gateway has registered the server and tools are available")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
		WaitForToolsWithPrefix(ctx, mcpGatewayClient, registeredServer.Spec.Prefix)

		By("Initializing a fresh raw HTTP session (no backend session exists yet)")
		var sessionID string
		Eventually(func(g Gomega) {
			var initErr error
			sessionID, initErr = mcpInitialize(ctx, gatewayURL, nil)
			g.Expect(initErr).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

		const concurrency = 8
		type callResult struct {
			backendSession string
			err            error
		}
		results := make([]callResult, concurrency)
		toolName := fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "headers")

		By(fmt.Sprintf("Firing %d concurrent tool calls before any backend session exists", concurrency))
		var wg sync.WaitGroup
		for i := range concurrency {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				_, content, err := mcpCallTool(ctx, gatewayURL, sessionID, toolName, nil, nil)
				if err != nil {
					results[idx] = callResult{err: err}
					return
				}
				results[idx] = callResult{backendSession: extractBackendSession(content)}
			}(i)
		}
		wg.Wait()

		By("Asserting all calls succeeded and all used the same backend session")
		var firstSession string
		for i, r := range results {
			Expect(r.err).NotTo(HaveOccurred(), "concurrent call %d should succeed", i)
			Expect(r.backendSession).NotTo(BeEmpty(), "concurrent call %d should return a backend session ID", i)
			if firstSession == "" {
				firstSession = r.backendSession
			}
			Expect(r.backendSession).To(Equal(firstSession), "concurrent call %d should share the same backend session", i)
		}
		GinkgoWriter.Printf("all %d concurrent calls used backend session: %s\n", concurrency, firstSession)
	})

	It("[Happy] concurrent tool calls across different backends should each reach the correct upstream", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		By("Registering server1 (Go) and everything-server (TypeScript) with unique prefixes")
		reg1 := NewMCPServerResourcesWithDefaults("cross-backend-1", k8sClient).
			WithPrefix("xb1_").Build()
		testResources = append(testResources, reg1.GetObjects()...)
		server1 := reg1.Register(ctx)

		reg2 := NewMCPServerResources("cross-backend-2", "everything-server.mcp-gateway.local", "everything-server", 9090, k8sClient).
			WithPrefix("xb2_").Build()
		testResources = append(testResources, reg2.GetObjects()...)
		server2 := reg2.Register(ctx)

		By("Waiting for both servers to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(BeNil())
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server2.Name, server2.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		tool1 := fmt.Sprintf("%shello_world", server1.Spec.Prefix)
		tool2 := fmt.Sprintf("%secho", server2.Spec.Prefix)
		WaitForToolsWithPrefix(ctx, mcpGatewayClient, server1.Spec.Prefix)
		WaitForToolsWithPrefix(ctx, mcpGatewayClient, server2.Spec.Prefix)

		By("Initializing a fresh session with no backend sessions")
		var sessionID string
		Eventually(func(g Gomega) {
			var initErr error
			sessionID, initErr = mcpInitialize(ctx, gatewayURL, nil)
			g.Expect(initErr).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

		By("Firing concurrent tool calls to both backends simultaneously")
		const rounds = 5
		type callResult struct {
			status int
			body   string
			err    error
		}
		results1 := make([]callResult, rounds)
		results2 := make([]callResult, rounds)

		var wg sync.WaitGroup
		for i := range rounds {
			wg.Add(2)
			go func(idx int) {
				defer wg.Done()
				callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				status, content, err := mcpCallTool(callCtx, gatewayURL, sessionID, tool1, map[string]any{"name": fmt.Sprintf("r%d", idx)}, nil)
				if err != nil {
					results1[idx] = callResult{err: fmt.Errorf("tool  %s %w", tool1, err)}
					return
				}
				body := ""
				if len(content) > 0 {
					body = content[0].Text
				}
				results1[idx] = callResult{status: status, body: body}
			}(i)
			go func(idx int) {
				defer wg.Done()
				callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				status, content, err := mcpCallTool(callCtx, gatewayURL, sessionID, tool2, map[string]any{"message": fmt.Sprintf("r%d", idx)}, nil)
				if err != nil {
					results2[idx] = callResult{err: fmt.Errorf("tool  %s %w", tool2, err)}
					return
				}
				body := ""
				if len(content) > 0 {
					body = content[0].Text
				}
				results2[idx] = callResult{status: status, body: body}
			}(i)
		}
		wg.Wait()

		By("Asserting all calls to server1 (Go greet) succeeded")
		for i, r := range results1 {
			Expect(r.err).NotTo(HaveOccurred(), "server1 call %d should succeed", i)
			Expect(r.status).To(Equal(200), "server1 call %d status", i)
			Expect(r.body).To(ContainSubstring("Hello, r"), "server1 call %d body", i)
		}

		By("Asserting all calls to everything-server (TS echo) succeeded")
		for i, r := range results2 {
			Expect(r.err).NotTo(HaveOccurred(), "everything-server call %d should succeed", i)
			Expect(r.status).To(Equal(200), "everything-server call %d status", i)
			Expect(r.body).To(ContainSubstring("Echo: r"), "everything-server call %d body", i)
		}
	})

	It("[Full] Redis session cache persists backend sessions across pod restarts", Serial, func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		deploymentName := "mcp-gateway"
		redisSecretName := "redis-session-store"
		redisConnectionString := fmt.Sprintf("redis://redis.%s.svc.cluster.local:6379", SystemNamespace)

		By("Creating Redis session store secret")
		redisSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      redisSecretName,
				Namespace: SystemNamespace,
				Labels: map[string]string{
					"mcp.kuadrant.io/secret": "true",
					"e2e":                    "test",
				},
			},
			Type: corev1.SecretTypeOpaque,
			StringData: map[string]string{
				"CACHE_CONNECTION_STRING": redisConnectionString,
			},
		}
		Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, redisSecret))).To(Succeed())

		DeferCleanup(func() {
			By("Cleanup: removing sessionStore from MCPGatewayExtension")
			Eventually(func(g Gomega) {
				ext := &mcpv1.MCPGatewayExtension{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: MCPExtensionName, Namespace: SystemNamespace}, ext)).To(Succeed())
				ext.Spec.SessionStore = nil
				g.Expect(k8sClient.Update(ctx, ext)).To(Succeed())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
			Expect(WaitForDeploymentReady(ctx, SystemNamespace, deploymentName)).To(Succeed())
			Expect(k8sClient.Delete(ctx, redisSecret)).To(Succeed())
		})

		By("Registering an MCP server")
		registration := NewMCPServerResourcesWithDefaults("redis-session", k8sClient).WithPrefix("redis_").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Ensuring the gateway has the tools")
		WaitForToolsWithPrefix(ctx, mcpGatewayClient, registeredServer.Spec.Prefix)

		gen, err := GetDeploymentGeneration(ctx, SystemNamespace, deploymentName)
		Expect(err).NotTo(HaveOccurred())

		By("Enabling Redis session cache via sessionStore on MCPGatewayExtension")
		Eventually(func(g Gomega) {
			ext := &mcpv1.MCPGatewayExtension{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: MCPExtensionName, Namespace: SystemNamespace}, ext)).To(Succeed())
			ext.Spec.SessionStore = &mcpv1.SessionStore{SecretName: redisSecretName}
			g.Expect(k8sClient.Update(ctx, ext)).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Waiting for gateway rollout after enabling Redis")
		Expect(WaitForDeploymentReplicas(ctx, SystemNamespace, deploymentName, 1, gen)).To(Succeed())

		By("Initializing a raw HTTP session with the gateway")
		var sessionID string
		Eventually(func(g Gomega) {
			var initErr error
			sessionID, initErr = mcpInitialize(ctx, gatewayURL, nil)
			g.Expect(initErr).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		GinkgoWriter.Println("client session ID:", sessionID)

		Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

		By("Calling headers tool to establish a backend session")
		toolName := fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "headers")
		var backendSessionID string
		Eventually(func(g Gomega) {
			_, content, callErr := mcpCallTool(ctx, gatewayURL, sessionID, toolName, nil, nil)
			g.Expect(callErr).NotTo(HaveOccurred())
			backendSessionID = extractBackendSession(content)
			g.Expect(backendSessionID).NotTo(BeEmpty(), "expected backend Mcp-Session-Id in tool response")
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		GinkgoWriter.Println("backend session before restart:", backendSessionID)

		By("Restarting the mcp-gateway deployment and waiting for rollout")
		Expect(RestartDeploymentAndWait(ctx, SystemNamespace, deploymentName)).To(Succeed())

		By("Calling headers tool with same session ID to verify Redis restored the backend session")
		var restoredSessionID string
		Eventually(func(g Gomega) {
			_, content, callErr := mcpCallTool(ctx, gatewayURL, sessionID, toolName, nil, nil)
			g.Expect(callErr).NotTo(HaveOccurred())
			restoredSessionID = extractBackendSession(content)
			g.Expect(restoredSessionID).NotTo(BeEmpty(), "expected backend Mcp-Session-Id in tool response")
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		GinkgoWriter.Println("backend session after restart:", restoredSessionID)
		Expect(restoredSessionID).To(Equal(backendSessionID),
			"backend session should be restored from Redis after pod restart")
	})

	It("[Happy] should assign unique mcp-session-ids to concurrent clients and new session on reconnect", func() {
		By("Creating multiple clients concurrently")
		var client1, client2, client3 *mcp.ClientSession
		Eventually(func(g Gomega) {
			var err error
			client1, err = NewMCPGatewayClient(ctx, gatewayURL)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer func() { _ = client1.Close() }()

		Eventually(func(g Gomega) {
			var err error
			client2, err = NewMCPGatewayClient(ctx, gatewayURL)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer func() { _ = client2.Close() }()

		Eventually(func(g Gomega) {
			var err error
			client3, err = NewMCPGatewayClient(ctx, gatewayURL)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer func() { _ = client3.Close() }()

		By("Verifying all clients have unique session IDs")
		session1 := client1.ID()
		session2 := client2.ID()
		session3 := client3.ID()

		Expect(session1).NotTo(BeEmpty(), "client1 should have a session ID")
		Expect(session2).NotTo(BeEmpty(), "client2 should have a session ID")
		Expect(session3).NotTo(BeEmpty(), "client3 should have a session ID")

		Expect(session1).NotTo(Equal(session2), "client1 and client2 should have different session IDs")
		Expect(session1).NotTo(Equal(session3), "client1 and client3 should have different session IDs")
		Expect(session2).NotTo(Equal(session3), "client2 and client3 should have different session IDs")

		By("Disconnecting client1 and reconnecting")
		Expect(client1.Close()).To(Succeed())

		var reconnectedClient *mcp.ClientSession
		Eventually(func(g Gomega) {
			var err error
			reconnectedClient, err = NewMCPGatewayClient(ctx, gatewayURL)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer func() { _ = reconnectedClient.Close() }()

		newSession := reconnectedClient.ID()
		Expect(newSession).NotTo(BeEmpty(), "reconnected client should have a session ID")
		Expect(newSession).NotTo(Equal(session1), "reconnected client should have a different session ID than before")
		Expect(newSession).NotTo(Equal(session2), "reconnected client should have a different session ID than client2")
		Expect(newSession).NotTo(Equal(session3), "reconnected client should have a different session ID than client3")
	})

	It("[Happy] should only return tools specified by MCPVirtualServer when using X-Mcp-Virtualserver header", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		By("Creating an MCPServerRegistration with tools")
		registration := NewMCPServerResourcesWithDefaults("virtualserver-test", k8sClient).WithPrefix("vst_").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying MCPServerRegistration tools are present")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer.Spec.Prefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer.Spec.Prefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating an MCPVirtualServer with a subset of tools")
		allowedTool := fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "hello_world")
		virtualServer := BuildTestMCPVirtualServer("test-virtualserver", TestServerNameSpace, []string{allowedTool}).Build()
		testResources = append(testResources, virtualServer)
		Expect(k8sClient.Create(ctx, virtualServer)).To(Succeed())

		By("Creating a client with X-Mcp-Virtualserver header")
		virtualServerHeader := fmt.Sprintf("%s/%s", virtualServer.Namespace, virtualServer.Name)
		virtualServerClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"X-Mcp-Virtualserver": virtualServerHeader,
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = virtualServerClient.Close() }()

		By("Verifying only the tools from MCPVirtualServer are returned")
		Eventually(func(g Gomega) {
			filteredTools, err := virtualServerClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(filteredTools).NotTo(BeNil())
			g.Expect(len(filteredTools.Tools)).To(Equal(1), "expected exactly 1 tool from virtual server")
			g.Expect(filteredTools.Tools[0].Name).To(Equal(allowedTool))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying the original client without header still sees all tools")
		allToolsAgain, err := mcpGatewayClient.ListTools(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(allToolsAgain.Tools)).To(BeNumerically(">", 1), "expected more than 1 tool without virtual server header")
	})

	It("[Happy] should send list_changed notifications to connected clients when a server with tools and prompts is registered", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		// NOTE on notifications. A notification is sent when servers are removed during clean up as this effects tools list also.
		// as the list_changed notification is broadcast, this can mean clients in other tests receive additional notifications
		// for that reason we only assert we received at least one rather than a set number
		By("Creating clients with notification handlers and different sessions")
		client1Notification := make(chan struct{}, 1)
		client1, err := NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(method string) {
			if strings.Contains(method, "list_changed") {
				GinkgoWriter.Println("client 1 received notification registration", method)
				select {
				case client1Notification <- struct{}{}:
				default:
				}
			}
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client1.Close() }()

		client2Notification := make(chan struct{}, 1)
		client2, err := NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(method string) {
			if strings.Contains(method, "list_changed") {
				GinkgoWriter.Println("client 2 received notification registration", method)
				select {
				case client2Notification <- struct{}{}:
				default:
				}
			}
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client2.Close() }()
		Expect(client1.sessionID).NotTo(BeEmpty())
		Expect(client2.sessionID).NotTo(BeEmpty())
		Expect(client1.sessionID).NotTo(Equal(client2.sessionID))

		By("registering a new MCPServerRegistration pointing to server1 which has both tools and prompts")
		registration := NewMCPServerResourcesWithDefaults("notification-test", k8sClient).
			WithBackendTarget(sharedMCPTestServer1, 9090).WithPrefix("notif_").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Waiting for the server to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		// We do this to wait for the tools and prompts to show up as we know then that the gateway has done its work
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer.Spec.Prefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer.Spec.Prefix))
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())
		WaitForPromptsWithPrefix(ctx, mcpGatewayClient, registeredServer.Spec.Prefix)

		By("Verifying both clients received list_changed notifications within 1 minutes")
		Eventually(func(g Gomega) {
			_, err := client1.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())
		Eventually(client1Notification).WithTimeout(TestTimeoutMedium).Should(Receive(), "client1 should have received a notification")
		Eventually(client2Notification).WithTimeout(TestTimeoutMedium).Should(Receive(), "client2 should have received a notification")
	})

	It("[Happy] should forward notifications/tools/list_changed from backend MCP server to connected clients", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		By("Creating an MCPServerRegistration pointing to server1 which has the add_tool feature")
		registration := NewMCPServerResourcesWithDefaults("backend-notification-test", k8sClient).
			WithBackendTarget(sharedMCPTestServer1, 9090).WithPrefix("bknotif_").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying initial tools are present")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer.Spec.Prefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer.Spec.Prefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating new clients with notification handlers")
		client1Notification := make(chan struct{}, 1)
		client1, err := NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(method string) {
			if method == "notifications/tools/list_changed" {
				GinkgoWriter.Println("client 1 received notification", method)
				select {
				case client1Notification <- struct{}{}:
				default:
				}
			}
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client1.Close() }()

		client2Notification := make(chan struct{}, 1)
		client2, err := NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(method string) {
			GinkgoWriter.Println("client 2 received notification", method)
			if method == "notifications/tools/list_changed" {
				select {
				case client2Notification <- struct{}{}:
				default:
				}
			}
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = client2.Close() }()

		By("Calling add_tool on the backend server to trigger notifications/tools/list_changed")
		dynamicToolName := fmt.Sprintf("dynamic_tool_%s", UniqueName(""))
		addToolName := fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "add_tool")
		var res *mcp.CallToolResult
		Eventually(func(g Gomega) {
			var err error
			res, err = client1.CallTool(ctx, &mcp.CallToolParams{
				Name: addToolName,
				Arguments: map[string]string{
					"name":        dynamicToolName,
					"description": "A dynamically added tool for testing notifications",
				},
			})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(res).NotTo(BeNil())
			for _, c := range res.Content {
				if tc, ok := c.(*mcp.TextContent); ok {
					g.Expect(tc.Text).NotTo(ContainSubstring("Tool not found"))
				}
			}
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		GinkgoWriter.Println("add_tool response:", res.Content)

		By("Verifying both clients received notifications/tools/list_changed")
		Eventually(client1Notification).WithTimeout(TestTimeoutMedium).Should(Receive(), "client1 should have received notifications/tools/list_changed")
		Eventually(client2Notification).WithTimeout(TestTimeoutMedium).Should(Receive(), "client2 should have received notifications/tools/list_changed")

		By("Verifying tools/list now includes the new dynamically added tool")
		Eventually(func(g Gomega) {
			toolsList, err := client1.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())

			foundNewTool := false
			for _, t := range toolsList.Tools {
				if strings.HasSuffix(t.Name, dynamicToolName) {
					foundNewTool = true
					break
				}
			}
			g.Expect(foundNewTool).To(BeTrueBecause("the dynamically added tool %s should be in the tools list", dynamicToolName))
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())
	})

	// Note this is a complex test as it scales up and down the server. It can take quite a while to run.
	// consider moving to separate suite
	It("[Full] should gracefully handle an MCP Server becoming unavailable", Serial, func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()
		_ = ScaleDeployment(ctx, TestServerNameSpace, scaledMCPTestServer, 1)

		By("Scaling down the MCP server3 deployment to 0")
		Expect(ScaleDeployment(ctx, TestServerNameSpace, scaledMCPTestServer, 0)).To(Succeed())

		By("Registering an MCPServerRegistration pointing to server3")
		registration := NewMCPServerResourcesWithDefaults("unavailable-test", k8sClient).
			WithPrefix("unavailable_").
			WithBackendTarget(scaledMCPTestServer, 9090).Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration is Ready (controller wrote config)")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying tools are NOT present (backend is down, broker cannot connect)")
		Consistently(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer.Spec.Prefix, toolsList)).To(BeFalseBecause("%s should not exist when backend is down", registeredServer.Spec.Prefix))
		}, "5s", TestRetryInterval).To(Succeed())

		By("Scaling up the MCP server3 deployment to 1")
		Expect(ScaleDeployment(ctx, TestServerNameSpace, scaledMCPTestServer, 1)).To(Succeed())

		By("Waiting for deployment to be ready")
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, TestServerNameSpace, scaledMCPTestServer)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying tools are now present")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer.Spec.Prefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer.Spec.Prefix))
		}, TestTimeoutConfigSync, TestRetryInterval).To(Succeed())

		By("Creating a client with notification handler")
		receivedNotification := make(chan struct{}, 1)
		notifyClient, err := NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(method string) {
			if method == "notifications/tools/list_changed" {
				GinkgoWriter.Println("received notification during unavailability test", method)
				select {
				case receivedNotification <- struct{}{}:
				default:
				}
			}
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = notifyClient.Close() }()

		By("Scaling back down the MCP server3 deployment to 0")
		Expect(ScaleDeployment(ctx, TestServerNameSpace, scaledMCPTestServer, 0)).To(Succeed())

		By("Verifying tools are removed from tools/list within timeout")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer.Spec.Prefix, toolsList)).To(BeFalseBecause("%s should be removed when server unavailable", registeredServer.Spec.Prefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying client notification was received")
		Eventually(receivedNotification).WithTimeout(TestTimeoutMedium).Should(Receive(), "should have received notifications/tools/list_changed")

		By("Verifying tool call fails when server unavailable")
		toolName := fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "time")
		Eventually(func(g Gomega) {
			res, callErr := mcpGatewayClient.CallTool(ctx, &mcp.CallToolParams{Name: toolName})
			if callErr != nil {
				return // transport error
			}
			g.Expect(res).NotTo(BeNil())
			g.Expect(res.IsError).To(BeTrue(), "tool call should fail when backend is down")
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Scaling the MCP server deployment back up")
		Expect(ScaleDeployment(ctx, TestServerNameSpace, scaledMCPTestServer, 1)).To(Succeed())

		By("Waiting for deployment to be ready")
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, TestServerNameSpace, scaledMCPTestServer)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying tools are restored in tools/list")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer.Spec.Prefix, toolsList)).To(BeTrueBecause("%s should be restored when server available", registeredServer.Spec.Prefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying tool call work when server back available")
		toolName = fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "time")
		_, err = mcpGatewayClient.CallTool(ctx, &mcp.CallToolParams{Name: toolName})
		Expect(err).NotTo(HaveOccurred(), "tool calls should work once the server is back and ready")
	})

	It("[Happy] should filter tools based on x-mcp-authorized JWT header", Serial, func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		SetupTrustedHeadersAuth(ctx, k8sClient)

		By("Creating an MCPServerRegistration with tools")
		registration := NewMCPServerResourcesWithDefaults("authorized-capabilities-test", k8sClient).WithPrefix("authcap_").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying MCPServerRegistration tools are present without filtering")
		var allTools *mcp.ListToolsResult
		Eventually(func(g Gomega) {
			var err error
			allTools, err = mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(allTools).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer.Spec.Prefix, allTools)).To(BeTrueBecause("%s should exist", registeredServer.Spec.Prefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating a JWT with allowed tools for the server")

		allowedTools := map[string][]string{
			fmt.Sprintf("%s/%s", registeredServer.Namespace, registeredServer.Name): {"hello_world"},
		}
		jwtToken, err := CreateAuthorizedCapabilitiesJWT(allowedTools)
		Expect(err).NotTo(HaveOccurred())

		By("Creating a client with x-mcp-authorized header")
		authorizedClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"X-Mcp-Authorized": jwtToken,
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = authorizedClient.Close() }()

		By("Verifying only the tools from the JWT are returned")
		Eventually(func(g Gomega) {
			filteredTools, err := authorizedClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(filteredTools).NotTo(BeNil())
			g.Expect(len(filteredTools.Tools)).To(Equal(1), "expected exactly 1 tool from authorized capabilities header")
			expectedToolName := fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "hello_world")
			g.Expect(filteredTools.Tools[0].Name).To(Equal(expectedToolName))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	It("[Happy] should register MCP server via Hostname backendRef", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)

		// verifies controller correctly handles Hostname backendRef
		// broker connectivity not tested - test server doesn't speak HTTPS
		externalHost := "mcp-test-server2.mcp-test.svc.cluster.local"
		port := int32(9090)

		By("Creating ServiceEntry, DestinationRule, HTTPRoute with Hostname backendRef, and MCPServerRegistration")
		registration := NewExternalMCPServerResources("hostname-backend", k8sClient, externalHost, port).Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying controller processed MCPServerRegistration (has status condition)")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationHasCondition(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())
	})

	It("[Full] should become Ready even with invalid protocol version", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)

		// the controller writes config and verifies the route is programmed.
		// protocol validation is a runtime concern handled by the broker, not
		// the controller. a server with an unsupported protocol version will
		// still be Ready in the CRD. protocol errors surface through the
		// broker's /status endpoint.
		By("Creating an MCPServerRegistration pointing to the broken server with wrong protocol version")
		registration := NewMCPServerResourcesWithDefaults("protocol-status-test", k8sClient).
			WithBackendTarget("mcp-test-broken-server", 9090).WithPrefix("broken_").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration becomes Ready (controller wrote config, route programmed)")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient,
				registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	It("[Happy] should become Ready even when tool conflicts exist from same prefix", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)

		// tool and prompt conflicts are detected by the broker at runtime, not
		// by the controller. both registrations will be Ready in the CRD.
		// conflicts surface through the broker's /status endpoint.
		By("Creating first MCPServerRegistration with a specific prefix pointing to server1")
		registration1 := NewMCPServerResources("conflict-test-1", "conflict-s1.mcp-gateway.local", sharedMCPTestServer1, 9090, k8sClient).
			WithPrefix("conflict_").Build()
		testResources = append(testResources, registration1.GetObjects()...)
		server1 := registration1.Register(ctx)

		By("Ensuring first server becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating second MCPServerRegistration with the SAME prefix pointing to server1 via a different hostname")
		registration2 := NewMCPServerResources("conflict-test-2", "conflict-s2.mcp-gateway.local", sharedMCPTestServer1, 9090, k8sClient).
			WithPrefix("conflict_").Build()
		testResources = append(testResources, registration2.GetObjects()...)
		server2 := registration2.Register(ctx)

		By("Verifying both MCPServerRegistrations become Ready (conflicts are broker-side)")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(BeNil())
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server2.Name, server2.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	It("[Full] should allow multiple MCP Servers without prefixes", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		By("Creating HTTPRoutes and MCP Servers")
		// create httproutes for test servers that should already be deployed
		registration := NewMCPServerResources("same-prefix", "everything-server.mcp-gateway.local", "everything-server", 9090, k8sClient).
			WithPrefix("").Build()
		// Important as we need to make sure to clean up
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer1 := registration.Register(ctx)

		// This server has a 'hello_world' tool
		registration = NewMCPServerResources("same-prefix", "e2e-server2.mcp-gateway.local", "mcp-test-server2", 9090, k8sClient).
			WithPrefix("").Build()
		// Important as we need to make sure to clean up
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer2 := registration.Register(ctx)

		By("Verifying MCPServerRegistrations become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer1.Name, registeredServer1.Namespace)).To(BeNil())
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer2.Name, registeredServer2.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying expected tools are present")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolPresent("echo", toolsList)).To(BeTrueBecause("%q should exist", "greet"))
			g.Expect(verifyMCPServerRegistrationToolPresent("hello_world", toolsList)).To(BeTrueBecause("%q should exist", "hello_world"))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		toolName := "hello_world"
		By("Invoking the first tool")
		res, err := mcpGatewayClient.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: map[string]string{
			"name": "e2e",
		}})
		Expect(err).Error().NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(len(res.Content)).To(BeNumerically("==", 1))
		content, ok := res.Content[0].(*mcp.TextContent)
		Expect(ok).To(BeTrue())
		Expect(content.Text).To(Equal("Hello, e2e!"))

		toolName = "echo"
		By("Invoking the second tool")
		res, err = mcpGatewayClient.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: map[string]string{
			"message": "e2e",
		}})
		Expect(err).Error().NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(len(res.Content)).To(BeNumerically("==", 1))
		content, ok = res.Content[0].(*mcp.TextContent)
		Expect(ok).To(BeTrue())
		Expect(content.Text).To(Equal("Echo: e2e"))
	})

	It("[Happy] should list prompts with prefix, invoke via GetPrompt, and remove on unregistration", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		By("Creating MCPServerRegistration pointing to server1 which has a 'greet' prompt")
		registration := NewMCPServerResourcesWithDefaults("prompt-list", k8sClient).
			WithBackendTarget(sharedMCPTestServer1, 9090).WithPrefix("prompttest_").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying prompts are present with prefix")
		expectedPrompt := fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "greet")
		Eventually(func(g Gomega) {
			promptsList, err := mcpGatewayClient.ListPrompts(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(promptsList).NotTo(BeNil())
			g.Expect(PromptsListHasPrompt(promptsList, expectedPrompt)).To(BeTrueBecause("prompt %q should exist", expectedPrompt))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Invoking the prompt via GetPrompt")
		promptResult, err := mcpGatewayClient.GetPrompt(ctx, &mcp.GetPromptParams{
			Name:      expectedPrompt,
			Arguments: map[string]string{"name": "e2e"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(promptResult).NotTo(BeNil())
		Expect(len(promptResult.Messages)).To(BeNumerically(">=", 1), "prompt should return at least one message")
		content, ok := promptResult.Messages[0].Content.(*mcp.TextContent)
		Expect(ok).To(BeTrue(), "prompt message content should be TextContent")
		Expect(content.Text).To(Equal("Say hi to e2e"))

		By("Unregistering the MCPServerRegistration")
		Expect(k8sClient.Delete(ctx, registeredServer)).To(Succeed())

		By("Verifying prompts are removed after unregistration")
		Eventually(func(g Gomega) {
			promptsList, err := mcpGatewayClient.ListPrompts(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(promptsList).NotTo(BeNil())
			g.Expect(PromptsListHasPrefix(promptsList, registeredServer.Spec.Prefix)).To(BeFalseBecause("prompts with prefix %q should be removed", registeredServer.Spec.Prefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	It("[Happy] should aggregate prompts from multiple servers with different prefixes", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		By("Creating two MCPServerRegistrations for server1 with different prefixes")
		registration1 := NewMCPServerResources("prompt-multi-1", "prompt-s1a.mcp-gateway.local", sharedMCPTestServer1, 9090, k8sClient).
			WithPrefix("s1a_").Build()
		testResources = append(testResources, registration1.GetObjects()...)
		server1 := registration1.Register(ctx)

		registration2 := NewMCPServerResources("prompt-multi-2", "prompt-s1b.mcp-gateway.local", sharedMCPTestServer1, 9090, k8sClient).
			WithPrefix("s1b_").Build()
		testResources = append(testResources, registration2.GetObjects()...)
		server2 := registration2.Register(ctx)

		By("Ensuring both servers become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(BeNil())
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server2.Name, server2.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying prompts from both servers are present with their respective prefixes")
		Eventually(func(g Gomega) {
			promptsList, err := mcpGatewayClient.ListPrompts(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(promptsList).NotTo(BeNil())
			g.Expect(PromptsListHasPrompt(promptsList, "s1a_greet")).To(BeTrueBecause("s1a_greet should exist"))
			g.Expect(PromptsListHasPrompt(promptsList, "s1b_greet")).To(BeTrueBecause("s1b_greet should exist"))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	It("[Happy] should filter prompts via MCPVirtualServer", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		By("Creating MCPServerRegistration for server1 with prompts")
		registration := NewMCPServerResourcesWithDefaults("prompt-vs-filter", k8sClient).
			WithBackendTarget(sharedMCPTestServer1, 9090).WithPrefix("vs_").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying prompt is available")
		expectedPrompt := fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "greet")
		WaitForPromptsWithPrefix(ctx, mcpGatewayClient, registeredServer.Spec.Prefix)

		By("Creating an MCPVirtualServer that allows one tool and the greet prompt")
		allowedTool := fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "greet")
		virtualServer := NewMCPVirtualServerBuilder("prompt-filter-vs", TestServerNameSpace).
			WithTools([]string{allowedTool}).
			WithPrompts([]string{expectedPrompt}).Build()
		testResources = append(testResources, virtualServer)
		Expect(k8sClient.Create(ctx, virtualServer)).To(Succeed())

		By("Creating a client with X-Mcp-Virtualserver header")
		virtualServerHeader := fmt.Sprintf("%s/%s", virtualServer.Namespace, virtualServer.Name)
		virtualServerClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"X-Mcp-Virtualserver": virtualServerHeader,
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = virtualServerClient.Close() }()

		By("Verifying only the allowed prompt is returned via virtual server")
		Eventually(func(g Gomega) {
			filteredPrompts, err := virtualServerClient.ListPrompts(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(filteredPrompts).NotTo(BeNil())
			g.Expect(len(filteredPrompts.Prompts)).To(Equal(1), "expected exactly 1 prompt from virtual server")
			g.Expect(filteredPrompts.Prompts[0].Name).To(Equal(expectedPrompt))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying the original client without header still sees all prompts")
		promptsAll, err := mcpGatewayClient.ListPrompts(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(PromptsListHasPrompt(promptsAll, expectedPrompt)).To(BeTrue())
	})

	It("[Happy] should filter prompts based on x-mcp-authorized JWT header", Serial, func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		SetupTrustedHeadersAuth(ctx, k8sClient)

		// server1 exposes a single prompt (greet), so register it twice with
		// different prefixes; allowing only one registration's prompt in the
		// JWT proves the other is filtered out rather than the filter being a no-op
		By("Creating two MCPServerRegistrations for server1 with different prefixes")
		registrationA := NewMCPServerResourcesWithDefaults("authz-prompt-a", k8sClient).
			WithBackendTarget(sharedMCPTestServer1, 9090).WithPrefix("pauthza_").Build()
		testResources = append(testResources, registrationA.GetObjects()...)
		registeredServerA := registrationA.Register(ctx)

		registrationB := NewMCPServerResourcesWithDefaults("authz-prompt-b", k8sClient).
			WithBackendTarget(sharedMCPTestServer1, 9090).WithPrefix("pauthzb_").Build()
		testResources = append(testResources, registrationB.GetObjects()...)
		registeredServerB := registrationB.Register(ctx)

		By("Ensuring the gateway has registered both servers")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServerA.Name, registeredServerA.Namespace)).To(BeNil())
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServerB.Name, registeredServerB.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying both prompts are present without filtering")
		WaitForPromptsWithPrefix(ctx, mcpGatewayClient, registeredServerA.Spec.Prefix)
		WaitForPromptsWithPrefix(ctx, mcpGatewayClient, registeredServerB.Spec.Prefix)

		By("Creating a JWT allowing only the greet prompt from the first server")
		allowedPrompts := map[string][]string{
			fmt.Sprintf("%s/%s", registeredServerA.Namespace, registeredServerA.Name): {"greet"},
		}
		jwtToken, err := CreateAuthorizedPromptsJWT(allowedPrompts)
		Expect(err).NotTo(HaveOccurred())

		By("Creating a client with x-mcp-authorized header")
		authorizedClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"X-Mcp-Authorized": jwtToken,
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = authorizedClient.Close() }()

		By("Verifying only the allowed prompt is returned")
		expectedPrompt := fmt.Sprintf("%s%s", registeredServerA.Spec.Prefix, "greet")
		Eventually(func(g Gomega) {
			filteredPrompts, err := authorizedClient.ListPrompts(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(filteredPrompts).NotTo(BeNil())
			g.Expect(len(filteredPrompts.Prompts)).To(Equal(1), "expected exactly 1 prompt from authorized capabilities header")
			g.Expect(filteredPrompts.Prompts[0].Name).To(Equal(expectedPrompt))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying a client without the header still sees both prompts")
		promptsAll, err := mcpGatewayClient.ListPrompts(ctx, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(PromptsListHasPrompt(promptsAll, expectedPrompt)).To(BeTrue())
		Expect(PromptsListHasPrompt(promptsAll, fmt.Sprintf("%s%s", registeredServerB.Spec.Prefix, "greet"))).To(BeTrue())
	})

	It("[Happy] should expose all prompts when MCPVirtualServer omits prompts field", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		By("Creating MCPServerRegistration for server1 with prompts")
		registration := NewMCPServerResourcesWithDefaults("prompt-vs-nofilter", k8sClient).
			WithBackendTarget(sharedMCPTestServer1, 9090).WithPrefix("nofilt_").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying prompts are available")
		WaitForPromptsWithPrefix(ctx, mcpGatewayClient, registeredServer.Spec.Prefix)

		By("Creating an MCPVirtualServer with tools only (no prompts field)")
		allowedTool := fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "greet")
		virtualServer := NewMCPVirtualServerBuilder("prompt-nofilter-vs", TestServerNameSpace).
			WithTools([]string{allowedTool}).Build()
		testResources = append(testResources, virtualServer)
		Expect(k8sClient.Create(ctx, virtualServer)).To(Succeed())

		By("Creating a client with X-Mcp-Virtualserver header")
		virtualServerHeader := fmt.Sprintf("%s/%s", virtualServer.Namespace, virtualServer.Name)
		virtualServerClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"X-Mcp-Virtualserver": virtualServerHeader,
		})
		Expect(err).NotTo(HaveOccurred())
		defer func() { _ = virtualServerClient.Close() }()

		By("Verifying all prompts are returned (no prompts field = no filtering)")
		Eventually(func(g Gomega) {
			filteredPrompts, err := virtualServerClient.ListPrompts(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(filteredPrompts).NotTo(BeNil())
			expectedPrompt := fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "greet")
			g.Expect(PromptsListHasPrompt(filteredPrompts, expectedPrompt)).To(BeTrueBecause("all prompts should be exposed when prompts field is omitted"))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying tools ARE filtered by the VirtualServer")
		Eventually(func(g Gomega) {
			filteredTools, err := virtualServerClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(filteredTools).NotTo(BeNil())
			g.Expect(len(filteredTools.Tools)).To(Equal(1), "expected exactly 1 tool from virtual server")
			g.Expect(filteredTools.Tools[0].Name).To(Equal(allowedTool))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	It("[Happy] should return error for prompts/get with nonexistent prompt", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)

		By("Creating MCPServerRegistration for server1")
		registration := NewMCPServerResourcesWithDefaults("prompt-notfound", k8sClient).
			WithBackendTarget(sharedMCPTestServer1, 9090).WithPrefix("notfound_").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Initialising a raw HTTP session for JSON-RPC error inspection")
		sessionID, err := mcpInitialize(ctx, gatewayURL, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

		By("Calling prompts/get with a nonexistent prompt name")
		status, body, err := mcpGetPrompt(ctx, gatewayURL, sessionID, "nonexistent_prompt_that_does_not_exist", nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusOK))

		rpcErr, parseErr := parseSSEError(body)
		Expect(parseErr).NotTo(HaveOccurred(), "expected a JSON-RPC error in SSE response")
		Expect(rpcErr.Code).To(Equal(-32602), "expected JSON-RPC invalid-params error code (-32602)")
	})

	It("[Happy] should resolve prefix conflicts by modifying MCPServer to add prefix", func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		// server1 has: greet, time, slow, headers, add_tool
		// server2 has: hello_world, time, headers, auth1234, slow, set_time, pour_chocolate_into_mold
		// Both have time, headers, slow - these will conflict

		By("Creating first MCPServer without prefix pointing to server1")
		registration1 := NewMCPServerResources("prefix-conflict-1", "conflict-server1.mcp-gateway.local", sharedMCPTestServer1, 9090, k8sClient).
			WithPrefix("").Build()
		testResources = append(testResources, registration1.GetObjects()...)
		server1 := registration1.Register(ctx)

		By("Waiting for first server to become ready before creating second server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating second MCPServer without prefix pointing to server2 (also has time, headers, slow)")
		registration2 := NewMCPServerResources("prefix-conflict-2", "conflict-server2.mcp-gateway.local", sharedMCPTestServer2, 9090, k8sClient).
			WithPrefix("").Build()
		testResources = append(testResources, registration2.GetObjects()...)
		server2 := registration2.Register(ctx)

		By("Verifying second server becomes Ready (conflicts are broker-side, not in CRD status)")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server2.Name, server2.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Modifying second MCPServer to add a prefix to resolve conflict")
		patch := client.MergeFrom(server2.DeepCopy())
		server2.Spec.Prefix = "server2_"
		Expect(k8sClient.Patch(ctx, server2, patch)).To(Succeed())

		By("Waiting for second server to become ready with new prefix")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server2.Name, server2.Namespace)).To(BeNil())
		}, TestTimeoutConfigSync, TestRetryInterval).To(Succeed())

		By("Verifying both servers' tools are now available")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			// greet from server1 (no prefix) - unique to server1
			g.Expect(verifyMCPServerRegistrationToolPresent("greet", toolsList)).To(BeTrueBecause("greet from server1 should exist"))
			// time from server1 (no prefix)
			g.Expect(verifyMCPServerRegistrationToolPresent("time", toolsList)).To(BeTrueBecause("time from server1 should exist"))
			// server2_hello_world from server2 (with prefix) - unique to server2
			g.Expect(verifyMCPServerRegistrationToolPresent("server2_hello_world", toolsList)).To(BeTrueBecause("server2_hello_world from server2 should exist"))
			// server2_time from server2 (with prefix)
			g.Expect(verifyMCPServerRegistrationToolPresent("server2_time", toolsList)).To(BeTrueBecause("server2_time from server2 should exist"))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Invoking same tools from both servers")
		// Call server1's time (no prefix)
		res, err := mcpGatewayClient.CallTool(ctx, &mcp.CallToolParams{Name: "time"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(len(res.Content)).To(BeNumerically(">=", 1))

		// Call server2's prefixed headers tool
		res, err = mcpGatewayClient.CallTool(ctx, &mcp.CallToolParams{Name: "server2_time"})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(len(res.Content)).To(BeNumerically(">=", 1))
	})

	It("[Happy] should disable and re-enable an MCPServerRegistration", Serial, func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		By("Creating an MCPServerRegistration with default state (Enabled)")
		registration := NewMCPServerResourcesWithDefaults("disable-enable-test", k8sClient).WithPrefix("distest_").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready and tools are present")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer.Spec.Prefix, toolsList)).To(BeTrueBecause("%s tools should exist", registeredServer.Spec.Prefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Disabling the MCPServerRegistration by patching spec.state to Disabled")
		patch := client.MergeFrom(registeredServer.DeepCopy())
		registeredServer.Spec.State = mcpv1.ServerStateDisabled
		Expect(k8sClient.Patch(ctx, registeredServer, patch)).To(Succeed())

		By("Verifying tools are removed from the gateway's tool list")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer.Spec.Prefix, toolsList)).To(BeFalseBecause("%s tools should be removed when disabled", registeredServer.Spec.Prefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying MCPServerRegistration status is Ready=False with reason Disabled")
		Eventually(func(g Gomega) {
			err := VerifyMCPServerRegistrationNotReadyWithReason(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace, "server is disabled")
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Re-enabling the MCPServerRegistration by patching spec.state back to Enabled")
		patch = client.MergeFrom(registeredServer.DeepCopy())
		registeredServer.Spec.State = mcpv1.ServerStateEnabled
		Expect(k8sClient.Patch(ctx, registeredServer, patch)).To(Succeed())

		By("Verifying tools return to the gateway's tool list")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent(registeredServer.Spec.Prefix, toolsList)).To(BeTrueBecause("%s tools should return after re-enabling", registeredServer.Spec.Prefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying MCPServerRegistration status is Ready=True after re-enabling")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	It("[Full] tools and prompts re-populate after gateway restart no redis", Serial, func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)
		mcpGatewayClient := newTestGatewayClient()

		deploymentName := "mcp-gateway"

		By("Registering an MCPServerRegistration with tools and prompts")
		registration := NewMCPServerResourcesWithDefaults("restart-test", k8sClient).
			WithBackendTarget(sharedMCPTestServer1, 9090).WithPrefix("restart_").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Waiting for the server to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying tools are present before restart")
		WaitForToolsWithPrefix(ctx, mcpGatewayClient, registeredServer.Spec.Prefix)

		By("Verifying prompts are present before restart")
		WaitForPromptsWithPrefix(ctx, mcpGatewayClient, registeredServer.Spec.Prefix)

		By("Closing the MCP client before restart")
		Expect(mcpGatewayClient.Close()).To(Succeed())

		By("Restarting the mcp-gateway deployment")
		Expect(RestartDeploymentAndWait(ctx, SystemNamespace, deploymentName)).To(Succeed())

		By("Verifying tools re-populate and tool invocation works after restart")
		toolName := fmt.Sprintf("%s%s", registeredServer.Spec.Prefix, "greet")
		Eventually(func(g Gomega) {
			sessionID, err := mcpInitialize(ctx, gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(sessionID).NotTo(BeEmpty())

			err = mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)
			g.Expect(err).NotTo(HaveOccurred())

			_, tools, err := mcpListTools(ctx, gatewayURL, sessionID, nil)
			g.Expect(err).NotTo(HaveOccurred())
			hasPrefix := false
			for _, t := range tools {
				if strings.HasPrefix(t, registeredServer.Spec.Prefix) {
					hasPrefix = true
					break
				}
			}
			g.Expect(hasPrefix).To(BeTrue(), "tools with prefix %q should exist after restart", registeredServer.Spec.Prefix)

			_, prompts, err := mcpListPrompts(ctx, gatewayURL, sessionID, nil)
			g.Expect(err).NotTo(HaveOccurred())
			hasPromptPrefix := false
			for _, p := range prompts {
				if strings.HasPrefix(p, registeredServer.Spec.Prefix) {
					hasPromptPrefix = true
					break
				}
			}
			g.Expect(hasPromptPrefix).To(BeTrue(), "prompts with prefix %q should exist after restart", registeredServer.Spec.Prefix)

			_, content, err := mcpCallTool(ctx, gatewayURL, sessionID, toolName, map[string]any{"name": "restart-test"}, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(content).NotTo(BeEmpty())
			g.Expect(content[0].Text).To(Equal("Hi restart-test"))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})
})
