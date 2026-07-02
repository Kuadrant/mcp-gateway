//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type acceptHandler struct {
	content map[string]any
}

func (h *acceptHandler) Elicit(_ context.Context, req mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	GinkgoWriter.Printf("elicitation request received: %s\n", req.Params.Message)
	return &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{
			Action:  mcp.ElicitationResponseActionAccept,
			Content: h.content,
		},
	}, nil
}

type declineHandler struct{}

func (h *declineHandler) Elicit(_ context.Context, req mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	GinkgoWriter.Printf("elicitation request received (declining): %s\n", req.Params.Message)
	return &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{
			Action: mcp.ElicitationResponseActionDecline,
		},
	}, nil
}

const (
	elicitExtName    = "elicitation-ext"
	elicitNamespace  = "mcp-elicitation"
	elicitPublicHost = "elicit.mcp-gateway.local"
)

var _ = Describe("Elicitation", Ordered, ContinueOnFailure, func() {
	var (
		testResources  []client.Object
		prefix         string
		elicitationExt *MCPGatewayExtensionSetup
	)

	BeforeAll(func() {
		By("Creating elicitation namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   elicitNamespace,
				Labels: map[string]string{"e2e": "test"},
			},
		}
		_ = k8sClient.Delete(ctx, ns)
		Eventually(func(g Gomega) {
			err := k8sClient.Create(ctx, ns)
			g.Expect(client.IgnoreAlreadyExists(err)).NotTo(HaveOccurred())
		}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

		By("Waiting for elicitation gateway to be programmed")
		Eventually(func(g Gomega) {
			gw := &gatewayapiv1.Gateway{}
			err := k8sClient.Get(ctx, client.ObjectKey{Name: ElicitationGatewayName, Namespace: GatewayNamespace}, gw)
			g.Expect(err).NotTo(HaveOccurred())
			programmed := false
			for _, cond := range gw.Status.Conditions {
				if cond.Type == string(gatewayapiv1.GatewayConditionProgrammed) && cond.Status == metav1.ConditionTrue {
					programmed = true
					break
				}
			}
			g.Expect(programmed).To(BeTrue(), "gateway %s should have Programmed=True", ElicitationGatewayName)
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Creating MCPGatewayExtension with URL elicitation enabled")
		elicitationExt = NewMCPGatewayExtensionSetup(k8sClient).
			WithName(elicitExtName).
			InNamespace(elicitNamespace).
			TargetingGateway(ElicitationGatewayName, GatewayNamespace).
			WithSectionName(ElicitationListenerName).
			WithPublicHost(elicitPublicHost).
			WithListenerPort(8443).
			WithURLElicitation().
			Build()
		elicitationExt.Clean(ctx).Register(ctx)

		By("Waiting for MCPGatewayExtension to become ready")
		Eventually(func(g Gomega) {
			err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, elicitExtName, elicitNamespace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Waiting for broker/router deployment to be ready")
		Eventually(func(g Gomega) {
			err := WaitForDeploymentReady(ctx, elicitNamespace, "mcp-gateway")
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Patching broker-router with CA cert for HTTPS hairpin")
		PatchBrokerCA(ctx, k8sClient, elicitNamespace)

		By("Registering an MCPServerRegistration pointing to the everything-server")
		registration := NewTestResources("elicitation", k8sClient).
			InNamespace(elicitNamespace).
			ForInternalService("everything-server", 9090).
			WithHostname("everything-server.mcp-gateway.local").
			WithBackendNamespace(TestServerNameSpace).
			WithPrefix("es_").
			WithParentGateway(ElicitationGatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)
		prefix = registeredServer.Spec.Prefix

		By("Waiting for the server to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	AfterAll(func() {
		for i := len(testResources) - 1; i >= 0; i-- {
			CleanupResource(ctx, k8sClient, testResources[i])
		}
		if elicitationExt != nil {
			elicitationExt.TearDown(ctx)
		}
	})

	// --- MCP protocol elicitation tests ---

	It("[Elicitation] should accept elicitation and return user-provided information", func() {
		toolName := fmt.Sprintf("%strigger-elicitation-request", prefix)

		handler := &acceptHandler{
			content: map[string]any{
				"name": "e2e-test-user",
			},
		}

		var elicitClient *mcpclient.Client
		Eventually(func(g Gomega) {
			var err error
			elicitClient, err = NewMCPGatewayClientWithElicitation(ctx, ElicitationGatewayURL, handler)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer func() { _ = elicitClient.Close() }()

		By("Verifying the trigger-elicitation-request tool is visible")
		Eventually(func(g Gomega) {
			toolsList, err := elicitClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolPresent(toolName, toolsList)).To(BeTrueBecause("%s should exist", toolName))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Calling trigger-elicitation-request tool")
		var responseText string
		Eventually(func(g Gomega) {
			res, err := elicitClient.CallTool(ctx, mcp.CallToolRequest{
				Params: mcp.CallToolParams{Name: toolName, Arguments: map[string]any{}},
			})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(res).NotTo(BeNil())
			g.Expect(len(res.Content)).To(BeNumerically(">=", 1))

			responseText = ""
			for _, c := range res.Content {
				tc, ok := c.(mcp.TextContent)
				if ok {
					responseText += tc.Text
				}
			}
			GinkgoWriter.Println("accept response:", responseText)
			g.Expect(responseText).To(ContainSubstring("User provided the requested information"))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	It("[Elicitation] should decline elicitation", func() {
		toolName := fmt.Sprintf("%strigger-elicitation-request", prefix)

		handler := &declineHandler{}

		var elicitClient *mcpclient.Client
		Eventually(func(g Gomega) {
			var err error
			elicitClient, err = NewMCPGatewayClientWithElicitation(ctx, ElicitationGatewayURL, handler)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer func() { _ = elicitClient.Close() }()

		By("Verifying the trigger-elicitation-request tool is visible")
		Eventually(func(g Gomega) {
			toolsList, err := elicitClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolPresent(toolName, toolsList)).To(BeTrueBecause("%s should exist", toolName))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Calling trigger-elicitation-request tool")
		var responseText string
		Eventually(func(g Gomega) {
			res, err := elicitClient.CallTool(ctx, mcp.CallToolRequest{
				Params: mcp.CallToolParams{Name: toolName, Arguments: map[string]any{}},
			})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(res).NotTo(BeNil())
			g.Expect(len(res.Content)).To(BeNumerically(">=", 1))

			responseText = ""
			for _, c := range res.Content {
				tc, ok := c.(mcp.TextContent)
				if ok {
					responseText += tc.Text
				}
			}
			GinkgoWriter.Println("decline response:", responseText)
			g.Expect(responseText).To(ContainSubstring("User declined"))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	It("[Full][Elicitation] should error when calling elicitation tool without handler", func() {
		toolName := fmt.Sprintf("%strigger-elicitation-request", prefix)

		By("Creating a standard client without elicitation handler")
		var standardClient *mcpclient.Client
		Eventually(func(g Gomega) {
			var err error
			standardClient, err = NewMCPGatewayClient(ctx, ElicitationGatewayURL)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer func() { _ = standardClient.Close() }()

		By("Verifying the trigger-elicitation-request tool is visible in tools/list")
		Eventually(func(g Gomega) {
			toolsList, err := standardClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			found := false
			for _, t := range toolsList.Tools {
				if strings.HasSuffix(t.Name, "trigger-elicitation-request") {
					found = true
				}
			}
			g.Expect(found).To(BeTrue(), "trigger-elicitation-request tool should be in tools/list")
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Calling trigger-elicitation-request tool should error")
		res, err := standardClient.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: toolName, Arguments: map[string]any{}},
		})
		if err != nil {
			GinkgoWriter.Println("tool call error (expected):", err)
			return
		}
		Expect(res).NotTo(BeNil())
		GinkgoWriter.Println("no-handler response:", res.Content)
		Expect(res.IsError).To(BeTrue(),
			"calling elicitation tool without handler should produce an error")
	})

	// --- URL elicitation tests ---
	// These share the same gateway/extension (with URLElicitation enabled)
	// but register the api-key-server per test via BeforeEach/AfterEach.

	Context("URL Elicitation", func() {
		var urlTestResources []client.Object
		var urlPrefix string

		BeforeEach(func() {
			By("Pre-cleaning credential secret from prior runs")
			cred := BuildCredentialSecret("url-elicit-cred", "test-api-key-secret-token")
			cred.Namespace = elicitNamespace
			CleanupResource(ctx, k8sClient, cred)

			By("Registering api-key-server with tokenURLElicitation and credentialRef")
			cred = BuildCredentialSecret("url-elicit-cred", "test-api-key-secret-token")
			cred.Namespace = elicitNamespace
			registration := NewTestResources("urlelicit", k8sClient).
				InNamespace(elicitNamespace).
				WithCredential(cred, "token").
				WithBackendTarget("mcp-api-key-server", 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithPrefix("ue_").
				WithTokenURLElicitation("").
				WithParentGateway(ElicitationGatewayName, GatewayNamespace).
				Build()
			urlTestResources = append(urlTestResources, registration.GetObjects()...)
			registeredServer := registration.Register(ctx)
			urlPrefix = registeredServer.Spec.Prefix

			By("Waiting for the server to become ready")
			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
			}, TestTimeoutConfigSync, TestRetryInterval).To(Succeed())
		})

		AfterEach(func() {
			for _, to := range urlTestResources {
				CleanupResource(ctx, k8sClient, to)
			}
			urlTestResources = nil
		})

		It("[Happy,URLElicitation] URL elicitation triggers on missing token for elicitation-capable client; server without tokenURLElicitation is unaffected", func() {
			toolName := fmt.Sprintf("%shello_world", urlPrefix)

			By("Registering a second server WITHOUT tokenURLElicitation or credentialRef")
			registration2 := NewTestResources("urlelicit-nocfg", k8sClient).
				InNamespace(elicitNamespace).
				WithBackendTarget(sharedMCPTestServer1, 9090).
				WithBackendNamespace(TestServerNameSpace).
				WithPrefix("uenone_").
				WithParentGateway(ElicitationGatewayName, GatewayNamespace).
				Build()
			urlTestResources = append(urlTestResources, registration2.GetObjects()...)
			registeredServer2 := registration2.Register(ctx)
			toolName2 := fmt.Sprintf("%sgreet", registeredServer2.Spec.Prefix)

			Eventually(func(g Gomega) {
				g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer2.Name, registeredServer2.Namespace)).To(BeNil())
			}, TestTimeoutConfigSync, TestRetryInterval).To(Succeed())

			By("Initializing with elicitation capability")
			var sessionID string
			Eventually(func(g Gomega) {
				var err error
				sessionID, err = mcpInitializeWithElicitation(ElicitationGatewayURL, nil)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(sessionID).NotTo(BeEmpty())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

			Expect(mcpNotifyInitialized(context.Background(), ElicitationGatewayURL, sessionID, nil)).To(Succeed())

			By("Waiting for tools from both servers to be available")
			Eventually(func(g Gomega) {
				_, tools, err := mcpListTools(context.Background(), ElicitationGatewayURL, sessionID, nil)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(tools).To(ContainElement(toolName))
				g.Expect(tools).To(ContainElement(toolName2))
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("Calling tool on the elicitation server — should get -32042 with elicitation URL")
			status, body, _, err := mcpCallToolRaw(ElicitationGatewayURL, sessionID, toolName, nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))

			sseErr, err := parseSSEError(body)
			Expect(err).NotTo(HaveOccurred())
			Expect(sseErr.Code).To(Equal(-32042))
			Expect(sseErr.Message).To(Equal("URL elicitation required"))

			elicitURL, err := extractElicitationURL(sseErr)
			Expect(err).NotTo(HaveOccurred())
			Expect(elicitURL).To(ContainSubstring("elicitation_id="))

			By("Calling tool on the server without tokenURLElicitation — should succeed without elicitation, no -32042")
			directStatus, directContent, err := mcpCallTool(context.Background(), ElicitationGatewayURL, sessionID, toolName2, map[string]any{"name": "direct"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(directStatus).To(Equal(200))
			Expect(directContent).NotTo(BeEmpty())
			Expect(directContent[0].Text).To(ContainSubstring("Hi direct"))
		})

		It("[Happy,URLElicitation] Full round-trip: token page submit then retry succeeds", func() {
			toolName := fmt.Sprintf("%shello_world", urlPrefix)

			By("Initializing with elicitation capability")
			var sessionID string
			Eventually(func(g Gomega) {
				var err error
				sessionID, err = mcpInitializeWithElicitation(ElicitationGatewayURL, nil)
				g.Expect(err).NotTo(HaveOccurred())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

			Expect(mcpNotifyInitialized(context.Background(), ElicitationGatewayURL, sessionID, nil)).To(Succeed())

			By("Waiting for tools to be available")
			Eventually(func(g Gomega) {
				_, tools, err := mcpListTools(context.Background(), ElicitationGatewayURL, sessionID, nil)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(tools).To(ContainElement(toolName))
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("Calling tool — should get -32042")
			_, body, _, err := mcpCallToolRaw(ElicitationGatewayURL, sessionID, toolName, nil, nil)
			Expect(err).NotTo(HaveOccurred())

			sseErr, err := parseSSEError(body)
			Expect(err).NotTo(HaveOccurred())
			Expect(sseErr.Code).To(Equal(-32042))

			elicitURL, err := extractElicitationURL(sseErr)
			Expect(err).NotTo(HaveOccurred())

			By("Adapting URL for test environment")
			testURL, err := adaptElicitationURL(elicitURL, ElicitationGatewayURL)
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
				strings.TrimSuffix(ElicitationGatewayURL, "/mcp")+"/tokens",
				formValues,
				nil,
				cookies...,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(postStatus).To(Equal(200))

			By("Retrying tool call — should succeed now")
			retryStatus, retryContent, err := mcpCallTool(context.Background(), ElicitationGatewayURL, sessionID, toolName, map[string]any{"name": "e2e"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(retryStatus).To(Equal(200))
			Expect(retryContent).NotTo(BeEmpty())
			Expect(retryContent[0].Text).To(ContainSubstring("Hello"))

			By("Second tool call should also succeed with cached token — no new -32042")
			status2, content2, err := mcpCallTool(context.Background(), ElicitationGatewayURL, sessionID, toolName, map[string]any{"name": "call2"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status2).To(Equal(200))
			Expect(content2).NotTo(BeEmpty())
			Expect(content2[0].Text).To(ContainSubstring("Hello"))
		})

		It("[URLElicitation] Non-elicitation-capable client gets standard error on missing token", func() {
			toolName := fmt.Sprintf("%shello_world", urlPrefix)

			By("Initializing WITHOUT elicitation capability")
			var sessionID string
			Eventually(func(g Gomega) {
				var err error
				sessionID, err = mcpInitialize(context.Background(), ElicitationGatewayURL, nil)
				g.Expect(err).NotTo(HaveOccurred())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

			Expect(mcpNotifyInitialized(context.Background(), ElicitationGatewayURL, sessionID, nil)).To(Succeed())

			By("Waiting for tools to be available")
			Eventually(func(g Gomega) {
				_, tools, err := mcpListTools(context.Background(), ElicitationGatewayURL, sessionID, nil)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(tools).To(ContainElement(toolName))
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("Calling tool — should get an isError result, NOT -32042")
			status, body, _, err := mcpCallToolRaw(ElicitationGatewayURL, sessionID, toolName, nil, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(status).To(Equal(200))
			Expect(body).To(ContainSubstring(`"isError":true`))
			Expect(body).To(ContainSubstring("elicitation"))
			Expect(body).NotTo(ContainSubstring("-32042"))
		})

		It("[Happy,URLElicitation] 401 from upstream invalidates cached token and re-triggers elicitation", func() {
			toolName := fmt.Sprintf("%shello_world", urlPrefix)

			By("Initializing with elicitation capability")
			var sessionID string
			Eventually(func(g Gomega) {
				var err error
				sessionID, err = mcpInitializeWithElicitation(ElicitationGatewayURL, nil)
				g.Expect(err).NotTo(HaveOccurred())
			}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

			Expect(mcpNotifyInitialized(context.Background(), ElicitationGatewayURL, sessionID, nil)).To(Succeed())

			By("Waiting for tools to be available")
			Eventually(func(g Gomega) {
				_, tools, err := mcpListTools(context.Background(), ElicitationGatewayURL, sessionID, nil)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(tools).To(ContainElement(toolName))
			}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

			By("Calling tool — should get -32042 (no token yet)")
			_, body, _, err := mcpCallToolRaw(ElicitationGatewayURL, sessionID, toolName, nil, nil)
			Expect(err).NotTo(HaveOccurred())
			sseErr, err := parseSSEError(body)
			Expect(err).NotTo(HaveOccurred())
			Expect(sseErr.Code).To(Equal(-32042))

			By("Submitting the CORRECT token via broker page")
			elicitURL, err := extractElicitationURL(sseErr)
			Expect(err).NotTo(HaveOccurred())
			testURL, err := adaptElicitationURL(elicitURL, ElicitationGatewayURL)
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
				strings.TrimSuffix(ElicitationGatewayURL, "/mcp")+"/tokens",
				formValues,
				nil,
				cookies...,
			)
			Expect(postErr).NotTo(HaveOccurred())
			Expect(postStatus).To(Equal(200))

			By("Calling tool with correct token — should succeed and establish backend session")
			successStatus, successContent, err := mcpCallTool(context.Background(), ElicitationGatewayURL, sessionID, toolName, map[string]any{"name": "setup"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(successStatus).To(Equal(200))
			Expect(successContent).NotTo(BeEmpty())
			Expect(successContent[0].Text).To(ContainSubstring("Hello"))

			By("Calling tool with X-Force-Auth-Reject — upstream returns 401, gateway invalidates token")
			retryStatus, _, _, err := mcpCallToolRaw(ElicitationGatewayURL, sessionID, toolName, map[string]any{"name": "reject"}, map[string]string{"X-Force-Auth-Reject": "true"})
			Expect(err).NotTo(HaveOccurred())
			Expect(retryStatus).To(Equal(401))

			By("Retrying — should get -32042 (token was invalidated)")
			_, body2, _, err := mcpCallToolRaw(ElicitationGatewayURL, sessionID, toolName, map[string]any{"name": "retry"}, nil)
			Expect(err).NotTo(HaveOccurred())
			sseErr2, err := parseSSEError(body2)
			Expect(err).NotTo(HaveOccurred())
			Expect(sseErr2.Code).To(Equal(-32042))

			By("Submitting the correct token again")
			elicitURL2, err := extractElicitationURL(sseErr2)
			Expect(err).NotTo(HaveOccurred())
			testURL2, err := adaptElicitationURL(elicitURL2, ElicitationGatewayURL)
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
				strings.TrimSuffix(ElicitationGatewayURL, "/mcp")+"/tokens",
				formValues2,
				nil,
				cookies2...,
			)
			Expect(postErr2).NotTo(HaveOccurred())
			Expect(postStatus2).To(Equal(200))

			By("Final retry — should succeed with correct token")
			finalStatus, finalContent, err := mcpCallTool(context.Background(), ElicitationGatewayURL, sessionID, toolName, map[string]any{"name": "final"}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(finalStatus).To(Equal(200))
			Expect(finalContent).NotTo(BeEmpty())
			Expect(finalContent[0].Text).To(ContainSubstring("Hello"))
		})
	})
})
