//go:build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
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

// GetDeploymentGeneration returns the current metadata.generation of a deployment
func GetDeploymentGeneration(namespace, name string) (string, error) {
	cmd := exec.Command("kubectl", "get", "deployment", name,
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
func WaitForDeploymentReplicas(namespace, name string, replicas int, prevGeneration string) error {
	// wait for generation to change (confirming the spec mutation was picked up)
	for i := 0; i < 30; i++ {
		gen, err := GetDeploymentGeneration(namespace, name)
		if err != nil {
			return err
		}
		if gen != prevGeneration {
			break
		}
		if i == 29 {
			return fmt.Errorf("deployment %s generation did not change from %s after 30s", name, prevGeneration)
		}
		time.Sleep(time.Second)
	}

	// now rollout status will correctly block on the new rollout
	cmd := exec.Command("kubectl", "rollout", "status", "deployment", name,
		"-n", namespace, "--timeout=120s")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("deployment %s rollout not complete: %s: %w", name, string(output), err)
	}

	// confirm exact ready replica count
	cmd = exec.Command("kubectl", "wait", "deployment", name,
		"-n", namespace,
		fmt.Sprintf("--for=jsonpath={.status.readyReplicas}=%d", replicas),
		"--timeout=120s")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("deployment %s readyReplicas != %d: %s: %w",
			name, replicas, string(output), err)
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
