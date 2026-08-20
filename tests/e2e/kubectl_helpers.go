//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
)

// ScaleDeployment scales a deployment to the specified replicas
func ScaleDeployment(ctx context.Context, namespace, name string, replicas int) error {
	cmd := exec.CommandContext(ctx, "kubectl", "scale", "deployment", name,
		"-n", namespace, fmt.Sprintf("--replicas=%d", replicas))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to scale deployment %s: %s: %w", name, string(output), err)
	}
	return nil
}

// WaitForDeploymentReady waits for a deployment to be ready
func WaitForDeploymentReady(ctx context.Context, namespace, name string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "rollout", "status", "deployment", name,
		"-n", namespace, "--timeout=60s")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("deployment %s not ready: %s: %w", name, string(output), err)
	}
	return nil
}

// GetDeploymentGeneration returns the current metadata.generation of a deployment
func GetDeploymentGeneration(ctx context.Context, namespace, name string) (string, error) {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "deployment", name,
		"-n", namespace, "-o", "jsonpath={.metadata.generation}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get deployment generation: %s: %w", string(output), err)
	}
	return strings.TrimSpace(string(output)), nil
}

// WaitForDeploymentReplicas waits until a deployment has completed its rollout
// with the expected number of ready replicas. It requires the caller to pass
// the generation from before any changes, so it can detect when the rollout
// has actually started (generation changes) then wait for it to complete.
func WaitForDeploymentReplicas(ctx context.Context, namespace, name string, replicas int, prevGeneration string) error {
	// wait for generation to change (confirming the spec mutation was picked up)
	Eventually(func() string {
		gen, _ := GetDeploymentGeneration(ctx, namespace, name)
		return gen
	}, "30s", "1s").ShouldNot(Equal(prevGeneration),
		fmt.Sprintf("deployment %s generation did not change from %s", name, prevGeneration))

	// now rollout status will correctly block on the new rollout
	cmd := exec.CommandContext(ctx, "kubectl", "rollout", "status", "deployment", name,
		"-n", namespace, "--timeout=120s")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("deployment %s rollout not complete: %s: %w", name, string(output), err)
	}

	// confirm exact ready replica count
	cmd = exec.CommandContext(ctx, "kubectl", "wait", "deployment", name,
		"-n", namespace,
		fmt.Sprintf("--for=jsonpath={.status.readyReplicas}=%d", replicas),
		"--timeout=120s")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("deployment %s readyReplicas != %d: %s: %w",
			name, replicas, string(output), err)
	}
	return nil
}

// RestartDeploymentAndWait triggers a rollout restart on a deployment and waits
// for the new rollout to complete. Unlike deleting pods directly, rollout restart
// changes the deployment generation so rollout status correctly blocks.
func RestartDeploymentAndWait(ctx context.Context, namespace, deploymentName string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "rollout", "restart", "deployment", deploymentName,
		"-n", namespace)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to restart deployment %s: %s: %w", deploymentName, string(output), err)
	}

	cmd = exec.CommandContext(ctx, "kubectl", "rollout", "status", "deployment", deploymentName,
		"-n", namespace, "--timeout=120s")
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("deployment %s not ready after restart: %s: %w", deploymentName, string(output), err)
	}
	return nil
}

// AddDeploymentCommandFlag appends a flag to a deployment's container command array.
func AddDeploymentCommandFlag(ctx context.Context, namespace, deploymentName, flag string) error {
	patch := fmt.Sprintf(`[{"op":"add","path":"/spec/template/spec/containers/0/command/-","value":"%s"}]`, flag)
	cmd := exec.CommandContext(ctx, "kubectl", "patch", "deployment", deploymentName,
		"-n", namespace, "--type=json", "-p", patch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add command flag on deployment %s: %s: %w", deploymentName, string(output), err)
	}
	return nil
}

