//go:build e2e

package e2e

import (
	"context"
	"fmt"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// verifyRedisSessionPersistence enables the session store from a secret built
// from secretData and checks a backend session survives a gateway restart.
func verifyRedisSessionPersistence(redisSecretName, registrationName string, secretData map[string]string) {
	testResources := []client.Object{}
	deferCleanupResources(&testResources)
	mcpGatewayClient := newTestGatewayClient()

	deploymentName := "mcp-gateway"

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
		Type:       corev1.SecretTypeOpaque,
		StringData: secretData,
	}
	Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, redisSecret.DeepCopy()))).To(Succeed())
	Expect(k8sClient.Create(ctx, redisSecret)).To(Succeed())

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
	registration := NewMCPServerResourcesWithDefaults(registrationName, k8sClient).WithPrefix("redis_").Build()
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
}

// deployRedisSentinel runs a single Sentinel (quorum 1) monitoring the CI Redis
// in the system namespace and returns its address. Sentinel announces the
// Redis service hostname, so the broker connects to the same Redis as the
// standalone test.
func deployRedisSentinel(ctx context.Context, name, master string) string {
	labels := map[string]string{"app": name, "e2e": "test"}
	conf := fmt.Sprintf("port 26379\nsentinel resolve-hostnames yes\nsentinel announce-hostnames yes\nsentinel monitor %s redis.%s.svc.cluster.local 6379 1\n", master, SystemNamespace)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: SystemNamespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:            "sentinel",
						Image:           "mirror.gcr.io/redis:7-alpine",
						ImagePullPolicy: corev1.PullIfNotPresent,
						// sentinel rewrites its config, so it must live on a writable path
						Command: []string{"sh", "-c", fmt.Sprintf("printf '%s' > /tmp/sentinel.conf && exec redis-sentinel /tmp/sentinel.conf", conf)},
						Ports:   []corev1.ContainerPort{{Name: "sentinel", ContainerPort: 26379}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
								Command: []string{"redis-cli", "-p", "26379", "sentinel", "get-master-addr-by-name", master},
							}},
							PeriodSeconds: 2,
						},
					}},
				},
			},
		},
	}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: SystemNamespace, Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    []corev1.ServicePort{{Name: "sentinel", Port: 26379, TargetPort: intstr.FromInt32(26379)}},
		},
	}

	for _, obj := range []client.Object{deployment, service} {
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj.DeepCopyObject().(client.Object)))).To(Succeed())
		Eventually(func() error {
			return k8sClient.Create(ctx, obj.DeepCopyObject().(client.Object))
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	}
	DeferCleanup(func() {
		for _, obj := range []client.Object{deployment, service} {
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, obj))).To(Succeed())
		}
	})
	Expect(WaitForDeploymentReady(ctx, SystemNamespace, name)).To(Succeed())
	return fmt.Sprintf("%s.%s.svc.cluster.local:26379", name, SystemNamespace)
}
