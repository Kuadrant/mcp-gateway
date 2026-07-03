//go:build e2e

package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
)

const (
	caBundleSecretName = "e2e-gateway-ca-bundle"
)

// patchExtensionCACertBundle patches the existing MCPGatewayExtension in-place to
// set or clear the caCertBundleRef. This avoids recreating the extension and losing
// deployment patches (e.g. --gateway-ca-cert for hairpin requests).
func patchExtensionCACertBundle(ref *mcpv1alpha1.CACertBundleReference) {
	ext := &mcpv1alpha1.MCPGatewayExtension{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{
		Name: MCPExtensionName, Namespace: SystemNamespace,
	}, ext)).To(Succeed())

	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"caCertBundleRef": ref,
		},
	}
	patchBytes, err := json.Marshal(patch)
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient.Patch(ctx, ext, client.RawPatch(types.MergePatchType, patchBytes))).To(Succeed())
}

var _ = Describe("CA Cert Bundle", Ordered, Serial, func() {
	var (
		testResources    []client.Object
		mcpGatewayClient *NotifyingMCPClient
		correctCAPEM     []byte
	)

	BeforeAll(func() {
		By("Extracting correct CA cert from cert-manager")
		caSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: caKeypairSecret, Namespace: certManagerNS,
		}, caSecret)).To(Succeed())
		var ok bool
		correctCAPEM, ok = caSecret.Data["ca.crt"]
		Expect(ok).To(BeTrue(), "private-ca-keypair should have ca.crt")
	})

	BeforeEach(func() {
		testResources = []client.Object{}
	})

	AfterEach(func() {
		if mcpGatewayClient != nil {
			_ = mcpGatewayClient.Close()
			mcpGatewayClient = nil
		}

		for _, obj := range testResources {
			CleanupResource(ctx, k8sClient, obj)
		}
		testResources = []client.Object{}

		By("Removing caCertBundleRef from MCPGatewayExtension")
		patchExtensionCACertBundle(nil)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPGatewayExtensionReady(ctx, k8sClient, MCPExtensionName, SystemNamespace)).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	})

	setupBundleRef := func() {
		By("Patching MCPGatewayExtension with caCertBundleRef")
		ref := &mcpv1alpha1.CACertBundleReference{Name: caBundleSecretName, Key: "ca.crt"}
		patchExtensionCACertBundle(ref)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPGatewayExtensionReady(ctx, k8sClient, MCPExtensionName, SystemNamespace)).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	}

	createLabeledCASecret := func(name, namespace string, pemData []byte) *corev1.Secret {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
				Labels: map[string]string{
					"mcp.kuadrant.io/secret": "true",
					"e2e":                    "test",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"ca.crt": pemData},
		}
		_ = k8sClient.Delete(ctx, secret)
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		return secret
	}

	connectMCPClient := func() {
		Eventually(func(g Gomega) {
			var err error
			mcpGatewayClient, err = NewMCPGatewayClientWithNotifications(ctx, gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	}

	It("[HTTPS] [Happy,CACertBundle] Gateway CA bundle enables TLS connection to upstream server", func() {
		By("Creating CA bundle secret with correct CA")
		caBundle := createLabeledCASecret(caBundleSecretName, SystemNamespace, correctCAPEM)
		testResources = append(testResources, caBundle)

		setupBundleRef()
		connectMCPClient()

		By("Registering TLS server WITHOUT per-server caCertSecretRef")
		registration := NewTestResources("ca-bundle-basic", k8sClient).
			ForInternalService(tlsServerName, tlsServerPort).
			WithHostname(tlsServerHostname).
			WithPrefix("cab_basic_").
			WithSectionName(tlsListenerName).
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient,
				registeredServer.Name, registeredServer.Namespace)).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Verifying tools are discovered")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcpgo.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolsPresent("cab_basic_", toolsList)).To(BeTrue())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	})

	It("[HTTPS] [Full,CACertBundle] Multiple servers sharing the same gateway CA bundle", func() {
		By("Creating CA bundle secret with correct CA")
		caBundle := createLabeledCASecret(caBundleSecretName, SystemNamespace, correctCAPEM)
		testResources = append(testResources, caBundle)

		setupBundleRef()
		connectMCPClient()

		By("Registering first TLS server without per-server CA")
		reg1 := NewTestResources("cab-multi1", k8sClient).
			ForInternalService(tlsServerName, tlsServerPort).
			WithHostname(tlsServerHostname).
			WithPrefix("cab_m1_").
			WithSectionName(tlsListenerName).
			Build()
		testResources = append(testResources, reg1.GetObjects()...)
		server1 := reg1.Register(ctx)

		By("Registering second TLS server without per-server CA")
		reg2 := NewTestResources("cab-multi2", k8sClient).
			ForInternalService(tlsServerName, tlsServerPort).
			WithHostname(tlsServerHostname).
			WithPrefix("cab_m2_").
			WithSectionName(tlsListenerName).
			Build()
		testResources = append(testResources, reg2.GetObjects()...)
		server2 := reg2.Register(ctx)

		By("Verifying both registrations become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server1.Name, server1.Namespace)).To(Succeed())
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, server2.Name, server2.Namespace)).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Verifying tools from both servers are discovered")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcpgo.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolsPresent("cab_m1_", toolsList)).To(BeTrue())
			g.Expect(verifyMCPServerRegistrationToolsPresent("cab_m2_", toolsList)).To(BeTrue())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	})

	It("[HTTPS] [Full,CACertBundle] Per-server CA appends to gateway bundle", func() {
		By("Generating unrelated CA for gateway bundle")
		unrelatedCAPEM := generateSelfSignedCACertForBundle("Unrelated Gateway CA")
		caBundle := createLabeledCASecret(caBundleSecretName, SystemNamespace, unrelatedCAPEM)
		testResources = append(testResources, caBundle)

		setupBundleRef()
		connectMCPClient()

		By("Creating per-server CA secret with the correct CA")
		perServerCA := createLabeledCASecret("e2e-per-server-ca", TestServerNameSpace, correctCAPEM)
		testResources = append(testResources, perServerCA)

		By("Registering TLS server with per-server caCertSecretRef")
		registration := NewTestResources("cab-additive", k8sClient).
			ForInternalService(tlsServerName, tlsServerPort).
			WithHostname(tlsServerHostname).
			WithPrefix("cab_add_").
			WithSectionName(tlsListenerName).
			WithCACertSecretRef(perServerCA.Name, "ca.crt").
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient,
				registeredServer.Name, registeredServer.Namespace)).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Verifying tools are discovered via additive trust pool")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcpgo.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolsPresent("cab_add_", toolsList)).To(BeTrue())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	})

	It("[HTTPS] [Full,CACertBundle] Wrong CA fails, rotation to correct CA recovers", func() {
		By("Generating wrong CA for gateway bundle")
		wrongCAPEM := generateSelfSignedCACertForBundle("Wrong CA")
		caBundle := createLabeledCASecret(caBundleSecretName, SystemNamespace, wrongCAPEM)
		testResources = append(testResources, caBundle)

		setupBundleRef()
		connectMCPClient()

		By("Registering TLS server WITHOUT per-server CA (gateway bundle has wrong CA)")
		registration := NewTestResources("cab-wrong", k8sClient).
			ForInternalService(tlsServerName, tlsServerPort).
			WithHostname(tlsServerHostname).
			WithPrefix("cab_wrong_").
			WithSectionName(tlsListenerName).
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registration.Register(ctx)

		By("Verifying tools are absent (TLS handshake fails)")
		// weak assertion: only proves tools don't appear, not why.
		// stronger: hit broker /status and assert x509 error in Message field.
		Consistently(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcpgo.ListToolsRequest{})
			if err != nil {
				return
			}
			g.Expect(verifyMCPServerRegistrationToolsPresent("cab_wrong_", toolsList)).To(BeFalse())
		}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

		By("Rotating CA bundle to correct CA")
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: caBundleSecretName, Namespace: SystemNamespace,
		}, caBundle)).To(Succeed())
		caBundle.Data["ca.crt"] = correctCAPEM
		Expect(k8sClient.Update(ctx, caBundle)).To(Succeed())

		By("Reconnecting MCP client after rotation")
		_ = mcpGatewayClient.Close()
		connectMCPClient()

		By("Verifying tools appear after CA rotation propagates")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcpgo.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolsPresent("cab_wrong_", toolsList)).To(BeTrue())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	})

	It("[HTTPS] [Full,CACertBundle] Invalid CA bundle secret — MCPGatewayExtension reports error", func() {
		By("Patching MCPGatewayExtension to reference non-existent secret")
		ref := &mcpv1alpha1.CACertBundleReference{Name: "nonexistent-secret", Key: "ca.crt"}
		patchExtensionCACertBundle(ref)

		By("Verifying MCPGatewayExtension reports SecretNotFound")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPGatewayExtensionNotReadyWithReason(ctx, k8sClient,
				MCPExtensionName, SystemNamespace, string(mcpv1alpha1.ConditionReasonSecretNotFound))).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Creating secret WITHOUT required label")
		unlabeled := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "cab-no-label",
				Namespace: SystemNamespace,
				Labels:    map[string]string{"e2e": "test"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"ca.crt": correctCAPEM},
		}
		_ = k8sClient.Delete(ctx, unlabeled)
		Expect(k8sClient.Create(ctx, unlabeled)).To(Succeed())
		testResources = append(testResources, unlabeled)

		By("Patching extension to reference unlabeled secret")
		ref = &mcpv1alpha1.CACertBundleReference{Name: "cab-no-label", Key: "ca.crt"}
		patchExtensionCACertBundle(ref)

		By("Verifying MCPGatewayExtension reports SecretInvalid for missing label")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPGatewayExtensionNotReadyWithReason(ctx, k8sClient,
				MCPExtensionName, SystemNamespace, string(mcpv1alpha1.ConditionReasonSecretInvalid))).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	})

	It("[HTTPS] [Full,CACertBundle] Gateway CA bundle with existing per-server CA on same server", func() {
		By("Creating CA bundle secret with correct CA")
		caBundle := createLabeledCASecret(caBundleSecretName, SystemNamespace, correctCAPEM)
		testResources = append(testResources, caBundle)

		setupBundleRef()
		connectMCPClient()

		By("Creating per-server CA secret with the same correct CA")
		perServerCA := createLabeledCASecret("e2e-same-ca", TestServerNameSpace, correctCAPEM)
		testResources = append(testResources, perServerCA)

		By("Registering TLS server with both gateway bundle and per-server CA (same CA)")
		registration := NewTestResources("cab-dup", k8sClient).
			ForInternalService(tlsServerName, tlsServerPort).
			WithHostname(tlsServerHostname).
			WithPrefix("cab_dup_").
			WithSectionName(tlsListenerName).
			WithCACertSecretRef(perServerCA.Name, "ca.crt").
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient,
				registeredServer.Name, registeredServer.Namespace)).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Verifying tools are discovered (no conflict from duplicate CA)")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcpgo.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolsPresent("cab_dup_", toolsList)).To(BeTrue())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	})
})

func generateSelfSignedCACertForBundle(cn string) []byte {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	Expect(err).NotTo(HaveOccurred())
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
