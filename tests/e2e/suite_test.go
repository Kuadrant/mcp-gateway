//go:build e2e

// E2E Test Environment Configuration
//
// By default, tests target a local Kind cluster using localhost port mappings
// and 127-0-0-1.sslip.io hostnames. To run against a real cluster, set:
//
//   E2E_DOMAIN    - base domain for all public hostnames (default: 127-0-0-1.sslip.io)
//   E2E_SCHEME    - http or https (default: http)
//
// Setting E2E_DOMAIN to a non-default value causes gateway URLs to be derived
// from the public hostnames automatically (e.g. http://mcp.<domain>/mcp) instead
// of using Kind's localhost port mappings.
//
// Namespace overrides (for non-standard installations):
//
//   MCP_GATEWAY_NAMESPACE  - MCP system namespace (default: mcp-system)
//   GATEWAY_NAMESPACE      - Gateway namespace (default: gateway-system)
//   TEST_SERVER_NAMESPACE  - Test server namespace (default: mcp-test)
//
// Individual gateway/host overrides:
//
//   GATEWAY_URL, GATEWAY_PUBLIC_HOST
//   E2E1_GATEWAY_URL, E2E1_PUBLIC_HOST
//   TEAM_A_GATEWAY_URL, TEAM_A_PUBLIC_HOST
//   TEAM_B_GATEWAY_URL, TEAM_B_PUBLIC_HOST
//   AUTH_GATEWAY_URL
//   KEYCLOAK_TOKEN_URL
//
// See commons.go for the full list of configurable values and their defaults.

package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	istionetv1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
)

var (
	k8sClient            client.Client
	testScheme           *runtime.Scheme
	cfg                  *rest.Config
	ctx                  context.Context
	cancel               context.CancelFunc
	defaultMCPGatewayExt *MCPGatewayExtensionSetup
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "MCP Gateway E2E Suite")
}

// setupK8sClient initializes the shared k8s client and scheme for this process.
func setupK8sClient() {
	logf.SetLogger(logr.FromSlogHandler(slog.NewTextHandler(GinkgoWriter, &slog.HandlerOptions{Level: slog.LevelDebug})))

	ctx, cancel = context.WithCancel(context.Background())

	testScheme = runtime.NewScheme()
	Expect(scheme.AddToScheme(testScheme)).To(Succeed())
	Expect(mcpv1.AddToScheme(testScheme)).To(Succeed())
	Expect(gatewayapiv1.Install(testScheme)).To(Succeed())
	Expect(gatewayv1beta1.Install(testScheme)).To(Succeed())
	Expect(istionetv1beta1.AddToScheme(testScheme)).To(Succeed())

	var err error
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home := os.Getenv("HOME")
		kubeconfig = filepath.Join(home, ".kube", "config")
	}

	cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).ToNot(HaveOccurred())

	k8sClient, err = client.New(cfg, client.Options{Scheme: testScheme})
	Expect(err).ToNot(HaveOccurred())

	namespaceList := &corev1.NamespaceList{}
	Expect(k8sClient.List(ctx, namespaceList)).To(Succeed())
}

