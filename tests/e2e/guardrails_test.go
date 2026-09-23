//go:build e2e

package e2e

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Kuadrant/mcp-gateway/internal/guardrails"
)

const (
	nemoGuardrailsExtName    = "nemo-guardrails-ext"
	nemoGuardrailsNamespace  = "mcp-nemo-guardrails"
	nemoGuardrailsSecretName = "nemo-guardrails-config"

	// annotation names the controller reads for guardrails wiring
	// (internal/controller/mcpgatewayextension_controller.go,
	// internal/controller/mcpserverregistration_controller.go).
	guardrailsRefAnnotation       = "mcp.kuadrant.io/guardrails-ref"
	guardrailsConfigIDsAnnotation = "mcp.kuadrant.io/guardrails-config-ids"
)

// createNemoGuardrailsSecret builds the Secret the gateway's guardrails-ref
// annotation resolves: nemo-guardrails-custom's URL/model/config ID, matching
// the test infra deployed by config/test-servers/nemo-guardrails-*.yaml.
func createNemoGuardrailsSecret(namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nemoGuardrailsSecretName,
			Namespace: namespace,
			Labels:    map[string]string{"mcp.kuadrant.io/secret": "true", "e2e": "test"},
		},
		Type: guardrails.SecretTypeNeMo,
		StringData: map[string]string{
			"config.yaml": "url: http://nemo-guardrails-custom." + TestServerNameSpace + ".svc.cluster.local:8000\n" +
				"model: phi3-judge\n" +
				"configIDs:\n" +
				"  - phi3-judge\n" +
				"failMode: deny\n",
		},
	}
}

// toolCaller is satisfied by both *mcp.ClientSession (stateful/stateless
// clients) and *NotifyingMCPClient, letting the same assertion helper cover
// both the 2025-11-25 and 2026-07-28 routers.
type toolCaller interface {
	CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error)
}

// callGuardedToolAndExpectVerdict calls prefix+"hello_world" through c and
// accepts a normal response or a guardrails block in either form: a
// request-phase block is a JSON-RPC error, a response-phase block is an
// isError tool result. llm-d-inference-sim is a response simulator, not a
// real judge model, so its allow/block verdict isn't meaningful - only that
// the check round-trips end-to-end (gateway -> NeMo -> sim -> back to the
// client) is.
func callGuardedToolAndExpectVerdict(c toolCaller, toolName string) {
	res, err := c.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: map[string]string{"name": "e2e"},
	})
	if err != nil {
		Expect(err.Error()).To(ContainSubstring("blocked by guardrails"), "unexpected error: %v", err)
		return
	}
	Expect(res).NotTo(BeNil())
	Expect(res.Content).NotTo(BeEmpty())
	if res.IsError {
		text, ok := res.Content[0].(*mcp.TextContent)
		Expect(ok).To(BeTrue(), "tool call failed with non-text content: %v", res.Content)
		Expect(text.Text).To(Equal("blocked by guardrails"), "tool call failed: %s", text.Text)
	}
}