// RemoveDeploymentCommandFlag removes a flag from a deployment's container command array by value.
func RemoveDeploymentCommandFlag(ctx context.Context, namespace, deploymentName, flag string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "deployment", deploymentName,
		"-n", namespace, "-o", "jsonpath={.spec.template.spec.containers[0].command}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get command array: %s: %w", string(output), err)
	}
	var command []string
	if err := json.Unmarshal(output, &command); err != nil {
		return fmt.Errorf("failed to parse command array: %w: %s", err, string(output))
	}
	idx := -1
	for i, c := range command {
		if c == flag {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil
	}
	patch := fmt.Sprintf(`[{"op":"remove","path":"/spec/template/spec/containers/0/command/%d"}]`, idx)
	cmd = exec.CommandContext(ctx, "kubectl", "patch", "deployment", deploymentName,
		"-n", namespace, "--type=json", "-p", patch)
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to remove command flag on deployment %s: %s: %w", deploymentName, string(output), err)
	}
	return nil
}

// AddGatewayHTTPSListener patches a Gateway to add an HTTPS listener with TLS termination.
// Idempotent: if the listener already exists, it is removed and re-added.
func AddGatewayHTTPSListener(ctx context.Context, namespace, gatewayName, listenerName, hostname, certSecretName string, port int) error {
	// check if listener already exists and remove it first
	checkCmd := exec.CommandContext(ctx, "kubectl", "get", "gateway", gatewayName,
		"-n", namespace, "-o", fmt.Sprintf("jsonpath={.spec.listeners[?(@.name==\"%s\")].name}", listenerName))
	checkOut, _ := checkCmd.CombinedOutput()
	if strings.TrimSpace(string(checkOut)) == listenerName {
		// find the index and remove it
		idxCmd := exec.CommandContext(ctx, "kubectl", "get", "gateway", gatewayName,
			"-n", namespace, "-o", "jsonpath={range .spec.listeners[*]}{.name}{\"\\n\"}{end}")
		idxOut, err := idxCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to list listeners: %s: %w", string(idxOut), err)
		}
		for i, name := range strings.Split(strings.TrimSpace(string(idxOut)), "\n") {
			if name == listenerName {
				rmPatch := fmt.Sprintf(`[{"op":"remove","path":"/spec/listeners/%d"}]`, i)
				rmCmd := exec.CommandContext(ctx, "kubectl", "patch", "gateway", gatewayName,
					"-n", namespace, "--type=json", "-p", rmPatch)
				rmOut, rmErr := rmCmd.CombinedOutput()
				if rmErr != nil {
					return fmt.Errorf("failed to remove existing listener %s: %s: %w", listenerName, string(rmOut), rmErr)
				}
				break
			}
		}
	}

	patch := fmt.Sprintf(`[{"op":"add","path":"/spec/listeners/-","value":{`+
		`"name":"%s","hostname":"%s","port":%d,"protocol":"HTTPS",`+
		`"tls":{"mode":"Terminate","certificateRefs":[{"kind":"Secret","name":"%s"}]},`+
		`"allowedRoutes":{"namespaces":{"from":"All"}}}}]`,
		listenerName, hostname, port, certSecretName)
	cmd := exec.CommandContext(ctx, "kubectl", "patch", "gateway", gatewayName,
		"-n", namespace, "--type=json", "-p", patch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add HTTPS listener to gateway %s: %s: %w", gatewayName, string(output), err)
	}
	return nil
}

