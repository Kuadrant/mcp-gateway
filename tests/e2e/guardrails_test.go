//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

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

	guardrailsRefAnnotation       = "mcp.kuadrant.io/guardrails-ref"
	guardrailsConfigIDsAnnotation = "mcp.kuadrant.io/guardrails-config-ids"

	// nemo-guardrails-custom answers any check naming this with a 422
	unknownGuardrailsConfigID = "e2e-unknown-config"
	// router's fail-closed error text
	guardrailsUnavailableMessage = "guardrails check unavailable"
)

// createNemoGuardrailsSecret is the Secret guardrails-ref resolves, pointed at the test NeMo server.
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

// toolCaller lets one helper cover both the stateful and stateless SDK clients.
type toolCaller interface {
	CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error)
}

// simCompletions sums the sim's vllm:request_success_total, read through the API
// server proxy. every NeMo check calls the sim, so a rise means the check ran.
func simCompletions() (int, error) {
	out, err := exec.CommandContext(ctx, "kubectl", "get", "--raw",
		"/api/v1/namespaces/"+TestServerNameSpace+"/services/llm-d-inference-sim:8032/proxy/metrics").Output()
	if err != nil {
		return 0, fmt.Errorf("reading llm-d-inference-sim metrics: %w", err)
	}
	total := 0
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "vllm:request_success_total{") {
			continue
		}
		fields := strings.Fields(line)
		v, err := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err != nil {
			return 0, fmt.Errorf("parsing %q: %w", line, err)
		}
		total += int(v)
	}
	return total, nil
}

// callGuardedToolAndExpectVerdict accepts an allowed result or a block, as a
// JSON-RPC error or an isError result. the sim's verdict is meaningless; the
// assertion is that its completion count rose, so a skipped check can't pass.
func callGuardedToolAndExpectVerdict(c toolCaller, toolName string) {
	before, err := simCompletions()
	Expect(err).NotTo(HaveOccurred())

	res, err := c.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: map[string]string{"name": "e2e"},
	})
	if err != nil {
		Expect(err.Error()).To(ContainSubstring("blocked by guardrails"), "unexpected error: %v", err)
	} else {
		Expect(res).NotTo(BeNil())
		Expect(res.Content).NotTo(BeEmpty())
		if res.IsError {
			text, ok := res.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue(), "tool call failed with non-text content: %v", res.Content)
			Expect(text.Text).To(Equal("blocked by guardrails"), "tool call failed: %s", text.Text)
		}
	}

	Eventually(simCompletions, TestTimeoutShort, TestRetryInterval).Should(BeNumerically(">", before),
		"no guardrails check reached NeMo's judge model for %s", toolName)
}

// one dual-protocol backend, exercised by both routers.
var _ = Describe("NeMo Guardrails", Ordered, func() {
	var (
		testResources []client.Object
		nemoExt       *MCPGatewayExtensionSetup
		nemoURL       = NemoGuardrailsGatewayURL
	)

	BeforeAll(func() {
		By("waiting for NeMo guardrails test infra (llm-d-inference-sim, nemo-guardrails-custom) to be ready")
		const notDeployed = "not ready; deploy with make deploy-nemo-guardrails-test-servers"
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, TestServerNameSpace, "llm-d-inference-sim")).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed(), "llm-d-inference-sim "+notDeployed)
		Eventually(func(g Gomega) {
			g.Expect(WaitForDeploymentReady(ctx, TestServerNameSpace, "nemo-guardrails-custom")).To(Succeed())
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed(), "nemo-guardrails-custom "+notDeployed)

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

		By("registering the dual-protocol backend a third time with a per-server config ID NeMo doesn't have")
		regUnknown := NewTestResources("nemo-unknown", k8sClient).
			InNamespace(nemoGuardrailsNamespace).
			WithBackendTarget("mcp-test-stateless-server", 9090).
			WithBackendNamespace(TestServerNameSpace).
			WithHostname(NemoGuardrailsServerHost).
			WithPrefix("nemo_unknown_").
			WithSectionName(NemoGuardrailsListenerName).
			WithParentGateway(GatewayName, GatewayNamespace).
			Build()
		regUnknown.GetMCPServer().Annotations = map[string]string{guardrailsConfigIDsAnnotation: unknownGuardrailsConfigID}
		testResources = append(testResources, regUnknown.GetObjects()...)
		serverUnknown := regUnknown.Register(ctx)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, serverGW.Name, serverGW.Namespace)).To(Succeed())
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, serverSrv.Name, serverSrv.Namespace)).To(Succeed())
			g.Expect(VerifyMCPServerRegistrationReady(ctx, k8sClient, serverUnknown.Name, serverUnknown.Namespace)).To(Succeed())
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

	It("[Full,NemoGuardrails] 2025-11-25 router: tools/call still round-trips with a per-server guardrails-config-ids annotation set", func() {
		c := newStatefulGuardrailsClient()
		WaitForToolsWithPrefix(ctx, c, "nemo_srv_")
		callGuardedToolAndExpectVerdict(c, "nemo_srv_hello_world")
	})

	It("[Full,NemoGuardrails] 2026-07-28 router: tools/call still round-trips with a per-server guardrails-config-ids annotation set", func() {
		c := newStatelessGuardrailsClient()
		WaitForToolsWithPrefix(ctx, c, "nemo_srv_")
		callGuardedToolAndExpectVerdict(c, "nemo_srv_hello_world")
	})

	// a 503 only happens if the per-server ID reached NeMo. raw HTTP: the SDK drops a 503 body.
	It("[Full,NemoGuardrails] 2025-11-25 router: a per-server config ID NeMo rejects fails the call closed", func() {
		WaitForToolsWithPrefix(ctx, newStatefulGuardrailsClient(), "nemo_unknown_")

		sessionID, err := mcpInitialize(ctx, nemoURL, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(mcpNotifyInitialized(ctx, nemoURL, sessionID, nil)).To(Succeed())

		status, _, err := mcpCallTool(ctx, nemoURL, sessionID, "nemo_unknown_hello_world", map[string]any{"name": "e2e"}, nil)
		Expect(status).To(Equal(http.StatusServiceUnavailable), "tools/call: %v", err)
		Expect(err).To(MatchError(ContainSubstring(guardrailsUnavailableMessage)))
	})

	It("[Full,NemoGuardrails] 2026-07-28 router: a per-server config ID NeMo rejects fails the call closed", func() {
		WaitForToolsWithPrefix(ctx, newStatelessGuardrailsClient(), "nemo_unknown_")

		const toolName = "nemo_unknown_hello_world"
		body, err := mcp2026Payload("tools/call", map[string]any{
			"name":      toolName,
			"arguments": map[string]any{"name": "e2e"},
		})
		Expect(err).NotTo(HaveOccurred())

		status, respBody, err := mcp2026RawPost(ctx, nemoURL, body, mcp2026Headers("tools/call", toolName))
		Expect(err).NotTo(HaveOccurred())
		Expect(status).To(Equal(http.StatusServiceUnavailable), "tools/call body: %s", respBody)
		Expect(respBody).To(ContainSubstring(guardrailsUnavailableMessage))
	})
})
