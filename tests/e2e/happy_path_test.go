//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var gatewayURL = "http://localhost:8001/mcp"

// these can be used across many tests
var sharedMCPTestServer1 = "mcp-test-server1"
var sharedMCPTestServer2 = "mcp-test-server2"

// this should only be used by one test as the tests run in parallel.
var scaledMCPTestServer = "mcp-test-server3"

var _ = Describe("MCP Gateway Registration Happy Path", func() {
	var (
		testResources = []client.Object{}
	)

	BeforeEach(func() {
		// we don't use defers for this so if a test fails ensure this server that gets scaled down and up is up and running
		_ = ScaleDeployment(TestNamespace, scaledMCPTestServer, 1)
	})

	AfterEach(func() {
		// cleanup in reverse order
		for _, to := range testResources {
			CleanupResource(ctx, k8sClient, to)
		}
	})

	JustAfterEach(func() {
		// dump logs if test failed
		if CurrentSpecReport().Failed() {
			GinkgoWriter.Println("failure detected")

			//	DumpComponentLogs()
			//	DumpTestServerLogs()
		}
	})

	It("should register multiple mcp servers with the gateway and make their tools available", func() {
		By("Creating HTTPRoutes and MCP Servers")
		// create httproutes for test servers that should already be deployed
		registration := NewMCPServerRegistrationWithDefaults("basic-registration", k8sClient)
		// Important as we need to make sure to clean up
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer1 := registration.Register(ctx)
		registration = NewMCPServerRegistrationWithDefaults("basic-registration", k8sClient)
		// Important as we need to make sure to clean up
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer2 := registration.Register(ctx)

		By("Verifying MCPServers become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerReadyWithToolsCount(ctx, k8sClient, registeredServer1.Name, registeredServer1.Namespace, 5)).To(BeNil())
			g.Expect(VerifyMCPServerReadyWithToolsCount(ctx, k8sClient, registeredServer2.Name, registeredServer2.Namespace, 5)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying MCPServers tools are present")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerToolsPresent(registeredServer1.Spec.ToolPrefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer1.Spec.ToolPrefix))
			g.Expect(verifyMCPServerToolsPresent(registeredServer2.Spec.ToolPrefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer2.Spec.ToolPrefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

	})

	It("should unregister mcp servers with the gateway", func() {
		registration := NewMCPServerRegistrationWithDefaults("basic-unregister", k8sClient)
		// Important as we need to make sure to clean up
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("ensuring the tools are present in the gateway")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerToolsPresent(registeredServer.Spec.ToolPrefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer.Spec.ToolPrefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("unregistering an MCPServer by Deleting the resource")
		Expect(k8sClient.Delete(ctx, registeredServer)).To(Succeed())

		By("Verifying broker removes the deleted server")
		// do tools call check tools no longer present
		Eventually(func(g Gomega) {
			err := VerifyMCPServerReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)
			g.Expect(err).NotTo(BeNil())
			g.Expect(err.Error()).Should(ContainSubstring("not found"))
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerToolsPresent(registeredServer.Spec.ToolPrefix, toolsList)).To(BeFalse())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	It("should invoke tools successfully", func() {
		registration := NewMCPServerRegistrationWithDefaults("tools-invoke", k8sClient)
		// Important as we need to make sure to clean up
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying MCPServers tools are present")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerToolsPresent(registeredServer.Spec.ToolPrefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer.Spec.ToolPrefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		toolName := fmt.Sprintf("%s%s", registeredServer.Spec.ToolPrefix, "hello_world")
		GinkgoWriter.Println("tool", toolName)
		By("Invoking a tool")
		res, err := mcpGatewayClient.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: toolName, Arguments: map[string]string{
				"name": "e2e",
			}},
		})
		Expect(err).Error().NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(len(res.Content)).To(BeNumerically("==", 1))
		content, ok := res.Content[0].(mcp.TextContent)
		Expect(ok).To(BeTrue())
		Expect(content.Text).To(Equal("Hello, e2e!"))
	})

	It("should register mcp server with credential with the gateway and make the tools available", func() {
		cred := BuildCredentialSecret("mcp-credential", "test-api-key-secret-toke")
		registration := NewMCPServerRegistrationWithDefaults("credentials", k8sClient).
			WithCredential(cred, "token").WithBackendTarget("mcp-api-key-server", 9090)
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)
		By("ensuring broker has failed authentication and the mcp server is not registered and the tools don't exist")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).Error().To(Not(BeNil()))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying MCPServers tools are not present")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerToolsPresent(registeredServer.Spec.ToolPrefix, toolsList)).To(BeFalseBecause("%s should NOT exist", registeredServer.Spec.ToolPrefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("updating the secret to a valid value the server should be registered and the tools should exist")
		patch := client.MergeFrom(cred.DeepCopy())
		cred.StringData = map[string]string{
			"token": "Bearer test-api-key-secret-token",
		}
		Expect(k8sClient.Patch(ctx, cred, patch)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).Error().To(BeNil())
		}, TestTimeoutConfigSync, TestRetryInterval).To(Succeed())

		By("Verifying MCPServers tools are present")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerToolsPresent(registeredServer.Spec.ToolPrefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer.Spec.ToolPrefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

	})

	It("should use and re-use a backend MCP session", func() {

		registration := NewMCPServerRegistrationWithDefaults("sessions", k8sClient)
		// Important as we need to make sure to clean up
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("creating a new client")
		mcpClient, err := NewMCPGatewayClient(context.Background(), gatewayURL)
		Expect(err).Error().NotTo(HaveOccurred())
		clientSession := mcpClient.GetSessionId()
		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
		By("Ensuring the gateway has the tools")
		Eventually(func(g Gomega) {
			toolsList, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerToolsPresent(registeredServer.Spec.ToolPrefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer.Spec.ToolPrefix))
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		toolName := fmt.Sprintf("%s%s", registeredServer.Spec.ToolPrefix, "headers")
		By("Invoking a tool")
		res, err := mcpClient.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: toolName},
		})
		Expect(err).Error().NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		mcpsessionid := ""
		for _, cont := range res.Content {
			textContent, ok := cont.(mcp.TextContent)
			Expect(ok).To(BeTrue())
			if strings.HasPrefix(textContent.Text, "Mcp-Session-Id") {
				GinkgoWriter.Println(textContent.Text)
				mcpsessionid = textContent.Text
			}
		}
		Expect(mcpsessionid).To(ContainSubstring("Mcp-Session-Id"))

		By("Invoking the headers tool again")
		res, err = mcpClient.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: toolName},
		})
		Expect(err).Error().NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		for _, cont := range res.Content {
			textContent, ok := cont.(mcp.TextContent)
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
		res, err = mcpClient.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: toolName},
		})
		Expect(err).Error().NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		for _, cont := range res.Content {
			textContent, ok := cont.(mcp.TextContent)
			Expect(ok).To(BeTrue())
			if strings.HasPrefix(textContent.Text, "Mcp-Session-Id") {
				GinkgoWriter.Println(textContent.Text)
				Expect(textContent.Text).To(ContainSubstring("Mcp-Session-Id"))
				Expect(mcpsessionid).To(Not(Equal(textContent.Text)))
				Expect(textContent.Text).To(Not(ContainSubstring(mcpClient.GetSessionId())))
			}
		}
	})

	It("should deploy redis and scale up the broker and see sessions shared", func() {
		Skip("not implemented")
	})

	It("should assign unique mcp-session-ids to concurrent clients and new session on reconnect", func() {
		By("Creating multiple clients concurrently")
		client1, err := NewMCPGatewayClient(ctx, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		defer client1.Close()

		client2, err := NewMCPGatewayClient(ctx, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		defer client2.Close()

		client3, err := NewMCPGatewayClient(ctx, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		defer client3.Close()

		By("Verifying all clients have unique session IDs")
		session1 := client1.GetSessionId()
		session2 := client2.GetSessionId()
		session3 := client3.GetSessionId()

		Expect(session1).NotTo(BeEmpty(), "client1 should have a session ID")
		Expect(session2).NotTo(BeEmpty(), "client2 should have a session ID")
		Expect(session3).NotTo(BeEmpty(), "client3 should have a session ID")

		Expect(session1).NotTo(Equal(session2), "client1 and client2 should have different session IDs")
		Expect(session1).NotTo(Equal(session3), "client1 and client3 should have different session IDs")
		Expect(session2).NotTo(Equal(session3), "client2 and client3 should have different session IDs")

		By("Disconnecting client1 and reconnecting")
		Expect(client1.Close()).To(Succeed())

		reconnectedClient, err := NewMCPGatewayClient(ctx, gatewayURL)
		Expect(err).NotTo(HaveOccurred())
		defer reconnectedClient.Close()

		newSession := reconnectedClient.GetSessionId()
		Expect(newSession).NotTo(BeEmpty(), "reconnected client should have a session ID")
		Expect(newSession).NotTo(Equal(session1), "reconnected client should have a different session ID than before")
		Expect(newSession).NotTo(Equal(session2), "reconnected client should have a different session ID than client2")
		Expect(newSession).NotTo(Equal(session3), "reconnected client should have a different session ID than client3")
	})

	It("should only return tools specified by MCPVirtualServer when using X-Mcp-Virtualserver header", func() {
		By("Creating an MCPServer with tools")
		registration := NewMCPServerRegistrationWithDefaults("virtualserver-test", k8sClient)
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying MCPServer tools are present")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerToolsPresent(registeredServer.Spec.ToolPrefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer.Spec.ToolPrefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating an MCPVirtualServer with a subset of tools")
		allowedTool := fmt.Sprintf("%s%s", registeredServer.Spec.ToolPrefix, "hello_world")
		virtualServer := BuildTestMCPVirtualServer("test-virtualserver", TestNamespace, []string{allowedTool}).Build()
		testResources = append(testResources, virtualServer)
		Expect(k8sClient.Create(ctx, virtualServer)).To(Succeed())

		By("Creating a client with X-Mcp-Virtualserver header")
		virtualServerHeader := fmt.Sprintf("%s/%s", virtualServer.Namespace, virtualServer.Name)
		virtualServerClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"X-Mcp-Virtualserver": virtualServerHeader,
		})
		Expect(err).NotTo(HaveOccurred())
		defer virtualServerClient.Close()

		By("Verifying only the tools from MCPVirtualServer are returned")
		Eventually(func(g Gomega) {
			filteredTools, err := virtualServerClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(filteredTools).NotTo(BeNil())
			g.Expect(len(filteredTools.Tools)).To(Equal(1), "expected exactly 1 tool from virtual server")
			g.Expect(filteredTools.Tools[0].Name).To(Equal(allowedTool))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying the original client without header still sees all tools")
		allToolsAgain, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(len(allToolsAgain.Tools)).To(BeNumerically(">", 1), "expected more than 1 tool without virtual server header")
	})

	It("should send notifications/tools/list_changed to connected clients when MCPServer is registered", func() {
		// NOTE on notifications. A notification is sent when servers are removed during clean up as this effects tools list also.
		// as the list_changed notification is broadcast, this can mean clients in other tests receive additional notifications
		// for that reason we only assert we received at least one rather than a set number
		By("Creating clients with notification handlers and different sessions")
		client1Notification := false
		client1, err := NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(j mcp.JSONRPCNotification) {
			GinkgoWriter.Println("client 1 received notification registration", j.Method)
			client1Notification = true
		})
		Expect(err).NotTo(HaveOccurred())
		defer client1.Close()

		client2Notification := false
		client2, err := NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(j mcp.JSONRPCNotification) {
			GinkgoWriter.Println("client 2 received notification registration", j.Method)
			client2Notification = true
		})
		Expect(err).NotTo(HaveOccurred())
		defer client2.Close()
		Expect(mcpGatewayClient.sessionID).NotTo(BeEmpty())
		Expect(client2.sessionID).NotTo(BeEmpty())
		Expect(client1.sessionID).NotTo(Equal(client2.sessionID))

		By("registering a new MCPServer")
		registration := NewMCPServerRegistrationWithDefaults("notification-test", k8sClient)
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Waiting for the server to become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		// We do this to wait for the tools to show up as we know then that the gateway has done its work
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerToolsPresent(registeredServer.Spec.ToolPrefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer.Spec.ToolPrefix))
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying both clients received notifications/tools/list_changed within 1 minutes")
		Eventually(func(g Gomega) {
			_, err := client1.ListTools(ctx, mcp.ListToolsRequest{})
			Expect(err).NotTo(HaveOccurred())
			g.Expect(client1Notification).To(BeTrue(), "client1 should have received a notification within 1 minutes")
			g.Expect(client2Notification).To(BeTrue(), "client1 should have received a notification within 1 minutes")
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())
	})

	It("should forward notifications/tools/list_changed from backend MCP server to connected clients", func() {

		By("Creating an MCPServer pointing to server1 which has the add_tool feature")
		registration := NewMCPServerRegistrationWithDefaults("backend-notification-test", k8sClient).
			WithBackendTarget(sharedMCPTestServer1, 9090)
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying initial tools are present")
		var initialToolCount int
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerToolsPresent(registeredServer.Spec.ToolPrefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer.Spec.ToolPrefix))
			initialToolCount = len(toolsList.Tools)
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating new clients with notification handlers")
		client1Notification := false
		client1, err := NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(j mcp.JSONRPCNotification) {
			if j.Method == "notifications/tools/list_changed" {
				GinkgoWriter.Println("client 1 received notification", j.Method)
				client1Notification = true
			}
		})
		Expect(err).NotTo(HaveOccurred())
		defer client1.Close()

		client2Notification := false
		client2, err := NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(j mcp.JSONRPCNotification) {
			GinkgoWriter.Println("client 2 received notification", j.Method)
			if j.Method == "notifications/tools/list_changed" {
				client2Notification = true
			}
		})
		Expect(err).NotTo(HaveOccurred())
		defer client2.Close()

		By("Calling add_tool on the backend server to trigger notifications/tools/list_changed")
		dynamicToolName := fmt.Sprintf("dynamic_tool_%s", UniqueName(""))
		addToolName := fmt.Sprintf("%s%s", registeredServer.Spec.ToolPrefix, "add_tool")
		res, err := client1.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name: addToolName,
				Arguments: map[string]string{
					"name":        dynamicToolName,
					"description": "A dynamically added tool for testing notifications",
				},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		GinkgoWriter.Println("add_tool response:", res.Content)

		By("Verifying both clients received notifications/tools/list_changed")
		Eventually(func(g Gomega) {
			g.Expect(client1Notification).To(BeTrue(), "client1 should have received notifications/tools/list_changed")
			g.Expect(client2Notification).To(BeTrue(), "client2 should have received notifications/tools/list_changed")
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying tools/list now includes the new dynamically added tool")
		Eventually(func(g Gomega) {
			toolsList, err := client1.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(len(toolsList.Tools)).To(BeNumerically("==", initialToolCount+1), "tools list should have increased by one")

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
	It("should gracefully handle an MCP Server becoming unavailable", func() {
		By("Scaling down the MCP server3 deployment to 0")
		Expect(ScaleDeployment(TestNamespace, scaledMCPTestServer, 0)).To(Succeed())

		By("Registering an MCPServer pointing to server3")
		registration := NewMCPServerRegistrationWithDefaults("unavailable-test", k8sClient).
			WithBackendTarget(scaledMCPTestServer, 9090)
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServer status reports connection failure")
		Eventually(func(g Gomega) {
			err := VerifyMCPServerNotReadyWithReason(ctx, k8sClient,
				registeredServer.Name, registeredServer.Namespace, "connection refused")
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Scaling up the MCP server3 deployment to 1")
		Expect(ScaleDeployment(TestNamespace, scaledMCPTestServer, 1)).To(Succeed())

		By("Waiting for deployment to be ready")
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(TestNamespace, scaledMCPTestServer, 1)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying tools are now present")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerToolsPresent(registeredServer.Spec.ToolPrefix, toolsList)).To(BeTrueBecause("%s should exist", registeredServer.Spec.ToolPrefix))
		}, TestTimeoutConfigSync, TestRetryInterval).To(Succeed())

		By("Creating a client with notification handler")
		receivedNotification := false
		notifyClient, err := NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(j mcp.JSONRPCNotification) {
			if j.Method == "notifications/tools/list_changed" {
				GinkgoWriter.Println("received notification during unavailability test", j.Method)
				receivedNotification = true
			}
		})
		Expect(err).NotTo(HaveOccurred())
		defer notifyClient.Close()

		By("Scaling back down the MCP server3 deployment to 0")
		Expect(ScaleDeployment(TestNamespace, scaledMCPTestServer, 0)).To(Succeed())

		By("Verifying tools are removed from tools/list within timeout")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerToolsPresent(registeredServer.Spec.ToolPrefix, toolsList)).To(BeFalseBecause("%s should be removed when server unavailable", registeredServer.Spec.ToolPrefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying client notification was received")
		Eventually(func(g Gomega) {
			g.Expect(receivedNotification).To(BeTrue(), "should have received notifications/tools/list_changed")
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())

		By("Verifying tool call returns error when server unavailable")
		toolName := fmt.Sprintf("%s%s", registeredServer.Spec.ToolPrefix, "time")
		_, err = mcpGatewayClient.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: toolName},
		})

		By("Scaling the MCP server deployment back up")
		Expect(ScaleDeployment(TestNamespace, scaledMCPTestServer, 1)).To(Succeed())

		By("Waiting for deployment to be ready")
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(TestNamespace, scaledMCPTestServer, 1)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying tools are restored in tools/list")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerToolsPresent(registeredServer.Spec.ToolPrefix, toolsList)).To(BeTrueBecause("%s should be restored when server available", registeredServer.Spec.ToolPrefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying tool call work when server back available")
		toolName = fmt.Sprintf("%s%s", registeredServer.Spec.ToolPrefix, "time")
		_, err = mcpGatewayClient.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: toolName},
		})
		Expect(err).NotTo(HaveOccurred(), "tool calls should work once the server is back and ready")
	})

	It("should filter tools based on x-authorized-tools JWT header", func() {
		if !IsTrustedHeadersEnabled() {
			Skip("trusted headers public key not configured - skipping x-authorized-tools test")
		}

		By("Creating an MCPServer with tools")
		registration := NewMCPServerRegistrationWithDefaults("authorized-tools-test", k8sClient)
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Ensuring the gateway has registered the server")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying MCPServer tools are present without filtering")
		var allTools *mcp.ListToolsResult
		Eventually(func(g Gomega) {
			var err error
			allTools, err = mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(allTools).NotTo(BeNil())
			g.Expect(verifyMCPServerToolsPresent(registeredServer.Spec.ToolPrefix, allTools)).To(BeTrueBecause("%s should exist", registeredServer.Spec.ToolPrefix))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating a JWT with allowed tools for the server")

		GinkgoWriter.Println("server name ", registeredServer.Name)

		allowedTools := map[string][]string{
			fmt.Sprintf("%s/%s", registeredServer.Namespace, registeredServer.Name): {"hello_world"},
		}
		jwtToken, err := CreateAuthorizedToolsJWT(allowedTools)
		Expect(err).NotTo(HaveOccurred())

		By("Creating a client with x-authorized-tools header")
		authorizedClient, err := NewMCPGatewayClientWithHeaders(ctx, gatewayURL, map[string]string{
			"X-Authorized-Tools": jwtToken,
		})
		Expect(err).NotTo(HaveOccurred())
		defer authorizedClient.Close()

		By("Verifying only the tools from the JWT are returned")
		Eventually(func(g Gomega) {
			filteredTools, err := authorizedClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(filteredTools).NotTo(BeNil())
			g.Expect(len(filteredTools.Tools)).To(Equal(1), "expected exactly 1 tool from authorized tools header")
			expectedToolName := fmt.Sprintf("%s%s", registeredServer.Spec.ToolPrefix, "hello_world")
			g.Expect(filteredTools.Tools[0].Name).To(Equal(expectedToolName))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	It("should register MCP server via Hostname backendRef", func() {
		// verifies controller correctly handles Hostname backendRef
		// broker connectivity not tested - test server doesn't speak HTTPS
		externalHost := "mcp-test-server2.mcp-test.svc.cluster.local"
		port := int32(9090)

		By("Creating ServiceEntry, DestinationRule, HTTPRoute with Hostname backendRef, and MCPServer")
		registration := NewExternalMCPServerRegistration("hostname-backend", k8sClient, externalHost, port)
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying controller processed MCPServer (has status condition)")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerHasCondition(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutMedium, TestRetryInterval).To(Succeed())
	})

	It("should report invalid protocol version in MCPServer status", func() {
		By("Creating an MCPServer pointing to the broken server with wrong protocol version")
		// The broken server is already deployed with --failure-mode=protocol
		registration := NewMCPServerRegistrationWithDefaults("protocol-status-test", k8sClient).
			WithBackendTarget("mcp-test-broken-server", 9090)
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServer status reports protocol version failure")
		Eventually(func(g Gomega) {
			err := VerifyMCPServerNotReadyWithReason(ctx, k8sClient,
				registeredServer.Name, registeredServer.Namespace, "unsupported protocol version")
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying the status message contains details about the protocol issue")
		msg, err := GetMCPServerStatusMessage(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)
		Expect(err).NotTo(HaveOccurred())
		GinkgoWriter.Println("MCPServer status message:", msg)
		Expect(msg).To(ContainSubstring("unsupported protocol version"))
	})

	It("should report tool conflicts in MCPServer status when same prefix is used", func() {

		By("Creating first MCPServer with a specific prefix")
		registration1 := NewMCPServerRegistrationWithDefaults("conflict-test-1", k8sClient).
			WithToolPrefix("conflict_")
		testResources = append(testResources, registration1.GetObjects()...)
		server1 := registration1.Register(ctx)

		By("Ensuring first server becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Creating second MCPServer with the SAME prefix pointing to server2")
		registration2 := NewMCPServerRegistrationWithDefaults("conflict-test-2", k8sClient).
			WithToolPrefix("conflict_")
		testResources = append(testResources, registration2.GetObjects()...)
		server2 := registration2.Register(ctx)

		By("Verifying at least one MCPServer reports tool conflict in status")
		// Both servers have tools like "time", "headers" etc.
		// With the same prefix, they would produce "conflict_time", "conflict_headers" etc.
		// At least one server should report a conflict.
		Eventually(func(g Gomega) {
			// Check if either server reports a conflict
			msg1, err1 := GetMCPServerStatusMessage(ctx, k8sClient, server1.Name, server1.Namespace)
			msg2, err2 := GetMCPServerStatusMessage(ctx, k8sClient, server2.Name, server2.Namespace)

			g.Expect(err1).NotTo(HaveOccurred())
			g.Expect(err2).NotTo(HaveOccurred())

			GinkgoWriter.Println("Server1 status:", msg1)
			GinkgoWriter.Println("Server2 status:", msg2)

			// At least one should contain conflict-related message
			hasConflict := strings.Contains(msg1, "conflict") || strings.Contains(msg2, "conflict") ||
				strings.Contains(msg1, "conflicts exists") || strings.Contains(msg2, "conflicts exists")
			g.Expect(hasConflict).To(BeTrue(), "expected at least one server to report tool conflict")
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	})

	It("should allow multiple MCP Servers without prefixes", func() {
		By("Creating HTTPRoutes and MCP Servers")
		// create httproutes for test servers that should already be deployed
		registration := NewMCPServerRegistration("same-prefix", "everything-server.mcp.local", "everything-server", 9090, k8sClient).
			WithToolPrefix("")
		// Important as we need to make sure to clean up
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer1 := registration.Register(ctx)

		// This server has a 'hello_world' tool
		registration = NewMCPServerRegistration("same-prefix", "e2e-server2.mcp.local", "mcp-test-server2", 9090, k8sClient).
			WithToolPrefix("")
		// Important as we need to make sure to clean up
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer2 := registration.Register(ctx)

		By("Verifying MCPServers become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerReady(ctx, k8sClient, registeredServer1.Name, registeredServer1.Namespace)).To(BeNil())
			g.Expect(VerifyMCPServerReady(ctx, k8sClient, registeredServer2.Name, registeredServer2.Namespace)).To(BeNil())
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		By("Verifying expected tools are present")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).Error().NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerToolPresent("echo", toolsList)).To(BeTrueBecause("%q should exist", "greet"))
			g.Expect(verifyMCPServerToolPresent("hello_world", toolsList)).To(BeTrueBecause("%q should exist", "hello_world"))
		}, TestTimeoutLong, TestRetryInterval).To(Succeed())

		toolName := "hello_world"
		By("Invoking the first tool")
		res, err := mcpGatewayClient.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: toolName, Arguments: map[string]string{
				"name": "e2e",
			}},
		})
		Expect(err).Error().NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(len(res.Content)).To(BeNumerically("==", 1))
		content, ok := res.Content[0].(mcp.TextContent)
		Expect(ok).To(BeTrue())
		Expect(content.Text).To(Equal("Hello, e2e!"))

		toolName = "echo"
		By("Invoking the second tool")
		res, err = mcpGatewayClient.CallTool(ctx, mcp.CallToolRequest{
			Params: mcp.CallToolParams{Name: toolName, Arguments: map[string]string{
				"message": "e2e",
			}},
		})
		Expect(err).Error().NotTo(HaveOccurred())
		Expect(res).NotTo(BeNil())
		Expect(len(res.Content)).To(BeNumerically("==", 1))
		content, ok = res.Content[0].(mcp.TextContent)
		Expect(ok).To(BeTrue())
		Expect(content.Text).To(Equal("Echo: e2e"))
	})
})
