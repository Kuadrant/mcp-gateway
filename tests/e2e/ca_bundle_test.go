//go:build e2e

package e2e

import (
	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Gateway CA Certificate Bundle Configuration", Ordered, func() {
	var (
		testResources    []client.Object
		mcpGatewayClient *NotifyingMCPClient
		caCertPEM        []byte
		caSecretName     = "e2e-gateway-ca-bundle"
	)

	BeforeAll(func() {
		By("Checking cert-manager is installed")
		probe := &unstructured.UnstructuredList{}
		probe.SetGroupVersionKind(schema.GroupVersionKind{
			Group: "cert-manager.io", Version: "v1", Kind: "ClusterIssuerList",
		})
		if err := k8sClient.List(ctx, probe); err != nil {
			Skip("cert-manager not installed - skipping Gateway CA Bundle tests")
		}

		By("Checking TLS test server is deployed")
		deploy := &appsv1.Deployment{}
		if err := k8sClient.Get(ctx, types.NamespacedName{
			Name: tlsServerName, Namespace: TestServerNameSpace,
		}, deploy); err != nil {
			Skip("TLS test server not deployed (run 'make deploy-tls-test-server') - skipping Gateway CA Bundle tests")
		}

		By("Extracting CA cert from cert-manager secret")
		caSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: caKeypairSecret, Namespace: certManagerNS,
		}, caSecret)).To(Succeed())
		var ok bool
		caCertPEM, ok = caSecret.Data["ca.crt"]
		Expect(ok).To(BeTrue(), "cert-manager CA secret should have ca.crt key")
		Expect(caCertPEM).NotTo(BeEmpty())
	})

	BeforeEach(func() {
		testResources = []client.Object{}
		Eventually(func(g Gomega) {
			var err error
			mcpGatewayClient, err = NewMCPGatewayClientWithNotifications(ctx, gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	AfterEach(func() {
		if mcpGatewayClient != nil {
			_ = mcpGatewayClient.Close()
			mcpGatewayClient = nil
		}

		// Remove caCertBundleRef from Gateway to avoid leaking state
		gw := &mcpv1alpha1.MCPGatewayExtension{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: GatewayName, Namespace: GatewayNamespace}, gw); err == nil {
			if gw.Spec.CACertBundleRef != nil {
				gw.Spec.CACertBundleRef = nil
				_ = k8sClient.Update(ctx, gw)
			}
		}

		for _, obj := range testResources {
			CleanupResource(ctx, k8sClient, obj)
		}
		testResources = []client.Object{}
	})

	createGatewayCABundle := func(name string, pemData []byte) *corev1.Secret {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: GatewayNamespace,
				Labels: map[string]string{
					"mcp.kuadrant.io/secret": "true",
					"e2e":                    "test",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"ca.crt": pemData,
			},
		}
		_ = k8sClient.Delete(ctx, secret)
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		testResources = append(testResources, secret)
		return secret
	}

	setGatewayCABundle := func(secretName string) {
		gw := &mcpv1alpha1.MCPGatewayExtension{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: GatewayName, Namespace: GatewayNamespace}, gw)).To(Succeed())
		gw.Spec.CACertBundleRef = &mcpv1alpha1.CACertBundleReference{
			Name: secretName,
		}
		Expect(k8sClient.Update(ctx, gw)).To(Succeed())
	}

	It("[Happy,CACertBundle] Gateway CA bundle enables TLS connection to upstream server", func() {
		By("Creating gateway CA bundle secret")
		createGatewayCABundle(caSecretName, caCertPEM)

		By("Configuring MCPGatewayExtension with caCertBundleRef")
		setGatewayCABundle(caSecretName)

		By("Creating MCPServerRegistration WITHOUT caCertSecretRef")
		registration := NewTestResources("bundle-tls", k8sClient).
			ForInternalService(tlsServerName, tlsServerPort).
			WithHostname(tlsServerHostname).
			WithPrefix("bundle_tls_").
			WithSectionName(tlsListenerName).
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Verifying tools are accessible")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcpgo.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent("bundle_tls_", toolsList)).To(BeTrue(),
				"tools with prefix bundle_tls_ should exist")
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	})

	It("[Happy,CACertBundle] Multiple servers sharing the same gateway CA bundle", func() {
		By("Creating gateway CA bundle secret")
		createGatewayCABundle(caSecretName, caCertPEM)
		setGatewayCABundle(caSecretName)

		By("Creating first MCPServerRegistration")
		reg1 := NewTestResources("bundle-tls-1", k8sClient).
			ForInternalService(tlsServerName, tlsServerPort).
			WithHostname(tlsServerHostname).
			WithPrefix("bundle_multi_1_").
			WithSectionName(tlsListenerName).
			Build()
		testResources = append(testResources, reg1.GetObjects()...)
		rs1 := reg1.Register(ctx)

		By("Creating second MCPServerRegistration")
		reg2 := NewTestResources("bundle-tls-2", k8sClient).
			ForInternalService(tlsServerName, tlsServerPort).
			WithHostname(tlsServerHostname).
			WithPrefix("bundle_multi_2_").
			WithSectionName(tlsListenerName).
			Build()
		testResources = append(testResources, reg2.GetObjects()...)
		rs2 := reg2.Register(ctx)

		By("Verifying both servers become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, rs1.Name, rs1.Namespace)).To(Succeed())
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, rs2.Name, rs2.Namespace)).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Verifying tools from both are accessible")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcpgo.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolsPresent("bundle_multi_1_", toolsList)).To(BeTrue())
			g.Expect(verifyMCPServerRegistrationToolsPresent("bundle_multi_2_", toolsList)).To(BeTrue())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	})

	It("[CACertBundle] Gateway bundle alone insufficient for server with unique CA", func() {
		By("Creating gateway CA bundle secret with wrong CA")
		wrongCAPEM := generateSelfSignedCACert()
		createGatewayCABundle(wrongCaSecret, wrongCAPEM)
		setGatewayCABundle(wrongCaSecret)

		By("Creating MCPServerRegistration WITHOUT caCertSecretRef")
		registration := NewTestResources("bundle-wrong-tls", k8sClient).
			ForInternalService(tlsServerName, tlsServerPort).
			WithHostname(tlsServerHostname).
			WithPrefix("bundle_wrong_").
			WithSectionName(tlsListenerName).
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration is not ready")
		Eventually(func(g Gomega) {
			mcpsr := &mcpv1alpha1.MCPServerRegistration{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: registeredServer.Name, Namespace: registeredServer.Namespace,
			}, mcpsr)).To(Succeed())
			g.Expect(mcpsr.Status.Conditions).NotTo(BeEmpty())
			for _, cond := range mcpsr.Status.Conditions {
				if cond.Type == "Ready" {
					g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
					g.Expect(cond.Message).To(ContainSubstring("x509"))
					return
				}
			}
			g.Expect(false).To(BeTrue(), "no Ready condition found")
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	})

	It("[CACertBundle] Invalid CA bundle secret — MCPGatewayExtension reports error", func() {
		By("Setting caCertBundleRef to a non-existent secret")
		setGatewayCABundle("non-existent-secret")

		By("Verifying MCPGatewayExtension reports error in status")
		Eventually(func(g Gomega) {
			gw := &mcpv1alpha1.MCPGatewayExtension{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: GatewayName, Namespace: GatewayNamespace}, gw)).To(Succeed())
			found := false
			for _, cond := range gw.Status.Conditions {
				if cond.Type == "Ready" && cond.Status == metav1.ConditionFalse {
					g.Expect(cond.Message).To(ContainSubstring("not found"))
					found = true
				}
			}
			g.Expect(found).To(BeTrue(), "expected Ready=False with not found error")
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})
})