// PatchBrokerCA configures the broker-router in the given namespace to trust the
// private CA that signs the gateway's HTTPS listener, so 2025-11-25 hairpin init
// requests succeed. It creates a labeled CA Secret and sets caCertBundleRef on
// the namespace's MCPGatewayExtension. The same bundle is the broker's upstream
// trust pool; concatenate additional PEMs in this Secret when upstream CAs differ.
func PatchBrokerCA(ctx context.Context, k8sClient client.Client, namespace string) {
	ext := getSingleMCPGatewayExtension(ctx, k8sClient, namespace)
	if ext.Spec.CACertBundleRef != nil && ext.Spec.CACertBundleRef.Name == "gateway-ca-bundle" {
		existing := &corev1.Secret{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Name: "gateway-ca-bundle", Namespace: namespace}, existing); err == nil {
			return
		}
	}

	var caCertPEM []byte
	if e2eDomain == defaultE2EDomain {
		// KIND: use cert-manager private CA
		caSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "private-ca-keypair", Namespace: "cert-manager"}, caSecret)).To(Succeed())
		pem, ok := caSecret.Data["ca.crt"]
		Expect(ok).To(BeTrue(), "private-ca-keypair should have ca.crt")
		caCertPEM = pem
	} else {
		// OpenShift/real clusters: source from the trusted CA bundle ConfigMap
		cm := &corev1.ConfigMap{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Name: GatewayCABundleConfigMap, Namespace: SystemNamespace}, cm)).To(Succeed())
		data, ok := cm.Data["ca-bundle.crt"]
		Expect(ok).To(BeTrue(), "%s should have ca-bundle.crt", GatewayCABundleConfigMap)
		caCertPEM = []byte(data)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway-ca-bundle",
			Namespace: namespace,
			Labels:    map[string]string{"mcp.kuadrant.io/secret": "true"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"ca.crt": caCertPEM},
	}
	_ = k8sClient.Delete(ctx, secret)
	Expect(k8sClient.Create(ctx, secret)).To(Succeed())

	if ext.Spec.CACertBundleRef != nil && ext.Spec.CACertBundleRef.Name == "gateway-ca-bundle" {
		return
	}

	patch := []byte(`{"spec":{"caCertBundleRef":{"name":"gateway-ca-bundle","key":"ca.crt"}}}`)
	Expect(k8sClient.Patch(ctx, ext, client.RawPatch(types.MergePatchType, patch))).To(Succeed())

	Eventually(func(g Gomega) {
		g.Expect(VerifyMCPGatewayExtensionReady(ctx, k8sClient, ext.Name, namespace)).To(Succeed())
		cfg := &corev1.Secret{}
		g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "mcp-gateway-config", Namespace: namespace}, cfg)).To(Succeed())
		configYAML, ok := cfg.Data["config.yaml"]
		g.Expect(ok).To(BeTrue(), "config secret should have config.yaml")
		g.Expect(string(configYAML)).To(ContainSubstring("gatewayCACertPEM"))
	}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
}

// getSingleMCPGatewayExtension returns the sole MCPGatewayExtension in a
// namespace. Each test namespace owns exactly one.
func getSingleMCPGatewayExtension(ctx context.Context, k8sClient client.Client, namespace string) *mcpv1.MCPGatewayExtension {
	list := &mcpv1.MCPGatewayExtensionList{}
	Expect(k8sClient.List(ctx, list, client.InNamespace(namespace))).To(Succeed())
	Expect(list.Items).To(HaveLen(1), "expected exactly one MCPGatewayExtension in %s", namespace)
	return &list.Items[0]
}

// PatchDeploymentJSON applies a JSON patch (RFC 6902) to a deployment.
func PatchDeploymentJSON(ctx context.Context, namespace, deploymentName, patchJSON string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "patch", "deployment", deploymentName,
		"-n", namespace, "--type=json", "-p", patchJSON)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to patch deployment %s: %s: %w", deploymentName, string(output), err)
	}
	return nil
}

// SetURLElicitation patches the MCPGatewayExtension to enable or disable URL elicitation.
// The operator reconciles the deployment args and /tokens HTTPRoute automatically.
func SetURLElicitation(namespace, name string, enabled bool) error {
	value := "Disabled"
	if enabled {
		value = "Enabled"
	}
	ctx := context.Background()
	// With v1 and v1alpha1 being structurally identical, we can patch the spec directly.
	patch := fmt.Sprintf(`{"spec":{"urlElicitation":"%s"}}`, value)
	cmd := exec.CommandContext(ctx, "kubectl", "patch", "mcpgatewayextension", name,
		"-n", namespace, "--type=merge", "-p", patch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to patch mcpgatewayextension %s: %s: %w", name, string(output), err)
	}
	return nil
}

// IsTrustedHeadersEnabled checks if the gateway has trusted headers public key configured
func IsTrustedHeadersEnabled(ctx context.Context) bool {
	return IsTrustedHeadersEnabledInNamespace(ctx, SystemNamespace)
}

