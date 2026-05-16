//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("MCP Gateway Prompt Federation", func() {
	var (
		testResources []client.Object
		headers       map[string]string
	)

	BeforeEach(func() {
		testResources = []client.Object{}
		headers = make(map[string]string)
	})

	AfterEach(func() {
		for i := len(testResources) - 1; i >= 0; i-- {
			CleanupResource(ctx, k8sClient, testResources[i])
		}
	})

	It("[Happy] prompts/list returns federated prompts with prefixes", func() {
		By("Registering server1 with prompts")
		reg1 := NewTestResources("prompt-list-prefix", k8sClient).
			ForInternalService(sharedMCPTestServer1, 9090).
			WithPrefix("test1_").
			Build()
		testResources = append(testResources, reg1.GetObjects()...)
		server1 := reg1.Register(ctx)

		By("Waiting for MCPServerRegistration to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Initializing a session")
		sessionID, err := mcpInitialize(ctx, gatewayURL, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

		By("Verifying prompts/list returns prefixed prompts")
		Eventually(func(g Gomega) {
			status, prompts, err := mcpListPrompts(ctx, gatewayURL, sessionID, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(status).To(Equal(http.StatusOK))
			g.Expect(prompts).To(ContainElement("test1_greet"))
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	It("[Happy] Multi-server prompt aggregation", func() {
		By("Registering server1 with prefix s1_")
		reg1 := NewTestResources("prompt-agg-s1", k8sClient).
			ForInternalService(sharedMCPTestServer1, 9090).
			WithPrefix("s1_").
			Build()
		testResources = append(testResources, reg1.GetObjects()...)
		server1 := reg1.Register(ctx)

		By("Registering everything-server with prefix e_")
		reg2 := NewTestResources("prompt-agg-e", k8sClient).
			ForInternalService("everything-server", 9090).
			WithPrefix("e_").
			Build()
		testResources = append(testResources, reg2.GetObjects()...)
		server2 := reg2.Register(ctx)

		By("Waiting for MCPServerRegistrations to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(BeNil())
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server2.Name, server2.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Initializing a session")
		sessionID, err := mcpInitialize(ctx, gatewayURL, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

		By("Verifying prompts/list returns prompts from both servers")
		Eventually(func(g Gomega) {
			status, prompts, err := mcpListPrompts(ctx, gatewayURL, sessionID, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(status).To(Equal(http.StatusOK))
			g.Expect(prompts).To(ContainElement("s1_greet"))
			// everything-server usually has several prompts
			found := false
			for _, p := range prompts {
				if strings.HasPrefix(p, "e_") {
					found = true
					break
				}
			}
			g.Expect(found).To(BeTrue(), "should have at least one prompt from everything-server")
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	It("[Happy] prompts/get routes to the correct upstream server", func() {
		By("Registering server1 with prompts")
		reg1 := NewTestResources("prompt-get-route", k8sClient).
			ForInternalService(sharedMCPTestServer1, 9090).
			WithPrefix("test1_").
			Build()
		testResources = append(testResources, reg1.GetObjects()...)
		server1 := reg1.Register(ctx)

		By("Waiting for MCPServerRegistration to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Initializing a session")
		sessionID, err := mcpInitialize(ctx, gatewayURL, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

		By("Invoking prompts/get for test1_greet")
		Eventually(func(g Gomega) {
			status, messages, err := mcpGetPrompt(ctx, gatewayURL, sessionID, "test1_greet", map[string]string{"name": "e2e"}, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(status).To(Equal(http.StatusOK))
			g.Expect(messages).NotTo(BeEmpty())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	It("[Happy] VirtualServer filters prompts", func() {
		By("Registering server1 with prompts")
		reg1 := NewTestResources("prompt-vs-filter", k8sClient).
			ForInternalService(sharedMCPTestServer1, 9090).
			WithPrefix("test1_").
			Build()
		testResources = append(testResources, reg1.GetObjects()...)
		server1 := reg1.Register(ctx)

		By("Waiting for MCPServerRegistration to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating an MCPVirtualServer with a subset of prompts")
		vs := NewMCPVirtualServerBuilder("prompt-vs", TestServerNameSpace).
			WithPrompts([]string{"test1_greet"}).
			Build()
		testResources = append(testResources, vs)
		Expect(k8sClient.Create(ctx, vs)).To(Succeed())

		By("Initializing a session with X-Mcp-Virtualserver header")
		headers["X-Mcp-Virtualserver"] = fmt.Sprintf("%s/%s", vs.Namespace, vs.Name)
		sessionID, err := mcpInitialize(ctx, gatewayURL, headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, headers)).To(Succeed())

		By("Verifying prompts/list is filtered")
		Eventually(func(g Gomega) {
			status, prompts, err := mcpListPrompts(ctx, gatewayURL, sessionID, headers)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(status).To(Equal(http.StatusOK))
			g.Expect(prompts).To(Equal([]string{"test1_greet"}))
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	It("[Happy] VirtualServer with no prompts field exposes all prompts", func() {
		By("Registering server1 with prompts")
		reg1 := NewTestResources("prompt-vs-no-field", k8sClient).
			ForInternalService(sharedMCPTestServer1, 9090).
			WithPrefix("test1_").
			Build()
		testResources = append(testResources, reg1.GetObjects()...)
		server1 := reg1.Register(ctx)

		By("Waiting for MCPServerRegistration to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating an MCPVirtualServer with no prompts field")
		// NewMCPVirtualServerBuilder doesn't set Prompts by default
		vs := NewMCPVirtualServerBuilder("prompt-vs-empty", TestServerNameSpace).Build()
		testResources = append(testResources, vs)
		Expect(k8sClient.Create(ctx, vs)).To(Succeed())

		By("Initializing a session with X-Mcp-Virtualserver header")
		headers["X-Mcp-Virtualserver"] = fmt.Sprintf("%s/%s", vs.Namespace, vs.Name)
		sessionID, err := mcpInitialize(ctx, gatewayURL, headers)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, headers)).To(Succeed())

		By("Verifying prompts/list returns all prompts")
		Eventually(func(g Gomega) {
			status, prompts, err := mcpListPrompts(ctx, gatewayURL, sessionID, headers)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(status).To(Equal(http.StatusOK))
			g.Expect(prompts).To(ContainElement("test1_greet"))
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	It("[Happy] prompts/get for nonexistent prompt returns error", func() {
		By("Initializing a session")
		sessionID, err := mcpInitialize(ctx, gatewayURL, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())

		By("Invoking prompts/get for non_existent_prompt")
		status, _, err := mcpGetPrompt(ctx, gatewayURL, sessionID, "non_existent_prompt", nil, nil)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("-32602"))
		Expect(status).To(Equal(http.StatusOK)) // JSON-RPC errors still return 200 OK
	})

	Context("Auth tests", func() {
		BeforeEach(func() {
			if !IsAuthPolicyConfigured(ctx) {
				Skip("auth not configured - skipping prompt auth tests")
			}
		})

		It("[Auth] JWT-filtered prompts/list with Keycloak", func() {
			By("Registering servers matching Keycloak client IDs")
			reg1 := NewTestResources("auth-prompt-s1", k8sClient).
				ForInternalService(sharedMCPTestServer1, 9090).
				WithPrefix("test1_").
				WithRegistrationName("test-server1").
				Build()
			testResources = append(testResources, reg1.GetObjects()...)
			server1 := reg1.Register(ctx)

			By("Waiting for MCPServerRegistration to become ready")
			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(BeNil())
			}, TestTimeoutLong, TestRetryInterval).To(Succeed())

			By("Obtaining a token from Keycloak")
			token, err := GetKeycloakUserToken(ctx, "mcp", "mcp")
			Expect(err).NotTo(HaveOccurred())
			headers["Authorization"] = "Bearer " + token

			By("Initializing a session")
			sessionID, err := mcpInitialize(ctx, authGatewayURL, headers)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, authGatewayURL, sessionID, headers)).To(Succeed())

			By("Verifying prompts/list is filtered by user roles")
			// mcp user in accounting group has prompt:* for test-server1
			Eventually(func(g Gomega) {
				status, prompts, err := mcpListPrompts(ctx, authGatewayURL, sessionID, headers)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(status).To(Equal(http.StatusOK))
				g.Expect(prompts).To(ContainElement("test1_greet"))
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		})

		It("[Auth] prompts/get with auth as first request (hairpin test)", func() {
			By("Registering servers matching Keycloak client IDs")
			reg1 := NewTestResources("auth-hairpin-prompt", k8sClient).
				ForInternalService(sharedMCPTestServer1, 9090).
				WithPrefix("test1_").
				WithRegistrationName("test-server1").
				Build()
			testResources = append(testResources, reg1.GetObjects()...)
			server1 := reg1.Register(ctx)

			By("Waiting for MCPServerRegistration to become ready")
			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(BeNil())
			}, TestTimeoutLong, TestRetryInterval).To(Succeed())

			By("Obtaining a token from Keycloak")
			token, err := GetKeycloakUserToken(ctx, "mcp", "mcp")
			Expect(err).NotTo(HaveOccurred())
			headers["Authorization"] = "Bearer " + token

			By("Invoking prompts/get as the first request (no prior session)")
			// gatewayURL is used here but maybe it should be authGatewayURL
			// Hairpin test implies it's the first thing called, so gateway will do lazy init
			Eventually(func(g Gomega) {
				status, messages, err := mcpGetPrompt(ctx, authGatewayURL, "", "test1_greet", map[string]string{"name": "e2e"}, headers)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(status).To(Equal(http.StatusOK))
				g.Expect(messages).NotTo(BeEmpty())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		})

		It("[Auth] Combined JWT + VirtualServer prompt filtering", func() {
			By("Registering servers matching Keycloak client IDs")
			reg1 := NewTestResources("auth-vs-prompt", k8sClient).
				ForInternalService(sharedMCPTestServer1, 9090).
				WithPrefix("test1_").
				WithRegistrationName("test-server1").
				Build()
			testResources = append(testResources, reg1.GetObjects()...)
			server1 := reg1.Register(ctx)

			By("Waiting for MCPServerRegistration to become ready")
			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(BeNil())
			}, TestTimeoutLong, TestRetryInterval).To(Succeed())

			By("Creating an MCPVirtualServer that allows a DIFFERENT prompt (which doesn't exist on server1)")
			vs := NewMCPVirtualServerBuilder("prompt-vs-combined", TestServerNameSpace).
				WithPrompts([]string{"test1_non_existent"}).
				Build()
			testResources = append(testResources, vs)
			Expect(k8sClient.Create(ctx, vs)).To(Succeed())

			By("Obtaining a token from Keycloak")
			token, err := GetKeycloakUserToken(ctx, "mcp", "mcp")
			Expect(err).NotTo(HaveOccurred())
			headers["Authorization"] = "Bearer " + token
			headers["X-Mcp-Virtualserver"] = fmt.Sprintf("%s/%s", vs.Namespace, vs.Name)

			By("Initializing a session")
			sessionID, err := mcpInitialize(ctx, authGatewayURL, headers)
			Expect(err).NotTo(HaveOccurred())
			Expect(mcpNotifyInitialized(ctx, authGatewayURL, sessionID, headers)).To(Succeed())

			By("Verifying prompts/list is empty (intersection of test1_greet and test1_non_existent)")
			Eventually(func(g Gomega) {
				status, prompts, err := mcpListPrompts(ctx, authGatewayURL, sessionID, headers)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(status).To(Equal(http.StatusOK))
				g.Expect(prompts).To(BeEmpty())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		})
	})
})