// SynchronizedBeforeSuite: first function runs on process 1 only (cluster
// mutation), second function runs on every process (local client setup).
var _ = SynchronizedBeforeSuite(func() {
	setupK8sClient()

	By("Checking test namespace exists")
	testNs := &corev1.Namespace{}
	err := k8sClient.Get(ctx, client.ObjectKey{Name: TestServerNameSpace}, testNs)
	if err != nil {
		GinkgoWriter.Printf("Warning: test namespace %s does not exist, tests may fail\n", TestServerNameSpace)
	}

	By("Checking system namespace exists")
	systemNs := &corev1.Namespace{}
	err = k8sClient.Get(ctx, client.ObjectKey{Name: SystemNamespace}, systemNs)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("System namespace %s must exist", SystemNamespace))

	By("cleaning up all existing mcpserverregistrations")
	err = k8sClient.DeleteAllOf(ctx, &mcpv1.MCPServerRegistration{}, client.InNamespace(TestServerNameSpace), &client.DeleteAllOfOptions{ListOptions: client.ListOptions{
		LabelSelector: labels.Everything(),
	}})
	Expect(err).ToNot(HaveOccurred(), "all existing MCPServers should be removed before the e2e test suite")

	By("cleaning up all http routes")
	err = k8sClient.DeleteAllOf(ctx, &gatewayapiv1.HTTPRoute{}, client.InNamespace(TestServerNameSpace))
	Expect(err).ToNot(HaveOccurred(), "all existing HTTPRoutes should be removed before the e2e test suite")

	By("cleaning up all existing mcpgatewayextensions")
	err = k8sClient.DeleteAllOf(ctx, &mcpv1.MCPGatewayExtension{}, client.InNamespace(SystemNamespace))
	Expect(err).ToNot(HaveOccurred(), "all existing MCPGatewayExtensions should be removed before the e2e test suite")

	By("waiting for existing mcpgatewayextensions to be fully deleted")
	Eventually(func(g Gomega) {
		extList := &mcpv1.MCPGatewayExtensionList{}
		err := k8sClient.List(ctx, extList, client.InNamespace(SystemNamespace))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(extList.Items).To(BeEmpty())
	}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

	By("adding HTTPS listener to the gateway")
	Expect(AddGatewayHTTPSListener(ctx, GatewayNamespace, GatewayName,
		GatewayListenerName, "*.mcp-gateway.local", "mcp-gateway-tls-cert", 8443)).To(Succeed())

	By("waiting for gateway to be programmed")
	Eventually(func(g Gomega) {
		gw := &gatewayapiv1.Gateway{}
		err := k8sClient.Get(ctx, client.ObjectKey{Name: GatewayName, Namespace: GatewayNamespace}, gw)
		g.Expect(err).NotTo(HaveOccurred())
		programmed := false
		for _, cond := range gw.Status.Conditions {
			if cond.Type == string(gatewayapiv1.GatewayConditionProgrammed) && cond.Status == metav1.ConditionTrue {
				programmed = true
				break
			}
		}
		g.Expect(programmed).To(BeTrue(), "gateway %s should have Programmed=True", GatewayName)
	}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

	By("setting up MCPGatewayExtension with ReferenceGrant")
	defaultMCPGatewayExt = NewMCPGatewayExtensionSetup(k8sClient).
		WithName(MCPExtensionName).
		InNamespace(SystemNamespace).
		TargetingGateway(GatewayName, GatewayNamespace).
		WithSectionName(GatewayListenerName).
		WithPublicHost(gatewayPublicHost).
		WithListenerPort(8443).
		Build()

	defaultMCPGatewayExt.
		Clean(ctx).
		Register(ctx)

	By("waiting for MCPGatewayExtension to become ready")
	Eventually(func(g Gomega) {
		err := VerifyMCPGatewayExtensionReady(ctx, k8sClient, MCPExtensionName, SystemNamespace)
		g.Expect(err).NotTo(HaveOccurred())
	}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

	By("waiting for broker/router deployment to be ready")
	Eventually(func(g Gomega) {
		deployment := &appsv1.Deployment{}
		err := k8sClient.Get(ctx, client.ObjectKey{Name: "mcp-gateway", Namespace: SystemNamespace}, deployment)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(deployment.Status.ReadyReplicas).To(BeNumerically(">=", 1))
	}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

	By("patching broker-router: CA cert for HTTPS listener")
	PatchBrokerCA(ctx, k8sClient, SystemNamespace)
}, func() {
	// runs on every process: set up local k8s client
	setupK8sClient()
})

// SynchronizedAfterSuite: first function runs on every process (local
// cleanup), second function runs on process 1 only (cluster teardown).
var _ = SynchronizedAfterSuite(func() {
	if cancel != nil {
		cancel()
	}
}, func() {
	setupK8sClient()

	By("Tearing down the test environment")

	By("cleaning up gateway CA bundle and deployment patches")
	caBundle := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "gateway-ca-bundle", Namespace: SystemNamespace},
	}
	_ = k8sClient.Delete(ctx, caBundle)
	_ = RemoveDeploymentCommandFlag(ctx, SystemNamespace, "mcp-gateway", "--gateway-ca-cert=/certs/gateway-ca.crt")
	_ = RemoveDeploymentVolumeMount(ctx, SystemNamespace, "mcp-gateway", "gateway-ca")
	_ = RemoveDeploymentVolume(ctx, SystemNamespace, "mcp-gateway", "gateway-ca")

	if defaultMCPGatewayExt != nil {
		defaultMCPGatewayExt.TearDown(ctx)
	}
})

// newTestGatewayClient creates an MCP gateway client targeting the shared gateway
// (gatewayURL). Isolated suites (elicitation, tool-discovery) should create
// clients with their own URLs instead of using this helper.
func newTestGatewayClient() *NotifyingMCPClient {
	var c *NotifyingMCPClient
	Eventually(func(g Gomega) {
		var err error
		c, err = NewMCPGatewayClientWithNotifications(ctx, gatewayURL, nil)
		g.Expect(err).NotTo(HaveOccurred())
	}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	DeferCleanup(func() {
		if c != nil {
			_ = c.Close()
		}
	})
	return c
}

// deferCleanupResources registers DeferCleanup to delete all objects in reverse order.
func deferCleanupResources(resources *[]client.Object) {
	DeferCleanup(func() {
		for i := len(*resources) - 1; i >= 0; i-- {
			CleanupResource(ctx, k8sClient, (*resources)[i])
		}
	})
}
