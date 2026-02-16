//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	istionetv1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
)

var (
	k8sClient            client.Client
	testScheme           *runtime.Scheme
	cfg                  *rest.Config
	ctx                  context.Context
	cancel               context.CancelFunc
	mcpGatewayClient     *NotifyingMCPClient
	defaultMCPGatewayExt *MCPGatewayExtensionSetup
	gatewayPublicHost    = "mcp.127-0-0-1.sslip.io"
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "MCP Gateway E2E Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(logr.FromSlogHandler(slog.NewTextHandler(GinkgoWriter, &slog.HandlerOptions{Level: slog.LevelDebug})))

	ctx, cancel = context.WithCancel(context.Background())

	By("Setting up test scheme")
	testScheme = runtime.NewScheme()
	Expect(scheme.AddToScheme(testScheme)).To(Succeed())
	Expect(mcpv1alpha1.AddToScheme(testScheme)).To(Succeed())
	Expect(gatewayapiv1.Install(testScheme)).To(Succeed())
	Expect(gatewayv1beta1.Install(testScheme)).To(Succeed())
	Expect(istionetv1beta1.AddToScheme(testScheme)).To(Succeed())

	By("Getting kubeconfig")
	var err error
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home := os.Getenv("HOME")
		kubeconfig = filepath.Join(home, ".kube", "config")
	}

	cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).ToNot(HaveOccurred())

	By("Creating Kubernetes client")
	k8sClient, err = client.New(cfg, client.Options{Scheme: testScheme})
	Expect(err).ToNot(HaveOccurred())

	By("Verifying cluster connection")
	namespaceList := &corev1.NamespaceList{}
	Expect(k8sClient.List(ctx, namespaceList)).To(Succeed())

	By("Checking test namespace exists")
	testNs := &corev1.Namespace{}
	err = k8sClient.Get(ctx, client.ObjectKey{Name: TestServerNameSpace}, testNs)
	if err != nil {
		GinkgoWriter.Printf("Warning: test namespace %s does not exist, tests may fail\n", TestServerNameSpace)
	}

	By("Checking system namespace exists")
	systemNs := &corev1.Namespace{}
	err = k8sClient.Get(ctx, client.ObjectKey{Name: SystemNamespace}, systemNs)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("System namespace %s must exist", SystemNamespace))

	By("cleaning up all existing mcpserverregistrations")

	err = k8sClient.DeleteAllOf(ctx, &mcpv1alpha1.MCPServerRegistration{}, client.InNamespace(TestServerNameSpace), &client.DeleteAllOfOptions{ListOptions: client.ListOptions{
		LabelSelector: labels.Everything(),
	}})
	Expect(err).ToNot(HaveOccurred(), "all existing MCPSevers should be removed before the e2e test suite")

	By("cleaning up all http routes")
	err = k8sClient.DeleteAllOf(ctx, &gatewayapiv1.HTTPRoute{}, client.InNamespace(TestServerNameSpace))
	Expect(err).ToNot(HaveOccurred(), "all existing HTTPRoutes should be removed before the e2e test suite")

	By("setting up MCPGatewayExtension with ReferenceGrant")
	defaultMCPGatewayExt = NewMCPGatewayExtensionSetup(k8sClient).
		WithName(MCPExtensionName).
		InNamespace(SystemNamespace).
		TargetingGateway(GatewayName, GatewayNamespace).
		WithPublicHost(gatewayPublicHost).
		WithHTTPRoute().
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

	By("setting up an mcp client for the gateway")
	Eventually(func(g Gomega) {
		var clientErr error
		mcpGatewayClient, clientErr = NewMCPGatewayClientWithNotifications(ctx, gatewayURL, func(j mcp.JSONRPCNotification) {})
		g.Expect(clientErr).Error().NotTo(HaveOccurred())
	}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

})

var _ = AfterSuite(func() {
	By("Tearing down the test environment")
	if mcpGatewayClient != nil {
		GinkgoWriter.Println("closing client")
		mcpGatewayClient.Close()
	}

	if defaultMCPGatewayExt != nil {
		defaultMCPGatewayExt.TearDown(ctx)
	}

	if cancel != nil {
		cancel()
	}
})
