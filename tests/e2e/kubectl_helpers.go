//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	. "github.com/onsi/gomega"
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

// IsTrustedHeadersEnabled checks if the gateway has trusted headers public key configured
func IsTrustedHeadersEnabled(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "kubectl", "get", "deployment", "-n", SystemNamespace,
		"mcp-gateway", "-o", "jsonpath={.spec.template.spec.containers[0].env[?(@.name=='TRUSTED_HEADER_PUBLIC_KEY')].valueFrom.secretKeyRef.name}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
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
