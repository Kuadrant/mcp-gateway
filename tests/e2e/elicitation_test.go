//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// acceptHandler returns an elicitation handler that accepts with provided content
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

// declineHandler returns an elicitation handler that declines
type declineHandler struct{}

func (h *declineHandler) Elicit(_ context.Context, req mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	GinkgoWriter.Printf("elicitation request received (declining): %s\n", req.Params.Message)
	return &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{
			Action: mcp.ElicitationResponseActionDecline,
		},
	}, nil
}

var _ = Describe("Elicitation", func() {
	var (
		testResources []client.Object
		prefix        string
	)

	BeforeEach(func() {
		By("Registering an MCPServerRegistration pointing to the everything-server")
		registration := NewMCPServerResources("elicitation", "everything-server.mcp.local", "everything-server", 9090, k8sClient).
			WithPrefix("es_").Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)
		prefix = registeredServer.Spec.Prefix

		By("Waiting for the server to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	AfterEach(func() {
		for _, to := range testResources {
			CleanupResource(ctx, k8sClient, to)
		}
		testResources = nil
	})

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
			elicitClient, err = NewMCPGatewayClientWithElicitation(ctx, gatewayURL, handler)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer elicitClient.Close()

		By("Verifying the trigger-elicitation-request tool is visible")
		Eventually(func(g Gomega) {
			toolsList, err := elicitClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolPresent(toolName, toolsList)).To(BeTrueBecause("%s should exist", toolName))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Calling trigger-elicitation-request tool")
		res, err := elicitClient.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: toolName, Arguments: map[string]any{}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(len(res.Content)).To(BeNumerically(">=", 1))

		By("Verifying the response indicates the user provided information")
		var responseText string
		for _, c := range res.Content {
			tc, ok := c.(mcp.TextContent)
			if ok {
				responseText += tc.Text
			}
		}
		GinkgoWriter.Println("accept response:", responseText)
		Expect(responseText).To(ContainSubstring("User provided the requested information"))
	})

	It("[Elicitation] should decline elicitation", func() {
		toolName := fmt.Sprintf("%strigger-elicitation-request", prefix)

		handler := &declineHandler{}

		var elicitClient *mcpclient.Client
		Eventually(func(g Gomega) {
			var err error
			elicitClient, err = NewMCPGatewayClientWithElicitation(ctx, gatewayURL, handler)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer elicitClient.Close()

		By("Verifying the trigger-elicitation-request tool is visible")
		Eventually(func(g Gomega) {
			toolsList, err := elicitClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolPresent(toolName, toolsList)).To(BeTrueBecause("%s should exist", toolName))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Calling trigger-elicitation-request tool")
		res, err := elicitClient.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: toolName, Arguments: map[string]any{}},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(len(res.Content)).To(BeNumerically(">=", 1))

		By("Verifying the response indicates the user declined")
		var responseText string
		for _, c := range res.Content {
			tc, ok := c.(mcp.TextContent)
			if ok {
				responseText += tc.Text
			}
		}
		GinkgoWriter.Println("decline response:", responseText)
		Expect(responseText).To(ContainSubstring("User declined"))
	})

	It("[Elicitation] should error when calling elicitation tool without handler", func() {
		toolName := fmt.Sprintf("%strigger-elicitation-request", prefix)

		By("Creating a standard client without elicitation handler")
		var standardClient *mcpclient.Client
		Eventually(func(g Gomega) {
			var err error
			standardClient, err = NewMCPGatewayClient(ctx, gatewayURL)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer standardClient.Close()

		By("Verifying the trigger-elicitation-request tool is visible in tools/list")
		toolFound := false
		Eventually(func(g Gomega) {
			toolsList, err := standardClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			for _, t := range toolsList.Tools {
				if strings.HasSuffix(t.Name, "trigger-elicitation-request") {
					toolFound = true
				}
			}
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Calling trigger-elicitation-request tool should error")
		res, err := standardClient.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: toolName, Arguments: map[string]any{}},
		})
		// the tool call may return an error or a result with isError set
		if err != nil {
			GinkgoWriter.Println("tool call error (expected):", err)
			return
		}
		// if no transport error, the result should indicate an error
		Expect(res).NotTo(BeNil())
		GinkgoWriter.Println("no-handler response:", res.Content)
		if toolFound {
			// the tool is visible because the broker declares elicitation capability,
			// but the client doesn't have a handler so the elicitation will fail
			Expect(res.IsError || err != nil).To(BeTrue(),
				"calling elicitation tool without handler should produce an error")
		}
	})
})
