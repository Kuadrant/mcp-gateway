//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
func AddGatewayHTTPSListener(ctx context.Context, namespace, gatewayName, listenerName, hostname, certSecretName string, port int) error {
	// check if listener already exists
	cmd := exec.CommandContext(ctx, "kubectl", "get", "gateway", gatewayName,
		"-n", namespace, "-o", fmt.Sprintf("jsonpath={.spec.listeners[?(@.name==\"%s\")].name}", listenerName))
	out, err := cmd.CombinedOutput()
	if err == nil && strings.TrimSpace(string(out)) == listenerName {
		return nil
	}

	patch := fmt.Sprintf(`[{"op":"add","path":"/spec/listeners/-","value":{`+
		`"name":"%s","hostname":"%s","port":%d,"protocol":"HTTPS",`+
		`"tls":{"mode":"Terminate","certificateRefs":[{"kind":"Secret","name":"%s"}]},`+
		`"allowedRoutes":{"namespaces":{"from":"All"}}}}]`,
		listenerName, hostname, port, certSecretName)
	cmd = exec.CommandContext(ctx, "kubectl", "patch", "gateway", gatewayName,
		"-n", namespace, "--type=json", "-p", patch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add HTTPS listener to gateway %s: %s: %w", gatewayName, string(output), err)
	}
	return nil
}

// PatchBrokerCA copies the private CA cert into the given namespace and patches
// the broker-router deployment to trust it for HTTPS hairpin requests.
func PatchBrokerCA(ctx context.Context, k8sClient client.Client, namespace string) {
	deployment := &appsv1.Deployment{}
	if err := k8sClient.Get(ctx, client.ObjectKey{Name: "mcp-gateway", Namespace: namespace}, deployment); err == nil {
		for _, v := range deployment.Spec.Template.Spec.Volumes {
			if v.Name == "gateway-ca" {
				return
			}
		}
	}

	caSecret := &corev1.Secret{}
	Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "private-ca-keypair", Namespace: "cert-manager"}, caSecret)).To(Succeed())
	caCertPEM, ok := caSecret.Data["ca.crt"]
	Expect(ok).To(BeTrue(), "private-ca-keypair should have ca.crt")

	caBundle := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway-ca-bundle",
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"ca.crt": caCertPEM},
	}
	_ = k8sClient.Delete(ctx, caBundle)
	Expect(k8sClient.Create(ctx, caBundle)).To(Succeed())

	combinedPatch := `[` +
		`{"op":"add","path":"/spec/template/spec/volumes/-","value":{"name":"gateway-ca","secret":{"secretName":"gateway-ca-bundle"}}},` +
		`{"op":"add","path":"/spec/template/spec/containers/0/volumeMounts/-","value":{"name":"gateway-ca","mountPath":"/certs/gateway-ca.crt","subPath":"ca.crt","readOnly":true}},` +
		`{"op":"add","path":"/spec/template/spec/containers/0/command/-","value":"--gateway-ca-cert=/certs/gateway-ca.crt"}` +
		`]`
	Expect(PatchDeploymentJSON(ctx, namespace, "mcp-gateway", combinedPatch)).To(Succeed())
	Expect(WaitForDeploymentReady(ctx, namespace, "mcp-gateway")).To(Succeed())
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

// RemoveDeploymentVolume removes a volume by name from a deployment's pod spec.
func RemoveDeploymentVolume(ctx context.Context, namespace, deploymentName, volumeName string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "deployment", deploymentName,
		"-n", namespace, "-o", "jsonpath={.spec.template.spec.volumes}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get volumes: %s: %w", string(output), err)
	}
	var volumes []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(output, &volumes); err != nil {
		return fmt.Errorf("failed to parse volumes: %w: %s", err, string(output))
	}
	idx := -1
	for i, v := range volumes {
		if v.Name == volumeName {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil
	}
	patch := fmt.Sprintf(`[{"op":"remove","path":"/spec/template/spec/volumes/%d"}]`, idx)
	return PatchDeploymentJSON(ctx, namespace, deploymentName, patch)
}

// RemoveDeploymentVolumeMount removes a volume mount by name from the first container.
func RemoveDeploymentVolumeMount(ctx context.Context, namespace, deploymentName, mountName string) error {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "deployment", deploymentName,
		"-n", namespace, "-o", "jsonpath={.spec.template.spec.containers[0].volumeMounts}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to get volumeMounts: %s: %w", string(output), err)
	}
	var mounts []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(output, &mounts); err != nil {
		return fmt.Errorf("failed to parse volumeMounts: %w: %s", err, string(output))
	}
	idx := -1
	for i, m := range mounts {
		if m.Name == mountName {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil
	}
	patch := fmt.Sprintf(`[{"op":"remove","path":"/spec/template/spec/containers/0/volumeMounts/%d"}]`, idx)
	return PatchDeploymentJSON(ctx, namespace, deploymentName, patch)
}

// SetURLElicitation patches the MCPGatewayExtension to enable or disable URL elicitation.
// The operator reconciles the deployment args and /tokens HTTPRoute automatically.
func SetURLElicitation(namespace, name string, enabled bool) error {
	value := "Disabled"
	if enabled {
		value = "Enabled"
	}
	ctx := context.Background()
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