// IsTrustedHeadersEnabledInNamespace checks if trusted headers auth is enabled on the deployment in the given namespace.
func IsTrustedHeadersEnabledInNamespace(ctx context.Context, namespace string) bool {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "deployment", "-n", namespace,
		"mcp-gateway", "-o", "jsonpath={.spec.template.spec.containers[0].env[?(@.name=='TRUSTED_HEADER_PUBLIC_KEY')].valueFrom.secretKeyRef.name}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}

// SetOAuthProtectedResource patches the MCPGatewayExtension to set the oauthProtectedResource field.
func SetOAuthProtectedResource(ctx context.Context, namespace, name string, authServers []string) error {
	servers, err := json.Marshal(authServers)
	if err != nil {
		return fmt.Errorf("failed to marshal authServers: %w", err)
	}
	patch := fmt.Sprintf(`{"spec":{"oauthProtectedResource":{"authorizationServers":%s}}}`, servers)
	cmd := exec.CommandContext(ctx, "kubectl", "patch", "mcpgatewayextension", name,
		"-n", namespace, "--type=merge", "-p", patch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to patch mcpgatewayextension %s: %s: %w", name, string(output), err)
	}
	return nil
}

// ClearOAuthProtectedResource removes the oauthProtectedResource field from the MCPGatewayExtension.
func ClearOAuthProtectedResource(ctx context.Context, namespace, name string) error {
	patch := `{"spec":{"oauthProtectedResource":null}}`
	cmd := exec.CommandContext(ctx, "kubectl", "patch", "mcpgatewayextension", name,
		"-n", namespace, "--type=merge", "-p", patch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to clear oauthProtectedResource on %s: %s: %w", name, string(output), err)
	}
	return nil
}

// IsAuthPolicyConfigured checks if AuthPolicy resources exist in the gateway namespace
func IsAuthPolicyConfigured(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "authpolicy", "-n", GatewayNamespace,
		"-o", "jsonpath={.items[*].metadata.name}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}

// DumpClusterState dumps gateway-related resources for post-failure debugging.
// Writes to a temp file and prints the path; falls back to GinkgoWriter on file errors.
func DumpClusterState(ctx context.Context, namespaces ...string) {
	resources := []string{
		"gateways.gateway.networking.k8s.io",
		"httproutes.gateway.networking.k8s.io",
		"mcpgatewayextensions.mcp.kuadrant.io",
		"mcpserverregistrations.mcp.kuadrant.io",
		"envoyfilters.networking.istio.io",
	}

	f, err := os.CreateTemp("", "e2e-cluster-dump-*.txt")
	if err != nil {
		GinkgoWriter.Printf("failed to create dump file, falling back to stdout: %v\n", err)
		dumpClusterStateTo(ctx, GinkgoWriter, resources, namespaces)
		return
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriter(f)
	dumpClusterStateTo(ctx, w, resources, namespaces)
	_ = w.Flush()

	GinkgoWriter.Printf("cluster state dumped to: %s\n", f.Name())
}

func dumpClusterStateTo(ctx context.Context, w io.Writer, resources, namespaces []string) {
	p := func(format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

	p("=== CLUSTER STATE DUMP (post-failure) ===\n")
	p("test: %s\n", CurrentSpecReport().FullText())
	p("time: %s\n\n", time.Now().UTC().Format(time.RFC3339))

	for _, res := range resources {
		cmd := exec.CommandContext(ctx, "kubectl", "get", res, "--all-namespaces", "-o", "wide")
		output, err := cmd.CombinedOutput()
		if err != nil {
			p("  [%s] error: %v\n", res, err)
			continue
		}
		p("\n--- %s ---\n%s", res, string(output))
	}

	for _, ns := range namespaces {
		p("\n--- config secret in %s (metadata only) ---\n", ns)
		cmd := exec.CommandContext(ctx, "kubectl", "get", "secret", "mcp-gateway-config",
			"-n", ns, "-o", "jsonpath={.metadata.name}{\"\\t\"}{.metadata.namespace}{\"\\t\"}{.metadata.creationTimestamp}{\"\\t\"}{.metadata.resourceVersion}{\"\\n\"}")
		output, err := cmd.CombinedOutput()
		if err != nil {
			p("  error: %v\n", err)
		} else {
			p("  name\tnamespace\tcreated\tresourceVersion\n")
			p("  %s\n", string(output))
		}

		p("\n--- pods in %s ---\n", ns)
		cmd = exec.CommandContext(ctx, "kubectl", "get", "pods", "-n", ns, "-o", "wide")
		output, _ = cmd.CombinedOutput()
		p("%s\n", string(output))

		p("\n--- deployments in %s ---\n", ns)
		cmd = exec.CommandContext(ctx, "kubectl", "get", "deployments", "-n", ns, "-o", "wide")
		output, _ = cmd.CombinedOutput()
		p("%s\n", string(output))
	}

	p("=== END CLUSTER STATE DUMP ===\n")
}
