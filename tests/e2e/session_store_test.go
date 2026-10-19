//go:build e2e

package e2e

import (
	"fmt"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
)

const (
	gatewayDeploymentName  = "mcp-gateway"
	sessionStoreSecretName = "redis-session-store"
)

// enableSessionStore points the shared MCPGatewayExtension at the session store and waits for
// the gateway rollout to reach the given replica count. cleanup removes the store and the secret.
func enableSessionStore(replicas int) {
	By("creating or updating the session store secret")
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: sessionStoreSecretName, Namespace: SystemNamespace}}
	err := k8sClient.Get(ctx, client.ObjectKeyFromObject(secret), secret)
	if err != nil && !apierrors.IsNotFound(err) {
		Expect(err).NotTo(HaveOccurred())
	}
	secret.Labels = map[string]string{"mcp.kuadrant.io/secret": "true", "e2e": "test"}
	secret.Type = corev1.SecretTypeOpaque
	secret.Data = map[string][]byte{"CACHE_CONNECTION_STRING": []byte(SessionStoreConnectionString)}
	// a stale secret from an earlier run is updated, not reused as-is
	if apierrors.IsNotFound(err) {
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
	} else {
		Expect(k8sClient.Update(ctx, secret)).To(Succeed())
	}

	DeferCleanup(func() {
		By("Cleanup: removing sessionStore from MCPGatewayExtension")
		Eventually(func(g Gomega) {
			ext := &mcpv1.MCPGatewayExtension{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: MCPExtensionName, Namespace: SystemNamespace}, ext)).To(Succeed())
			ext.Spec.SessionStore = nil
			g.Expect(k8sClient.Update(ctx, ext)).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		Expect(WaitForDeploymentReady(ctx, SystemNamespace, gatewayDeploymentName)).To(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: sessionStoreSecretName, Namespace: SystemNamespace},
		}))).To(Succeed())
	})

	ext := &mcpv1.MCPGatewayExtension{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: MCPExtensionName, Namespace: SystemNamespace}, ext)).To(Succeed())
	if ext.Spec.SessionStore != nil && ext.Spec.SessionStore.SecretName == sessionStoreSecretName {
		// already enabled by a crashed earlier run; no rollout to wait for
		Expect(WaitForDeploymentReady(ctx, SystemNamespace, gatewayDeploymentName)).To(Succeed())
		return
	}

	gen, err := GetDeploymentGeneration(ctx, SystemNamespace, gatewayDeploymentName)
	Expect(err).NotTo(HaveOccurred())

	By("enabling the session store via sessionStore on MCPGatewayExtension")
	Eventually(func(g Gomega) {
		ext := &mcpv1.MCPGatewayExtension{}
		g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: MCPExtensionName, Namespace: SystemNamespace}, ext)).To(Succeed())
		ext.Spec.SessionStore = &mcpv1.SessionStore{SecretName: sessionStoreSecretName}
		g.Expect(k8sClient.Update(ctx, ext)).To(Succeed())
	}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

	By("waiting for gateway rollout after enabling the session store")
	Expect(WaitForDeploymentReplicas(ctx, SystemNamespace, gatewayDeploymentName, replicas, gen)).To(Succeed())
}

