//go:build e2e

// Package e2e contains end-to-end tests for HTTPS MCP backend connections.
// These tests verify the controller correctly handles TLS when connecting to
// upstream MCP servers. They are intentionally excluded from standard PR checks
// because they require external services (GitHub PAT) or cluster infrastructure
// (cert-manager, real wildcard certs) not present in the standard Kind setup.
//
// Run with: make test-e2e-https
// Required env vars:
//   - GITHUB_MCP_PAT — GitHub Personal Access Token with repo scope (Test A)
//
// Optional env vars:
//   - E2E_HTTPS_REAL_CERTS=true — enables Test B (requires a real TLS-capable cluster)
package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
)

// GitHub MCP endpoint constants
const (
	// githubMCPHost is the external hostname of the GitHub MCP server.
	githubMCPHost = "api.githubcopilot.com"
	// githubMCPPort is the HTTPS port for the GitHub MCP server.
	githubMCPPort = int32(443)
	// githubMCPPath is the MCP endpoint path on the GitHub server.
	githubMCPPath = "/mcp"
)

var _ = Describe("HTTPS MCP Backends", func() {
	// testResources tracks all resources created in each test so AfterEach can clean up.
	var testResources []client.Object

	AfterEach(func() {
		for _, obj := range testResources {
			CleanupResource(ctx, k8sClient, obj)
		}
		testResources = nil
	})

	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			GinkgoWriter.Println("HTTPS test failure detected — dumping MCPServerRegistrations:")
			list := &mcpv1alpha1.MCPServerRegistrationList{}
			if err := k8sClient.List(ctx, list, client.InNamespace(TestServerNameSpace)); err == nil {
				for _, sr := range list.Items {
					GinkgoWriter.Printf("  %s: conditions=%v\n", sr.Name, sr.Status.Conditions)
				}
			}
		}
	})

	// -------------------------------------------------------------------------
	// Test Case A: External service with public TLS (GitHub MCP)
	// -------------------------------------------------------------------------
	// Verifies the broker can discover tools from the GitHub MCP server, which
	// uses a publicly trusted TLS certificate. No custom CA cert is needed.
	// The controller already generates https:// URLs for Hostname-type backends.
	// -------------------------------------------------------------------------
	It("[HTTPS] [Happy] External GitHub MCP server discovers tools over public TLS", func() {
		pat := os.Getenv("GITHUB_MCP_PAT")
		if pat == "" {
			Skip("Skipping: GITHUB_MCP_PAT environment variable not set")
		}

		By("Creating a Secret containing the GitHub PAT")
		// The secret must carry the mcp.kuadrant.io/secret=true label so the
		// controller's secret watch filter accepts it.
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
				"token": pat,
			},
		}
		Expect(k8sClient.Create(ctx, patSecret)).To(Succeed())
		testResources = append(testResources, patSecret)

		By("Registering the GitHub MCP server as an external hostname backend")
		// ForExternalService creates an Istio ServiceEntry + Hostname-type HTTPRoute.
		// The controller detects the Hostname backend type and generates an
		// https:// URL for the broker to connect to.
		resources := NewTestResources("github-mcp", k8sClient).
			ForExternalService(githubMCPHost, githubMCPPort).
			WithPrefix("github_").
			WithPath(githubMCPPath).
			WithCredential(patSecret, "token").
			WithParentGateway(GatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, resources.GetObjects()...)
		resources.Register(ctx)

		mcpServer := resources.GetMCPServer()

		By("Waiting for MCPServerRegistration to become Ready")
		// The controller must: find the ServiceEntry, resolve the external host,
		// build the https:// URL, connect via the broker, and discover tools.
		// Use TestTimeoutLong because the GitHub MCP server may take time to respond.
		Eventually(func(g Gomega) {
			err := VerifyMCPServerRegistrationReady(ctx, k8sClient, mcpServer.Name, TestServerNameSpace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Asserting the registered server has discovered at least one tool")
		// If discoveredTools > 0 the broker successfully connected over HTTPS and
		// called tools/list on the GitHub MCP endpoint.
		Eventually(func(g Gomega) {
			sr := &mcpv1alpha1.MCPServerRegistration{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mcpServer), sr)).To(Succeed())
			g.Expect(sr.Status.DiscoveredTools).To(BeNumerically(">", 0),
				"expected at least one tool discovered over HTTPS from GitHub MCP")
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Asserting the config stored for this server uses an https:// URL")
		// Read the broker config Secret and verify the URL scheme.
		// The controller writes server configs into the mcp-gateway-config Secret.
		Eventually(func(g Gomega) {
			secret := &corev1.Secret{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{
				Name:      ConfigMapName,
				Namespace: SystemNamespace,
			}, secret)).To(Succeed())
			// The config data contains server URLs; find our github entry.
			for _, v := range secret.Data {
				if strings.Contains(string(v), githubMCPHost) {
					g.Expect(string(v)).To(ContainSubstring("https://"),
						"GitHub MCP server should have an https:// URL in config")
				}
			}
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	// -------------------------------------------------------------------------
	// Test Case B: In-cluster with real public wildcard certificate (skip-guarded)
	// -------------------------------------------------------------------------
	// This test verifies initialize, tools/list, and tools/call all work over
	// HTTPS end-to-end when the gateway is fronted by a real TLS certificate.
	// It was manually verified on OpenShift 4.20 with a Let's Encrypt wildcard cert
	// (see issue #450 for the verification procedure).
	//
	// This test CANNOT run on a standard Kind cluster. It requires:
	//   - A cluster with a TLS-terminating load balancer
	//   - A publicly trusted wildcard certificate for the gateway hostname
	//   - E2E_HTTPS_REAL_CERTS=true and E2E_SCHEME=https and E2E_DOMAIN set
	// -------------------------------------------------------------------------
	It("[HTTPS] [RealCerts] In-cluster MCP server accessible over public TLS", func() {
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
		resources.Register(ctx)

		mcpServer := resources.GetMCPServer()

		By("Waiting for MCPServerRegistration to become Ready over HTTPS")
		Eventually(func(g Gomega) {
			err := VerifyMCPServerRegistrationReady(ctx, k8sClient, mcpServer.Name, TestServerNameSpace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Verifying tools are accessible via the HTTPS gateway URL")
		var mcpClient *NotifyingMCPClient
		Eventually(func(g Gomega) {
			var err error
			mcpClient, err = NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(_ mcp.JSONRPCNotification) {})
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		defer func() { _ = mcpClient.Close() }()

		By("Verifying tools/list succeeds over HTTPS")
		Eventually(func(g Gomega) {
			toolsList, err := mcpClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			var names []string
			for _, t := range toolsList.Tools {
				names = append(names, t.Name)
			}
			g.Expect(names).To(ContainElement(ContainSubstring("realcert_")),
				"expected to find realcert_ prefixed tools over HTTPS")
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	// -------------------------------------------------------------------------
	// Test Case C (Positive): In-cluster with private CA via cert-manager
	// -------------------------------------------------------------------------
	// Deploys a TLS-terminating Nginx sidecar that fronts mcp-test-server2.
	// Uses cert-manager to issue a certificate from a private (self-signed) CA.
	// The MCPServerRegistration includes the caCertSecretRef pointing at the CA
	// bundle so the broker can verify the private certificate chain.
	// -------------------------------------------------------------------------
	It("[HTTPS] [Happy] Broker connects to in-cluster TLS upstream with private CA cert", func() {
		certManagerAvailable := checkCertManagerAvailable(ctx, k8sClient)
		if !certManagerAvailable {
			Skip("Skipping: cert-manager is not available in this cluster")
		}

		By("Creating a self-signed cert-manager Issuer")
		// The Issuer is namespace-scoped and creates a self-signed CA.
		issuerName := UniqueName("https-test-issuer")
		issuer := buildSelfSignedIssuer(issuerName, TestServerNameSpace)
		Expect(k8sClient.Create(ctx, issuer)).To(Succeed())
		testResources = append(testResources, issuer)

		By("Creating a cert-manager CA Certificate (self-signed root)")
		caCertName := UniqueName("https-test-ca")
		caCert := buildCACertificate(caCertName, issuerName, TestServerNameSpace)
		Expect(k8sClient.Create(ctx, caCert)).To(Succeed())
		testResources = append(testResources, caCert)

		By("Waiting for the CA Certificate Secret to be issued")
		// cert-manager populates a Secret named after the Certificate once it's ready.
		// We poll until the secret exists and contains a ca.crt key.
		caCertSecretName := caCertName // cert-manager uses the Certificate name as the Secret name
		Eventually(func(g Gomega) {
			secret := &corev1.Secret{}
			err := k8sClient.Get(ctx, client.ObjectKey{
				Name:      caCertSecretName,
				Namespace: TestServerNameSpace,
			}, secret)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(secret.Data).To(HaveKey("ca.crt"), "CA cert secret must contain ca.crt")
			g.Expect(secret.Data["ca.crt"]).NotTo(BeEmpty())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Labelling the CA cert Secret so the controller's watch filter accepts it")
		caSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{
			Name:      caCertSecretName,
			Namespace: TestServerNameSpace,
		}, caSecret)).To(Succeed())
		patch := client.MergeFrom(caSecret.DeepCopy())
		if caSecret.Labels == nil {
			caSecret.Labels = map[string]string{}
		}
		caSecret.Labels["mcp.kuadrant.io/secret"] = "true"
		Expect(k8sClient.Patch(ctx, caSecret, patch)).To(Succeed())
		testResources = append(testResources, caSecret)

		By("Creating a CA Issuer")
		caIssuerName := UniqueName("https-ca-issuer")
		caIssuer := buildCAIssuer(caIssuerName, caCertSecretName, TestServerNameSpace)
		Expect(k8sClient.Create(ctx, caIssuer)).To(Succeed())
		testResources = append(testResources, caIssuer)

		By("Creating a leaf Certificate for the Nginx TLS proxy")
		leafCertName := UniqueName("https-test-leaf")
		leafCert := buildLeafCertificate(leafCertName, caIssuerName, TestServerNameSpace)
		Expect(k8sClient.Create(ctx, leafCert)).To(Succeed())
		testResources = append(testResources, leafCert)

		By("Waiting for the leaf Certificate to be Ready")
		Eventually(func(g Gomega) {
			c := &unstructuredCert{}
			err := k8sClient.Get(ctx, client.ObjectKey{
				Name:      leafCertName,
				Namespace: TestServerNameSpace,
			}, c.asObject())
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(c.isReady()).To(BeTrue(), "leaf certificate should be ready")
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Deploying the Nginx TLS proxy in front of mcp-test-server2")
		nginxName := UniqueName("nginx-tls-proxy")
		nginxResources := buildNginxTLSProxy(nginxName, leafCertName, TestServerNameSpace)
		for _, obj := range nginxResources {
			Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		}
		testResources = append(testResources, nginxResources...)

		By("Registering the TLS proxy with caCertSecretRef pointing at the private CA")
		// The builder creates: ServiceEntry (external Hostname) + HTTPRoute + MCPServerRegistration.
		// WithCACertSecret tells the controller to read the CA PEM and pass it to the broker.
		resources := NewTestResources("https-private-ca", k8sClient).
			ForInternalService(nginxName, 443).
			WithPrefix("privateca_").
			WithCACertSecretRef(caSecret.Name, "ca.crt").
			WithParentGateway(GatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, resources.GetObjects()...)
		resources.Register(ctx)

		mcpServer := resources.GetMCPServer()

		By("Waiting for MCPServerRegistration to become Ready over private-CA TLS")
		// The broker must: load the CA cert, build a custom http.Client, connect to
		// the Nginx TLS proxy over HTTPS, and successfully call tools/list.
		Eventually(func(g Gomega) {
			err := VerifyMCPServerRegistrationReady(ctx, k8sClient, mcpServer.Name, TestServerNameSpace)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Asserting tools were discovered through the private-CA TLS connection")
		Eventually(func(g Gomega) {
			sr := &mcpv1alpha1.MCPServerRegistration{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mcpServer), sr)).To(Succeed())
			g.Expect(sr.Status.DiscoveredTools).To(BeNumerically(">", 0),
				"expected tools to be discovered via the private-CA TLS upstream")
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	// -------------------------------------------------------------------------
	// Test Case C (Negative): TLS upstream without CA cert must fail
	// -------------------------------------------------------------------------
	// Same Nginx TLS proxy backed by a private CA, but the MCPServerRegistration
	// deliberately omits the caCertSecretRef. The broker's default TLS client
	// cannot verify the private certificate chain and must report a cert error.
	// -------------------------------------------------------------------------
	It("[HTTPS] [Negative] Broker rejects private-CA TLS upstream when CA cert is not provided", func() {
		certManagerAvailable := checkCertManagerAvailable(ctx, k8sClient)
		if !certManagerAvailable {
			Skip("Skipping: cert-manager is not available in this cluster")
		}

		By("Creating a self-signed CA Issuer and Certificate for this negative test")
		issuerName := UniqueName("neg-test-issuer")
		issuer := buildSelfSignedIssuer(issuerName, TestServerNameSpace)
		Expect(k8sClient.Create(ctx, issuer)).To(Succeed())
		testResources = append(testResources, issuer)

		caCertName := UniqueName("neg-test-ca")
		caCert := buildCACertificate(caCertName, issuerName, TestServerNameSpace)
		Expect(k8sClient.Create(ctx, caCert)).To(Succeed())
		testResources = append(testResources, caCert)

		By("Creating a CA Issuer")
		caIssuerName := UniqueName("neg-ca-issuer")
		caIssuer := buildCAIssuer(caIssuerName, caCertName, TestServerNameSpace)
		Expect(k8sClient.Create(ctx, caIssuer)).To(Succeed())
		testResources = append(testResources, caIssuer)

		leafCertName := UniqueName("neg-test-leaf")
		leafCert := buildLeafCertificate(leafCertName, caIssuerName, TestServerNameSpace)
		Expect(k8sClient.Create(ctx, leafCert)).To(Succeed())
		testResources = append(testResources, leafCert)

		By("Waiting for the leaf Certificate to be Ready")
		Eventually(func(g Gomega) {
			c := &unstructuredCert{}
			err := k8sClient.Get(ctx, client.ObjectKey{
				Name:      leafCertName,
				Namespace: TestServerNameSpace,
			}, c.asObject())
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(c.isReady()).To(BeTrue())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Deploying the Nginx TLS proxy (same private CA)")
		nginxName := UniqueName("neg-nginx-tls")
		nginxResources := buildNginxTLSProxy(nginxName, leafCertName, TestServerNameSpace)
		for _, obj := range nginxResources {
			Expect(k8sClient.Create(ctx, obj)).To(Succeed())
		}
		testResources = append(testResources, nginxResources...)

		By("Registering the TLS proxy WITHOUT providing a caCertSecretRef")
		// The broker will use the system root pool, which does not trust the private CA.
		resources := NewTestResources("https-neg-no-ca", k8sClient).
			ForInternalService(nginxName, 443).
			WithPrefix("noca_").
			// Intentionally no WithCACertSecret() call
			WithParentGateway(GatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, resources.GetObjects()...)
		resources.Register(ctx)

		mcpServer := resources.GetMCPServer()

		By("Waiting for MCPServerRegistration to report NotReady with a TLS/cert error")
		// The broker should fail to connect with a "certificate signed by unknown
		// authority" (or equivalent) error, causing the controller to set the
		// Ready condition to False.
		Eventually(func(g Gomega) {
			err := VerifyMCPServerRegistrationHasCondition(ctx, k8sClient, mcpServer.Name, TestServerNameSpace)
			g.Expect(err).NotTo(HaveOccurred())

			msg, err := GetMCPServerRegistrationStatusMessage(ctx, k8sClient, mcpServer.Name, TestServerNameSpace)
			g.Expect(err).NotTo(HaveOccurred())
			GinkgoWriter.Printf("Status message: %s\n", msg)
			// The error message may vary by Go version / OS, but will always
			// contain one of these substrings for a TLS verification failure.
			g.Expect(msg).To(Or(
				ContainSubstring("certificate signed by unknown authority"),
				ContainSubstring("x509:"),
				ContainSubstring("tls:"),
				ContainSubstring("certificate verify"),
			), "expected a TLS certificate verification error in the status message")
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Confirming the server did NOT successfully discover any tools")
		sr := &mcpv1alpha1.MCPServerRegistration{}
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mcpServer), sr)).To(Succeed())
		Expect(sr.Status.DiscoveredTools).To(BeZero(),
			"no tools should be discovered when TLS verification fails")
	})
})

// -----------------------------------------------------------------------------
// cert-manager helpers
// These use unstructured objects so the test binary does not need to import
// the cert-manager Go client (which is a heavy dependency).
// -----------------------------------------------------------------------------

// checkCertManagerAvailable returns true if the cert-manager CRD for Certificates exists.
func checkCertManagerAvailable(ctx context.Context, k8sClient client.Client) bool {
	cert := buildCertificateUnstructured("probe", "default")
	err := k8sClient.Get(ctx, client.ObjectKey{Name: "probe", Namespace: "default"}, cert)
	// If the error is NotFound, the CRD exists. Any other error (e.g. "no kind is registered")
	// means cert-manager is absent.
	return apierrors.IsNotFound(err)
}

// unstructuredCert is a thin wrapper for reading cert-manager Certificate status
type unstructuredCert struct {
	obj unstructured.Unstructured
}

func (c *unstructuredCert) asObject() client.Object {
	c.obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	})
	return &c.obj
}

func (c *unstructuredCert) isReady() bool {
	conditions, found, err := unstructured.NestedSlice(c.obj.Object, "status", "conditions")
	if err != nil || !found || conditions == nil {
		return false
	}
	for _, cond := range conditions {
		condition, ok := cond.(map[string]interface{})
		if !ok {
			continue
		}
		if condition["type"] == "Ready" && condition["status"] == "True" {
			return true
		}
	}
	return false
}

func buildCertificateUnstructured(name, namespace string) client.Object {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	})
	u.SetName(name)
	u.SetNamespace(namespace)
	return u
}

func buildSelfSignedIssuer(name, namespace string) client.Object {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Issuer",
	})
	u.SetName(name)
	u.SetNamespace(namespace)
	u.Object["spec"] = map[string]interface{}{
		"selfSigned": map[string]interface{}{},
	}
	return u
}

func buildCACertificate(name, issuerName, namespace string) client.Object {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	})
	u.SetName(name)
	u.SetNamespace(namespace)
	u.Object["spec"] = map[string]interface{}{
		"isCA":       true,
		"commonName": "mcp-e2e-ca",
		"secretName": name,
		"issuerRef": map[string]interface{}{
			"name": issuerName,
			"kind": "Issuer",
		},
	}
	return u
}

func buildCAIssuer(name, secretName, namespace string) client.Object {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Issuer",
	})
	u.SetName(name)
	u.SetNamespace(namespace)
	u.Object["spec"] = map[string]interface{}{
		"ca": map[string]interface{}{
			"secretName": secretName,
		},
	}
	return u
}

func buildLeafCertificate(name, caIssuerName, namespace string) client.Object {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	})
	u.SetName(name)
	u.SetNamespace(namespace)
	u.Object["spec"] = map[string]interface{}{
		"dnsNames": []interface{}{
			fmt.Sprintf("%s.%s.svc.cluster.local", name, namespace),
			name,
		},
		"secretName": name,
		"issuerRef": map[string]interface{}{
			"name": caIssuerName,
			"kind": "Issuer",
		},
	}
	return u
}

func buildNginxTLSProxy(name, leafCertSecretName, namespace string) []client.Object {
	nginxConf := `
events {}
http {
  server {
    listen 443 ssl;
    ssl_certificate     /etc/nginx/tls/tls.crt;
    ssl_certificate_key /etc/nginx/tls/tls.key;

    location / {
      proxy_pass http://mcp-test-server2:9090;
      proxy_set_header Host $host;
    }
  }
}
`
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + "-conf",
			Namespace: namespace,
			Labels:    map[string]string{"e2e": "test"},
		},
		Data: map[string]string{
			"nginx.conf": nginxConf,
		},
	}

	labels := map[string]string{"app": name, "e2e": "test"}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "nginx",
							Image: "nginx:alpine",
							Ports: []corev1.ContainerPort{{ContainerPort: 443}},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "config", MountPath: "/etc/nginx/nginx.conf", SubPath: "nginx.conf"},
								{Name: "tls", MountPath: "/etc/nginx/tls", ReadOnly: true},
							},
						},
					},
					Volumes: []corev1.Volume{
						{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configMap.Name}}}},
						{Name: "tls", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: leafCertSecretName}}},
					},
				},
			},
		},
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{Port: 443, TargetPort: intstr.FromInt32(443)},
			},
		},
	}

	return []client.Object{configMap, deployment, svc}
}