// Registers mcp-test-stateless-server (2025+2026 dual-protocol) once so both
// router paths can be exercised against the same backend and guardrails
// config, without repeating registration/readiness setup per protocol.
var _ = Describe("NeMo Guardrails", Ordered, func() {
	var (
		testResources []client.Object
		nemoExt       *MCPGatewayExtensionSetup
		nemoURL       = NemoGuardrailsGatewayURL
	)

	BeforeAll(func() {
		By("waiting for NeMo guardrails test infra (llm-d-inference-sim, nemo-guardrails-custom) to be ready")
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, TestServerNameSpace, "llm-d-inference-sim")).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, TestServerNameSpace, "nemo-guardrails-custom")).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("creating nemo-guardrails namespace")
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   nemoGuardrailsNamespace,
				Labels: map[string]string{"e2e": "test"},
			},
		}
		_ = k8sClient.Delete(ctx, ns)
		Eventually(func(g Gomega) {
			err := k8sClient.Create(ctx, ns)
			g.Expect(client.IgnoreAlreadyExists(err)).NotTo(HaveOccurred())
		}, TestTimeoutShort, TestRetryInterval).Should(Succeed())

		By("creating the guardrails config Secret")
		secret := createNemoGuardrailsSecret(nemoGuardrailsNamespace)
		_ = k8sClient.Delete(ctx, secret)
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		By("creating MCPGatewayExtension targeting the nemo-guardrails listener, with guardrails-ref set")
		nemoExt = NewMCPGatewayExtensionSetup(k8sClient).
			WithName(nemoGuardrailsExtName).
			InNamespace(nemoGuardrailsNamespace).
			TargetingGateway(GatewayName, GatewayNamespace).
			WithSectionName(NemoGuardrailsListenerName).
			WithPublicHost(NemoGuardrailsPublicHost).
			Build()
		nemoExt.GetExtension().Annotations = map[string]string{guardrailsRefAnnotation: nemoGuardrailsSecretName}
		nemoExt.Clean(ctx).Register(ctx)

		By("waiting for the MCPGatewayExtension to resolve guardrails and become ready")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPGatewayExtensionReady(ctx, k8sClient, nemoGuardrailsExtName, nemoGuardrailsNamespace)).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("waiting for broker/router deployment to be ready")
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, nemoGuardrailsNamespace, "mcp-gateway")).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("registering the dual-protocol backend under the gateway's guardrails config (no per-server override)")
		regGW := NewTestResources("nemo-gw", k8sClient).
			InNamespace(nemoGuardrailsNamespace).
			WithBackendTarget("mcp-test-stateless-server", 9090).
			WithBackendNamespace(TestServerNameSpace).
			WithHostname(NemoGuardrailsServerHost).
			WithPrefix("nemo_gw_").
			WithSectionName(NemoGuardrailsListenerName).
			WithParentGateway(GatewayName, GatewayNamespace).
			Build()
		testResources = append(testResources, regGW.GetObjects()...)
		serverGW := regGW.Register(ctx)

		By("registering the dual-protocol backend again with a per-server guardrails-config-ids override")
		regSrv := NewTestResources("nemo-server", k8sClient).
			InNamespace(nemoGuardrailsNamespace).
			WithBackendTarget("mcp-test-stateless-server", 9090).
			WithBackendNamespace(TestServerNameSpace).
			WithHostname(NemoGuardrailsServerHost).
			WithPrefix("nemo_srv_").
			WithSectionName(NemoGuardrailsListenerName).
			WithParentGateway(GatewayName, GatewayNamespace).
			Build()
		regSrv.GetMCPServer().Annotations = map[string]string{guardrailsConfigIDsAnnotation: "phi3-judge"}
		testResources = append(testResources, regSrv.GetObjects()...)
		serverSrv := regSrv.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, serverGW.Name, serverGW.Namespace)).To(Succeed())
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, serverSrv.Name, serverSrv.Namespace)).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})

	AfterAll(func() {
		for _, obj := range testResources {
			CleanupResource(ctx, k8sClient, obj)
		}
		if nemoExt != nil {
			nemoExt.TearDown(ctx)
		}
		_ = k8sClient.Delete(ctx, createNemoGuardrailsSecret(nemoGuardrailsNamespace))
	})

	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			DumpClusterState(ctx, nemoGuardrailsNamespace, TestServerNameSpace, GatewayNamespace)
		}
	})

	newStatefulGuardrailsClient := func() *mcp.ClientSession {
		var c *mcp.ClientSession
		Eventually(func(g Gomega) {
			var err error
			c, err = NewStatefulClient(ctx, nemoURL)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		DeferCleanup(func() { _ = c.Close() })
		return c
	}

	newStatelessGuardrailsClient := func() *mcp.ClientSession {
		var c *mcp.ClientSession
		Eventually(func(g Gomega) {
			var err error
			c, err = NewStatelessClient(ctx, nemoURL)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
		DeferCleanup(func() { _ = c.Close() })
		return c
	}

	It("[Full,NemoGuardrails] 2025-11-25 router: tools/call round-trips through the gateway-level NeMo guardrails check", func() {
		c := newStatefulGuardrailsClient()
		WaitForToolsWithPrefix(ctx, c, "nemo_gw_")
		callGuardedToolAndExpectVerdict(c, "nemo_gw_hello_world")
	})

	It("[Full,NemoGuardrails] 2026-07-28 router: tools/call round-trips through the gateway-level NeMo guardrails check", func() {
		c := newStatelessGuardrailsClient()
		WaitForToolsWithPrefix(ctx, c, "nemo_gw_")
		callGuardedToolAndExpectVerdict(c, "nemo_gw_hello_world")
	})

	It("[Full,NemoGuardrails] 2025-11-25 router: per-server guardrails-config-ids annotation merges without breaking tools/call", func() {
		c := newStatefulGuardrailsClient()
		WaitForToolsWithPrefix(ctx, c, "nemo_srv_")
		callGuardedToolAndExpectVerdict(c, "nemo_srv_hello_world")
	})

	It("[Full,NemoGuardrails] 2026-07-28 router: per-server guardrails-config-ids annotation merges without breaking tools/call", func() {
		c := newStatelessGuardrailsClient()
		WaitForToolsWithPrefix(ctx, c, "nemo_srv_")
		callGuardedToolAndExpectVerdict(c, "nemo_srv_hello_world")
	})
})