// newRawSession initializes a 2025-11-25 gateway session with a raw HTTP client
func newRawSession() string {
	var sessionID string
	Eventually(func(g Gomega) {
		var err error
		sessionID, err = mcpInitialize(ctx, gatewayURL, nil)
		g.Expect(err).NotTo(HaveOccurred())
	}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	Expect(mcpNotifyInitialized(ctx, gatewayURL, sessionID, nil)).To(Succeed())
	return sessionID
}

// backendSessionFor calls a headers tool and returns the backend Mcp-Session-Id it received
func backendSessionFor(sessionID, toolName string) string {
	var backendSessionID string
	Eventually(func(g Gomega) {
		_, content, err := mcpCallTool(ctx, gatewayURL, sessionID, toolName, nil, nil)
		g.Expect(err).NotTo(HaveOccurred())
		backendSessionID = extractBackendSession(content)
		g.Expect(backendSessionID).NotTo(BeEmpty(), "expected backend Mcp-Session-Id in tool response")
	}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	return backendSessionID
}

// gatewayRestartCount sums container restarts across the broker-router pods
func gatewayRestartCount() int32 {
	pods := &corev1.PodList{}
	Expect(k8sClient.List(ctx, pods, client.InNamespace(SystemNamespace),
		client.MatchingLabels{"app.kubernetes.io/name": gatewayDeploymentName})).To(Succeed())
	var restarts int32
	for i := range pods.Items {
		for _, cs := range pods.Items[i].Status.ContainerStatuses {
			restarts += cs.RestartCount
		}
	}
	return restarts
}

// registerHeadersServer registers mcp-test-server2 under prefix and waits for its tools
func registerHeadersServer(testName, prefix string, testResources *[]client.Object) string {
	mcpGatewayClient := newTestGatewayClient()
	registration := NewMCPServerResourcesWithDefaults(testName, k8sClient).WithPrefix(prefix).Build()
	*testResources = append(*testResources, registration.GetObjects()...)
	registeredServer := registration.Register(ctx)

	Eventually(func(g Gomega) {
		g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
	}, TestTimeoutLong, TestRetryInterval).To(Succeed())
	WaitForToolsWithPrefix(ctx, mcpGatewayClient, prefix)
	return prefix + "headers"
}

var _ = Describe("Session store", func() {
	It("[Full] Redis session cache shares backend sessions across gateway replicas", Serial, func() {
		const iterations = 20
		testResources := []client.Object{}
		deferCleanupResources(&testResources)

		By("registering an MCP server")
		prefix := "redisrep_"
		toolName := registerHeadersServer("redis-replicas", prefix, &testResources)

		enableSessionStore(1)

		By("scaling the gateway to 2 replicas")
		gen, err := GetDeploymentGeneration(ctx, SystemNamespace, gatewayDeploymentName)
		Expect(err).NotTo(HaveOccurred())
		Expect(ScaleDeployment(ctx, SystemNamespace, gatewayDeploymentName, 2)).To(Succeed())
		// register scale-down now that the spec is at 2, so cleanup runs on any later failure
		DeferCleanup(func() {
			By("scaling the gateway back to 1 replica")
			downGen, dErr := GetDeploymentGeneration(ctx, SystemNamespace, gatewayDeploymentName)
			Expect(dErr).NotTo(HaveOccurred())
			Expect(ScaleDeployment(ctx, SystemNamespace, gatewayDeploymentName, 1)).To(Succeed())
			Expect(WaitForDeploymentReplicas(ctx, SystemNamespace, gatewayDeploymentName, 1, downGen)).To(Succeed())
		})
		Expect(WaitForDeploymentReplicas(ctx, SystemNamespace, gatewayDeploymentName, 2, gen)).To(Succeed())

		// readiness doesn't guarantee tools are loaded; require consecutive fresh sessions
		// (> 2x replicas) to see the tools so both replicas are warm before the strict loop
		By("warming up: requiring consecutive fresh sessions to list the tools across replicas")
		const warmStreak = 6
		Eventually(func(g Gomega) {
			for range warmStreak {
				sid, err := mcpInitialize(ctx, gatewayURL, nil)
				g.Expect(err).NotTo(HaveOccurred())
				_, names, err := mcpListTools(ctx, gatewayURL, sid)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(names).To(ContainElement(HavePrefix(prefix)))
			}
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		sessionID := newRawSession()
		backendSessionID := backendSessionFor(sessionID, toolName)
		GinkgoWriter.Println("backend session:", backendSessionID)

		By(fmt.Sprintf("calling the tool %d times across replicas with the same gateway session", iterations))
		for i := range iterations {
			status, content, err := mcpCallTool(ctx, gatewayURL, sessionID, toolName, nil, nil)
			Expect(err).NotTo(HaveOccurred(), "iteration %d", i)
			Expect(status).To(Equal(http.StatusOK), "iteration %d", i)
			Expect(extractBackendSession(content)).To(Equal(backendSessionID),
				"iteration %d: every replica must reuse the backend session stored in redis", i)
		}
	})

	It("[Full] Backend restart invalidates the cached backend session and the next tool call re-initializes", Serial, func() {
		const backendDeployment = "mcp-test-server2"
		testResources := []client.Object{}
		deferCleanupResources(&testResources)

		By("registering an MCP server")
		toolName := registerHeadersServer("backend-restart", "bkrestart_", &testResources)

		sessionID := newRawSession()
		before := backendSessionFor(sessionID, toolName)
		GinkgoWriter.Println("backend session before restart:", before)

		By("restarting the backend MCP server")
		Expect(RestartDeploymentAndWait(ctx, TestServerNameSpace, backendDeployment)).To(Succeed())

		By("expecting a 404 for the backend session the restarted server no longer knows")
		Eventually(func(g Gomega) {
			status, _, _ := mcpCallTool(ctx, gatewayURL, sessionID, toolName, nil, nil)
			g.Expect(status).To(Equal(http.StatusNotFound))
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("expecting the next call to initialize a new backend session")
		after := backendSessionFor(sessionID, toolName)
		GinkgoWriter.Println("backend session after restart:", after)
		Expect(after).NotTo(Equal(before), "a new backend session must be created after the 404")

		By("expecting later calls to reuse the new backend session")
		Expect(backendSessionFor(sessionID, toolName)).To(Equal(after))
	})

	It("[Full] Redis outage fails tool calls and recovers without a gateway restart", Serial, func() {
		testResources := []client.Object{}
		deferCleanupResources(&testResources)

		By("registering an MCP server")
		toolName := registerHeadersServer("redis-outage", "redisout_", &testResources)

		enableSessionStore(1)

		sessionID := newRawSession()
		Expect(backendSessionFor(sessionID, toolName)).NotTo(BeEmpty())
		restarts := gatewayRestartCount()

		By("taking the session store down")
		// register the restore before scaling down, so cleanup runs on any later failure
		DeferCleanup(func() {
			By("Cleanup: restoring the session store")
			Expect(ScaleDeployment(ctx, SessionStoreNamespace, SessionStoreDeployment, 1)).To(Succeed())
			Expect(WaitForDeploymentReady(ctx, SessionStoreNamespace, SessionStoreDeployment)).To(Succeed())
		})
		Expect(ScaleDeployment(ctx, SessionStoreNamespace, SessionStoreDeployment, 0)).To(Succeed())

		By("expecting tools/call to fail with 500 while the store is unreachable")
		Eventually(func(g Gomega) {
			status, _, err := mcpCallTool(ctx, gatewayURL, sessionID, toolName, nil, nil)
			g.Expect(status).To(Equal(http.StatusInternalServerError))
			g.Expect(err).To(MatchError(ContainSubstring("internal error")))
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("expecting initialize to keep working without the store")
		_, err := mcpInitialize(ctx, gatewayURL, nil)
		Expect(err).NotTo(HaveOccurred())

		By("restoring the session store")
		Expect(ScaleDeployment(ctx, SessionStoreNamespace, SessionStoreDeployment, 1)).To(Succeed())
		Expect(WaitForDeploymentReady(ctx, SessionStoreNamespace, SessionStoreDeployment)).To(Succeed())

		By("expecting the same gateway session to work again without restarting the gateway")
		// the store may come back empty, so a new backend session is acceptable
		Expect(backendSessionFor(sessionID, toolName)).NotTo(BeEmpty())
		Expect(gatewayRestartCount()).To(Equal(restarts), "gateway pods must reconnect without restarting")
	})
})
