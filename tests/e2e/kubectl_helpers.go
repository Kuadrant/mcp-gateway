//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	. "github.com/onsi/gomega"
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
func WaitForDeploymentReplicas(namespace, name string, replicas int, prevGeneration string) {
	// wait for generation to change (confirming the spec mutation was picked up)
	Eventually(func(g Gomega) {
		gen, err := GetDeploymentGeneration(namespace, name)
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(gen).NotTo(Equal(prevGeneration))
	}, "30s", "1s").Should(Succeed(),
		fmt.Sprintf("deployment %s generation did not change from %s", name, prevGeneration))

	// now rollout status will correctly block on the new rollout
	cmd := exec.Command("kubectl", "rollout", "status", "deployment", name,
		"-n", namespace, "--timeout=120s")
	output, err := cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("deployment %s rollout not complete: %s", name, string(output)))

	// confirm exact ready replica count
	cmd = exec.Command("kubectl", "wait", "deployment", name,
		"-n", namespace,
		fmt.Sprintf("--for=jsonpath={.status.readyReplicas}=%d", replicas),
		"--timeout=120s")
	output, err = cmd.CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("deployment %s readyReplicas != %d: %s", name, replicas, string(output)))
}

// RestartDeploymentAndWait triggers a rollout restart on a deployment and waits
// for the new rollout to complete. Unlike deleting pods directly, rollout restart
// changes the deployment generation so rollout status correctly blocks.
func RestartDeploymentAndWait(namespace, deploymentName string) error {
	cmd := exec.Command("kubectl", "rollout", "restart", "deployment", deploymentName,
		"-n", namespace)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to restart deployment %s: %s: %w", deploymentName, string(output), err)
	}

	cmd = exec.Command("kubectl", "rollout", "status", "deployment", deploymentName,
		"-n", namespace, "--timeout=120s")
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("deployment %s not ready after restart: %s: %w", deploymentName, string(output), err)
	}
	return nil
}

// AddDeploymentCommandFlag appends a flag to a deployment's container command array.
// The flag is not reverted by the controller if it is in the ignoredCommandFlags list.
func AddDeploymentCommandFlag(namespace, deploymentName, flag string) error {
	patch := fmt.Sprintf(`[{"op":"add","path":"/spec/template/spec/containers/0/command/-","value":"%s"}]`, flag)
	cmd := exec.Command("kubectl", "patch", "deployment", deploymentName,
		"-n", namespace, "--type=json", "-p", patch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to add command flag on deployment %s: %s: %w", deploymentName, string(output), err)
	}
	return nil
}

// RemoveDeploymentCommandFlag removes a flag from a deployment's container command array by value.
func RemoveDeploymentCommandFlag(namespace, deploymentName, flag string) error {
	cmd := exec.Command("kubectl", "get", "deployment", deploymentName,
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
		return nil // flag not present, nothing to do
	}
	patch := fmt.Sprintf(`[{"op":"remove","path":"/spec/template/spec/containers/0/command/%d"}]`, idx)
	cmd = exec.Command("kubectl", "patch", "deployment", deploymentName,
		"-n", namespace, "--type=json", "-p", patch)
	output, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to remove command flag on deployment %s: %s: %w", deploymentName, string(output), err)
	}
	return nil
}

// IsTrustedHeadersEnabled checks if the gateway has trusted headers public key configured
func IsTrustedHeadersEnabled() bool {
	cmd := exec.Command("kubectl", "get", "deployment", "-n", SystemNamespace,
		"mcp-gateway", "-o", "jsonpath={.spec.template.spec.containers[0].env[?(@.name=='TRUSTED_HEADER_PUBLIC_KEY')].valueFrom.secretKeyRef.name}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}

// IsAuthPolicyConfigured checks if AuthPolicy resources exist in the gateway namespace
func IsAuthPolicyConfigured() bool {
	cmd := exec.Command("kubectl", "get", "authpolicy", "-n", GatewayNamespace,
		"-o", "jsonpath={.items[*].metadata.name}")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) != ""
}
