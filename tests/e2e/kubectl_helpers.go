//go:build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
)

// ScaleDeployment scales a deployment to the specified replicas
func ScaleDeployment(namespace, name string, replicas int) error {
	cmd := exec.Command("kubectl", "scale", "deployment", name,
		"-n", namespace, fmt.Sprintf("--replicas=%d", replicas))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to scale deployment %s: %s: %w", name, string(output), err)
	}
	return nil
}

// WaitForDeploymentReady waits for a deployment to be ready
func WaitForDeploymentReady(namespace, name string, _ int) error {
	cmd := exec.Command("kubectl", "rollout", "status", "deployment", name,
		"-n", namespace, "--timeout=60s")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("deployment %s not ready: %s: %w", name, string(output), err)
	}
	return nil
}

// SetDeploymentEnv sets an environment variable on a deployment
func SetDeploymentEnv(namespace, deploymentName, envVar string) error {
	cmd := exec.Command("kubectl", "set", "env", fmt.Sprintf("deployment/%s", deploymentName),
		"-n", namespace, envVar)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to set env on deployment %s: %s: %w", deploymentName, string(output), err)
	}
	return nil
}

// IsTrustedHeadersEnabled checks if the gateway has trusted headers public key configured
func IsTrustedHeadersEnabled() bool {
	cmd := exec.Command("kubectl", "get", "deployment", "-n", SystemNamespace,
		"mcp-broker-router", "-o", "jsonpath={.spec.template.spec.containers[0].env[?(@.name=='TRUSTED_HEADER_PUBLIC_KEY')].valueFrom.secretKeyRef.name}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}
