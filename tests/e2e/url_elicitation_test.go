//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("URL Elicitation", func() {
	var (
		testResources []client.Object
		prefix        string
	)

	BeforeEach(func() {
		By("Enabling URL elicitation on the MCPGatewayExtension")
		Expect(SetURLElicitation(SystemNamespace, MCPExtensionName, true)).To(Succeed())
		Expect(WaitForDeploymentReady(context.Background(), SystemNamespace, "mcp-gateway")).To(Succeed())

		By("Pre-cleaning credential secret from prior runs")
		cred := BuildCredentialSecret("url-elicit-cred", "test-api-key-secret-token")
		CleanupResource(ctx, k8sClient, cred)

		By("Registering api-key-server with tokenURLElicitation and credentialRef")
		cred = BuildCredentialSecret("url-elicit-cred", "test-api-key-secret-token")
		registration := NewMCPServerResourcesWithDefaults("urlelicit", k8sClient).
			WithCredential(cred, "token").
			WithBackendTarget("mcp-api-key-server", 9090).
			WithPrefix("ue_").
			WithTokenURLElicitation("").
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)
		prefix = registeredServer.Spec.Prefix

		By("Waiting for the server to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutConfigSync, TestRetryInterval).To(Succeed())
	})

	AfterEach(func() {
		for _, to := range testResources {
			CleanupResource(ctx, k8sClient, to)
		}
		testResources = nil

		By("Disabling URL elicitation on the MCPGatewayExtension")
		Expect(SetURLElicitation(SystemNamespace, MCPExtensionName, false)).To(Succeed())
		Expect(WaitForDeploymentReady(context.Background(), SystemNamespace, "mcp-gateway")).To(Succeed())
	})

	It("[Happy,URLElicitation] URL elicitation triggers on missing token for elicitation-capable client", func() {
		toolName := fmt.Sprintf("%shello_world", prefix)

		By("Initializing with elicitation capability")
		var sessionID string
		Eventually(func(g Gomega) {
			var err error
			sessionID, err = mcpInitializeWithElicitation(gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(sessionID).NotTo(BeEmpty())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		Expect(mcpNotifyInitialized(context.Background(), gatewayURL, sessionID, nil)).To(Succeed())

		By("Waiting for tools to be available")
		Eventually(func(g Gomega) {
			_, tools, err := mcpListTools(context.Background(), gatewayURL, sessionID, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(tools).To(ContainElement(toolName))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Calling tool — should get -32042 with elicitation URL")
		status, body, _, err := mcpCallToolRaw(gatewayURL, sessionID, toolName, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200))

		sseErr, err := parseSSEError(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(sseErr.Code).To(Equal(-32042))
		Expect(sseErr.Message).To(Equal("URL elicitation required"))

		elicitURL, err := extractElicitationURL(sseErr)
		Expect(err).NotTo(HaveOccurred())
		Expect(elicitURL).To(ContainSubstring("elicitation_id="))
	})

	It("[Happy,URLElicitation] Full round-trip: token page submit then retry succeeds", func() {
		toolName := fmt.Sprintf("%shello_world", prefix)

		By("Initializing with elicitation capability")
		var sessionID string
		Eventually(func(g Gomega) {
			var err error
			sessionID, err = mcpInitializeWithElicitation(gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		Expect(mcpNotifyInitialized(context.Background(), gatewayURL, sessionID, nil)).To(Succeed())

		By("Waiting for tools to be available")
		Eventually(func(g Gomega) {
			_, tools, err := mcpListTools(context.Background(), gatewayURL, sessionID, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(tools).To(ContainElement(toolName))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Calling tool — should get -32042")
		_, body, _, err := mcpCallToolRaw(gatewayURL, sessionID, toolName, nil, nil)
		Expect(err).NotTo(HaveOccurred())

		sseErr, err := parseSSEError(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(sseErr.Code).To(Equal(-32042))

		elicitURL, err := extractElicitationURL(sseErr)
		Expect(err).NotTo(HaveOccurred())

		By("Adapting URL for test environment")
		testURL, err := adaptElicitationURL(elicitURL, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Println("token page URL:", testURL)

		By("GET /tokens — should return form page with CSRF token")
		status, htmlBody, cookies, err := rawHTTPGetFull(testURL, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200))
		Expect(htmlBody).To(ContainSubstring("elicitation_id"))

		csrfToken := extractHiddenField(htmlBody, "csrf_token")
		Expect(csrfToken).NotTo(BeEmpty(), "csrf_token hidden field not found in form")

		By("Extracting elicitation_id from URL")
		parsed, err := url.Parse(testURL)
		Expect(err).NotTo(HaveOccurred())
		elicitationID := parsed.Query().Get("elicitation_id")

		By("POST /tokens — submit token with CSRF cookie and token")
		formValues := url.Values{
			"elicitation_id": {elicitationID},
			"token":          {"Bearer test-api-key-secret-token"},
			"csrf_token":     {csrfToken},
		}
		postStatus, _, err := rawHTTPPostForm(
			strings.TrimSuffix(gatewayURL, "/mcp")+"/tokens",
			formValues,
			nil,
			cookies...,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(postStatus).To(Equal(200))

		By("Retrying tool call — should succeed now")
		retryStatus, retryContent, err := mcpCallTool(context.Background(), gatewayURL, sessionID, toolName, map[string]any{"name": "e2e"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(retryStatus).To(Equal(200))
		Expect(retryContent).NotTo(BeEmpty())
		Expect(retryContent[0].Text).To(ContainSubstring("Hello"))
	})

	It("[URLElicitation] Cached token reused across multiple tool calls", func() {
		toolName := fmt.Sprintf("%shello_world", prefix)

		By("Initializing, triggering -32042, and submitting token")
		var sessionID string
		Eventually(func(g Gomega) {
			var err error
			sessionID, err = mcpInitializeWithElicitation(gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		Expect(mcpNotifyInitialized(context.Background(), gatewayURL, sessionID, nil)).To(Succeed())

		Eventually(func(g Gomega) {
			_, tools, err := mcpListTools(context.Background(), gatewayURL, sessionID, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(tools).To(ContainElement(toolName))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		_, body, _, err := mcpCallToolRaw(gatewayURL, sessionID, toolName, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		sseErr, err := parseSSEError(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(sseErr.Code).To(Equal(-32042))

		elicitURL, err := extractElicitationURL(sseErr)
		Expect(err).NotTo(HaveOccurred())
		testURL, err := adaptElicitationURL(elicitURL, gatewayURL)
		Expect(err).NotTo(HaveOccurred())

		_, htmlBody, cookies, err := rawHTTPGetFull(testURL, nil)
		Expect(err).NotTo(HaveOccurred())
		csrfToken := extractHiddenField(htmlBody, "csrf_token")
		Expect(csrfToken).NotTo(BeEmpty())

		parsed, err := url.Parse(testURL)
		Expect(err).NotTo(HaveOccurred())
		elicitationID := parsed.Query().Get("elicitation_id")

		formValues := url.Values{
			"elicitation_id": {elicitationID},
			"token":          {"Bearer test-api-key-secret-token"},
			"csrf_token":     {csrfToken},
		}
		postStatus, _, postErr := rawHTTPPostForm(
			strings.TrimSuffix(gatewayURL, "/mcp")+"/tokens",
			formValues,
			nil,
			cookies...,
		)
		Expect(postErr).NotTo(HaveOccurred())
		Expect(postStatus).To(Equal(200))

		By("First tool call should succeed with cached token")
		status1, content1, err := mcpCallTool(context.Background(), gatewayURL, sessionID, toolName, map[string]any{"name": "call1"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status1).To(Equal(200))
		Expect(content1).NotTo(BeEmpty())

		By("Second tool call should also succeed — no new -32042")
		status2, content2, err := mcpCallTool(context.Background(), gatewayURL, sessionID, toolName, map[string]any{"name": "call2"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status2).To(Equal(200))
		Expect(content2).NotTo(BeEmpty())
		Expect(content2[0].Text).To(ContainSubstring("Hello"))
	})

	It("[URLElicitation] Non-elicitation-capable client gets standard error on missing token", func() {
		toolName := fmt.Sprintf("%shello_world", prefix)

		By("Initializing WITHOUT elicitation capability")
		var sessionID string
		Eventually(func(g Gomega) {
			var err error
			sessionID, err = mcpInitialize(context.Background(), gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		Expect(mcpNotifyInitialized(context.Background(), gatewayURL, sessionID, nil)).To(Succeed())

		By("Waiting for tools to be available")
		Eventually(func(g Gomega) {
			_, tools, err := mcpListTools(context.Background(), gatewayURL, sessionID, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(tools).To(ContainElement(toolName))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Calling tool — should get an isError result, NOT -32042")
		status, body, _, err := mcpCallToolRaw(gatewayURL, sessionID, toolName, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200))
		Expect(body).To(ContainSubstring(`"isError":true`))
		Expect(body).To(ContainSubstring("elicitation"))
		Expect(body).NotTo(ContainSubstring("-32042"))
	})

	It("[Happy,URLElicitation] 401 from upstream invalidates cached token and re-triggers elicitation", func() {
		toolName := fmt.Sprintf("%shello_world", prefix)

		By("Initializing with elicitation capability")
		var sessionID string
		Eventually(func(g Gomega) {
			var err error
			sessionID, err = mcpInitializeWithElicitation(gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		Expect(mcpNotifyInitialized(context.Background(), gatewayURL, sessionID, nil)).To(Succeed())

		By("Waiting for tools to be available")
		Eventually(func(g Gomega) {
			_, tools, err := mcpListTools(context.Background(), gatewayURL, sessionID, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(tools).To(ContainElement(toolName))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Calling tool — should get -32042 (no token yet)")
		_, body, _, err := mcpCallToolRaw(gatewayURL, sessionID, toolName, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		sseErr, err := parseSSEError(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(sseErr.Code).To(Equal(-32042))

		By("Submitting the CORRECT token via broker page")
		elicitURL, err := extractElicitationURL(sseErr)
		Expect(err).NotTo(HaveOccurred())
		testURL, err := adaptElicitationURL(elicitURL, gatewayURL)
		Expect(err).NotTo(HaveOccurred())

		_, htmlBody, cookies, err := rawHTTPGetFull(testURL, nil)
		Expect(err).NotTo(HaveOccurred())
		csrfToken := extractHiddenField(htmlBody, "csrf_token")
		Expect(csrfToken).NotTo(BeEmpty())

		parsed, err := url.Parse(testURL)
		Expect(err).NotTo(HaveOccurred())
		elicitationID := parsed.Query().Get("elicitation_id")

		formValues := url.Values{
			"elicitation_id": {elicitationID},
			"token":          {"Bearer test-api-key-secret-token"},
			"csrf_token":     {csrfToken},
		}
		postStatus, _, postErr := rawHTTPPostForm(
			strings.TrimSuffix(gatewayURL, "/mcp")+"/tokens",
			formValues,
			nil,
			cookies...,
		)
		Expect(postErr).NotTo(HaveOccurred())
		Expect(postStatus).To(Equal(200))

		By("Calling tool with correct token — should succeed and establish backend session")
		successStatus, successContent, err := mcpCallTool(context.Background(), gatewayURL, sessionID, toolName, map[string]any{"name": "setup"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(successStatus).To(Equal(200))
		Expect(successContent).NotTo(BeEmpty())
		Expect(successContent[0].Text).To(ContainSubstring("Hello"))

		By("Calling tool with X-Force-Auth-Reject — upstream returns 401, gateway invalidates token")
		retryStatus, _, _, err := mcpCallToolRaw(gatewayURL, sessionID, toolName, map[string]any{"name": "reject"}, map[string]string{"X-Force-Auth-Reject": "true"})
		Expect(err).NotTo(HaveOccurred())
		Expect(retryStatus).To(Equal(401))

		By("Retrying — should get -32042 (token was invalidated)")
		_, body2, _, err := mcpCallToolRaw(gatewayURL, sessionID, toolName, map[string]any{"name": "retry"}, nil)
		Expect(err).NotTo(HaveOccurred())
		sseErr2, err := parseSSEError(body2)
		Expect(err).NotTo(HaveOccurred())
		Expect(sseErr2.Code).To(Equal(-32042))

		By("Submitting the correct token again")
		elicitURL2, err := extractElicitationURL(sseErr2)
		Expect(err).NotTo(HaveOccurred())
		testURL2, err := adaptElicitationURL(elicitURL2, gatewayURL)
		Expect(err).NotTo(HaveOccurred())

		_, htmlBody2, cookies2, err := rawHTTPGetFull(testURL2, nil)
		Expect(err).NotTo(HaveOccurred())
		csrfToken2 := extractHiddenField(htmlBody2, "csrf_token")
		Expect(csrfToken2).NotTo(BeEmpty())

		parsed2, err := url.Parse(testURL2)
		Expect(err).NotTo(HaveOccurred())
		elicitationID2 := parsed2.Query().Get("elicitation_id")

		formValues2 := url.Values{
			"elicitation_id": {elicitationID2},
			"token":          {"Bearer test-api-key-secret-token"},
			"csrf_token":     {csrfToken2},
		}
		postStatus2, _, postErr2 := rawHTTPPostForm(
			strings.TrimSuffix(gatewayURL, "/mcp")+"/tokens",
			formValues2,
			nil,
			cookies2...,
		)
		Expect(postErr2).NotTo(HaveOccurred())
		Expect(postStatus2).To(Equal(200))

		By("Final retry — should succeed with correct token")
		finalStatus, finalContent, err := mcpCallTool(context.Background(), gatewayURL, sessionID, toolName, map[string]any{"name": "final"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(finalStatus).To(Equal(200))
		Expect(finalContent).NotTo(BeEmpty())
		Expect(finalContent[0].Text).To(ContainSubstring("Hello"))
	})

	It("[Happy,URLElicitation] Server without tokenURLElicitation is unaffected", func() {
		By("Registering a server WITHOUT tokenURLElicitation or credentialRef")
		registration2 := NewMCPServerResourcesWithDefaults("urlelicit-nocfg", k8sClient).
			WithBackendTarget(sharedMCPTestServer1, 9090).
			WithPrefix("uenone_").
			Build()
		testResources = append(testResources, registration2.GetObjects()...)
		registeredServer2 := registration2.Register(ctx)
		prefix2 := registeredServer2.Spec.Prefix

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer2.Name, registeredServer2.Namespace)).To(BeNil())
		}, TestTimeoutConfigSync, TestRetryInterval).To(Succeed())

		toolName2 := fmt.Sprintf("%sgreet", prefix2)

		By("Initializing with elicitation capability")
		var sessionID string
		Eventually(func(g Gomega) {
			var err error
			sessionID, err = mcpInitializeWithElicitation(gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		Expect(mcpNotifyInitialized(context.Background(), gatewayURL, sessionID, nil)).To(Succeed())

		By("Waiting for tools")
		Eventually(func(g Gomega) {
			_, tools, err := mcpListTools(context.Background(), gatewayURL, sessionID, nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(tools).To(ContainElement(toolName2))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Calling tool — should succeed without elicitation, no -32042")
		status, content, err := mcpCallTool(context.Background(), gatewayURL, sessionID, toolName2, map[string]any{"name": "direct"}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(200))
		Expect(content).NotTo(BeEmpty())
		Expect(content[0].Text).To(ContainSubstring("Hi direct"))
	})
})
