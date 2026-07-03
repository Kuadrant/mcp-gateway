//go:build e2e

package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"time"

	wrappers "github.com/golang/protobuf/ptypes/wrappers"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	istiov1beta1 "istio.io/api/networking/v1beta1"
	istionetv1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	tlsServerName     = "mcp-tls-server"
	tlsServerPort     = int32(8443)
	tlsListenerName   = "mcp-tls"
	tlsServerHostname = "tls-server.mcp-gateway.local"
	caKeypairSecret   = "private-ca-keypair"
	certManagerNS     = "cert-manager"
	wrongCaSecret     = "e2e-wrong-ca"

	githubMCPHost = "api.githubcopilot.com"
	githubMCPPort = int32(443)
	githubMCPPath = "/mcp"
)

var _ = Describe("Custom TLS Configuration", Ordered, func() {
	var (
		testResources    []client.Object
		mcpGatewayClient *NotifyingMCPClient
	)

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
		for _, obj := range testResources {
			CleanupResource(ctx, k8sClient, obj)
		}
		testResources = []client.Object{}
	})

	It("[HTTPS] [Happy] broker connects to TLS upstream and tools/call works via hairpin SNI", func() {
		By("Registering a plain HTTP backend via the HTTPS listener")
		registration := NewTestResources("tls-hairpin", k8sClient).
			ForInternalService("mcp-test-server1", 9090).
			WithHostname("hairpin-test.mcp-gateway.local").
			WithPrefix("tls_hp_").
			WithSectionName(tlsListenerName).
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Verifying tools with tls_hp_ prefix are present via tools/list")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcpgo.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent("tls_hp_", toolsList)).To(BeTrue(),
				"tools with prefix tls_hp_ should exist")
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Invoking tools/call — hairpin uses server hostname as SNI for correct filter chain selection")
		Eventually(func(g Gomega) {
			res, err := mcpGatewayClient.CallTool(ctx, mcpgo.CallToolRequest{
				Params: mcpgo.CallToolParams{
					Name:      "tls_hp_greet",
					Arguments: map[string]string{"name": "hairpin-sni-test"},
				},
			})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(res).NotTo(BeNil())
			g.Expect(res.IsError).To(BeFalse(), "tools/call should succeed via hairpin with server hostname SNI")
			g.Expect(len(res.Content)).To(BeNumerically(">=", 1))
			content, ok := res.Content[0].(mcpgo.TextContent)
			g.Expect(ok).To(BeTrue())
			g.Expect(content.Text).To(ContainSubstring("hairpin-sni-test"))
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	It("[HTTPS] [Negative] broker rejects TLS upstream with wrong CA certificate", func() {
		By("Generating a wrong CA certificate")
		wrongCAPEM := generateSelfSignedCACert()

		By("Creating labeled secret with wrong CA")
		wrongCA := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      wrongCaSecret,
				Namespace: TestServerNameSpace,
				Labels: map[string]string{
					"mcp.kuadrant.io/secret": "true",
					"e2e":                    "test",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"ca.crt": wrongCAPEM,
			},
		}
		_ = k8sClient.Delete(ctx, wrongCA)
		Expect(k8sClient.Create(ctx, wrongCA)).To(Succeed())
		testResources = append(testResources, wrongCA)

		By("Creating MCPServerRegistration with wrong CA")
		registration := NewTestResources("wrong-tls", k8sClient).
			ForInternalService(tlsServerName, tlsServerPort).
			WithHostname(tlsServerHostname).
			WithPrefix("tls_wrong_").
			WithSectionName(tlsListenerName).
			WithCACertSecretRef(wrongCaSecret, "ca.crt").
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		// tls certificate validation is a runtime concern handled by the broker.
		// the controller writes config successfully; errors surface via broker /status.
		By("Verifying MCPServerRegistration becomes Ready (TLS errors are broker-side)")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient,
				registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Verifying tools with tls_wrong_ prefix are absent")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcpgo.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolsPresent("tls_wrong_", toolsList)).To(BeFalse(),
				"tools with prefix tls_wrong_ should NOT exist")
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	It("[HTTPS] [Happy] tools/call to internal TLS backend fails without DestinationRule, succeeds with it", func() {
		By("Extracting CA cert from cert-manager secret")
		caSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: caKeypairSecret, Namespace: certManagerNS,
		}, caSecret)).To(Succeed())
		caCertPEM := caSecret.Data["ca.crt"]
		Expect(caCertPEM).NotTo(BeEmpty())

		By("Creating labeled CA secret in test namespace")
		labeledCA := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      UniqueName("tls-dr-ca"),
				Namespace: TestServerNameSpace,
				Labels: map[string]string{
					"mcp.kuadrant.io/secret": "true",
					"e2e":                    "test",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"ca.crt": caCertPEM},
		}
		_ = k8sClient.Delete(ctx, labeledCA)
		Expect(k8sClient.Create(ctx, labeledCA)).To(Succeed())
		testResources = append(testResources, labeledCA)

		By("Creating MCPServerRegistration targeting the TLS server (no DestinationRule yet)")
		registration := NewTestResources("tls-dr", k8sClient).
			ForInternalService(tlsServerName, tlsServerPort).
			WithHostname(tlsServerHostname).
			WithPrefix("tls_dr_").
			WithSectionName(tlsListenerName).
			WithCACertSecretRef(labeledCA.Name, "ca.crt").
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Verifying MCPServerRegistration becomes ready (broker connects directly)")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Verifying tools/list succeeds (broker discovers tools directly)")
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcpgo.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(verifyMCPServerRegistrationToolsPresent("tls_dr_", toolsList)).To(BeTrue())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Attempting tools/call without DestinationRule — Envoy sends plain HTTP to TLS backend")
		toolName := "tls_dr_echo_tls"
		Eventually(func(g Gomega) {
			res, err := mcpGatewayClient.CallTool(ctx, mcpgo.CallToolRequest{
				Params: mcpgo.CallToolParams{
					Name:      toolName,
					Arguments: map[string]string{"message": "should-fail"},
				},
			})
			// the hairpin initialize also routes through Envoy, so the plain-HTTP-to-TLS
			// mismatch can surface as either a transport error (500 from failed init) or
			// an MCP-level error (isError=true). Both prove the backend rejected it.
			failed := err != nil || (res != nil && res.IsError)
			g.Expect(failed).To(BeTrue(),
				"tools/call should fail: Envoy forwards plain HTTP to a TLS-only backend")
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Creating DestinationRule with TLS origination for the TLS server")
		tlsServerFQDN := fmt.Sprintf("%s.%s.svc.cluster.local", tlsServerName, TestServerNameSpace)
		dr := &istionetv1beta1.DestinationRule{
			ObjectMeta: metav1.ObjectMeta{
				Name:      UniqueName("tls-origination"),
				Namespace: TestServerNameSpace,
				Labels:    map[string]string{"e2e": "test"},
			},
			Spec: istiov1beta1.DestinationRule{
				Host: tlsServerFQDN,
				TrafficPolicy: &istiov1beta1.TrafficPolicy{
					Tls: &istiov1beta1.ClientTLSSettings{
						Mode:               istiov1beta1.ClientTLSSettings_SIMPLE,
						Sni:                tlsServerFQDN,
						InsecureSkipVerify: &wrappers.BoolValue{Value: true},
					},
				},
			},
		}
		_ = k8sClient.Delete(ctx, dr)
		Expect(k8sClient.Create(ctx, dr)).To(Succeed())
		testResources = append(testResources, dr)

		By("Reconnecting MCP client for fresh session after DestinationRule creation")
		_ = mcpGatewayClient.Close()
		Eventually(func(g Gomega) {
			var err error
			mcpGatewayClient, err = NewMCPGatewayClientWithNotifications(ctx, gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Calling tools/call — Envoy re-encrypts via DestinationRule TLS origination")
		Eventually(func(g Gomega) {
			res, err := mcpGatewayClient.CallTool(ctx, mcpgo.CallToolRequest{
				Params: mcpgo.CallToolParams{
					Name:      toolName,
					Arguments: map[string]string{"message": "tls-origination-test"},
				},
			})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(res).NotTo(BeNil())
			g.Expect(res.IsError).To(BeFalse(),
				"tools/call should succeed with DestinationRule TLS origination")
			g.Expect(len(res.Content)).To(BeNumerically(">=", 1))
			content, ok := res.Content[0].(mcpgo.TextContent)
			g.Expect(ok).To(BeTrue())
			g.Expect(content.Text).To(ContainSubstring("tls-origination-test"))
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})
})

var _ = Describe("HTTPS External Backends", func() {
	var testResources []client.Object

	AfterEach(func() {
		for _, obj := range testResources {
			CleanupResource(ctx, k8sClient, obj)
		}
		testResources = nil
	})

	It("[HTTPS] [HTTPS_EXTERNAL] External GitHub MCP server discovers tools over public TLS", func() {
		pat := os.Getenv("GITHUB_MCP_PAT")
		if pat == "" {
			Skip("GITHUB_MCP_PAT not set — skipping external GitHub MCP test")
		}

		By("Creating a Secret containing the GitHub PAT")
		patSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      UniqueName("github-pat"),
				Namespace: TestServerNameSpace,
				Labels: map[string]string{
					"mcp.kuadrant.io/secret": "true",
					"e2e":                    "test",
				},
			},
			Type: corev1.SecretTypeOpaque,
			StringData: map[string]string{
				"token": fmt.Sprintf("Bearer %s", pat),
			},
		}

		By("Registering the GitHub MCP server as an external hostname backend")
		resources := NewTestResources("github-mcp", k8sClient).
			ForExternalService(githubMCPHost, githubMCPPort).
			WithPrefix("github_").
			WithPath(githubMCPPath).
			WithCredential(patSecret, "token").
			WithParentGateway(GatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, resources.GetObjects()...)
		for _, obj := range resources.GetObjects() {
			CleanupResource(ctx, k8sClient, obj)
		}
		resources.Register(ctx)

		mcpServer := resources.GetMCPServer()

		By("Waiting for MCPServerRegistration to become Ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, mcpServer.Name, TestServerNameSpace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Asserting the config stored for this server uses an https:// URL")
		Eventually(func(g Gomega) {
			secret := &corev1.Secret{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{
				Name:      ConfigMapName,
				Namespace: SystemNamespace,
			}, secret)).To(Succeed())
			configData, ok := secret.Data["config.yaml"]
			g.Expect(ok).To(BeTrue(), "config secret should have config.yaml key")
			configStr := string(configData)
			g.Expect(configStr).To(ContainSubstring(githubMCPHost),
				"expected to find GitHub MCP host in config")
			g.Expect(configStr).To(ContainSubstring("https://"),
				"GitHub MCP server should have an https:// URL in config")
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	It("[HTTPS] [HTTPS_EXTERNAL] In-cluster MCP server accessible over public TLS via real certs", func() {
		if os.Getenv("E2E_HTTPS_REAL_CERTS") != "true" {
			Skip("Skipping: E2E_HTTPS_REAL_CERTS is not set to 'true'. " +
				"This test requires a cluster with a real wildcard certificate.")
		}
		if e2eScheme != "https" {
			Skip("Skipping: E2E_SCHEME must be 'https' for real-cert tests")
		}

		By("Registering an internal MCP server via HTTPS gateway")
		resources := NewTestResources("https-real-certs", k8sClient).
			ForInternalService("mcp-test-server1", 9090).
			WithPrefix("realcert_").
			WithParentGateway(GatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, resources.GetObjects()...)
		for _, obj := range resources.GetObjects() {
			CleanupResource(ctx, k8sClient, obj)
		}
		resources.Register(ctx)

		mcpServer := resources.GetMCPServer()

		By("Waiting for MCPServerRegistration to become Ready over HTTPS")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, mcpServer.Name, TestServerNameSpace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Verifying tools are accessible via the HTTPS gateway URL")
		var mcpClient *NotifyingMCPClient
		Eventually(func(g Gomega) {
			var err error
			mcpClient, err = NewMCPGatewayClientWithNotifications(ctx, gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer func() { _ = mcpClient.Close() }()

		By("Verifying tools/list succeeds over HTTPS")
		Eventually(func(g Gomega) {
			toolsList, err := mcpClient.ListTools(ctx, mcpgo.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(verifyMCPServerRegistrationToolsPresent("realcert_", toolsList)).To(BeTrue(),
				"expected to find realcert_ prefixed tools over HTTPS")
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})
})

func generateSelfSignedCACert() []byte {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Wrong CA"},
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
