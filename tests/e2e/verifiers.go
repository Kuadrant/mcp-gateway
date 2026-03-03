//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
)

// Verifier provides helper methods for verifying resource states in tests
type Verifier struct {
	ctx       context.Context
	k8sClient client.Client
}

// NewVerifier creates a new Verifier
func NewVerifier(ctx context.Context, k8sClient client.Client) *Verifier {
	return &Verifier{
		ctx:       ctx,
		k8sClient: k8sClient,
	}
}

// getMCPServerRegistration fetches an MCPServerRegistration by name and namespace
func (v *Verifier) getMCPServerRegistration(name, namespace string) (*mcpv1alpha1.MCPServerRegistration, error) {
	mcpServer := &mcpv1alpha1.MCPServerRegistration{}
	err := v.k8sClient.Get(v.ctx, types.NamespacedName{Name: name, Namespace: namespace}, mcpServer)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCPServerRegistration %s/%s: %w", namespace, name, err)
	}
	return mcpServer, nil
}

// getHTTPRoute fetches an HTTPRoute by name and namespace
func (v *Verifier) getHTTPRoute(name, namespace string) (*gatewayapiv1.HTTPRoute, error) {
	httpRoute := &gatewayapiv1.HTTPRoute{}
	err := v.k8sClient.Get(v.ctx, types.NamespacedName{Name: name, Namespace: namespace}, httpRoute)
	if err != nil {
		return nil, fmt.Errorf("failed to get HTTPRoute %s/%s: %w", namespace, name, err)
	}
	return httpRoute, nil
}

// MCPServerRegistrationReady checks if the MCPServerRegistration has Ready=True condition
func (v *Verifier) MCPServerRegistrationReady(name, namespace string) error {
	mcpServer, err := v.getMCPServerRegistration(name, namespace)
	if err != nil {
		return err
	}

	for _, condition := range mcpServer.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == metav1.ConditionTrue {
			return nil
		}
	}
	return fmt.Errorf("MCPServerRegistration %s/%s not ready", namespace, name)
}

// MCPServerRegistrationReadyWithToolsCount checks Ready=True and expected tool count
func (v *Verifier) MCPServerRegistrationReadyWithToolsCount(name, namespace string, expectedCount int) error {
	mcpServer, err := v.getMCPServerRegistration(name, namespace)
	if err != nil {
		return err
	}

	for _, condition := range mcpServer.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == metav1.ConditionTrue {
			if mcpServer.Status.DiscoveredTools != expectedCount {
				return fmt.Errorf("expected %d tools, got %d", expectedCount, mcpServer.Status.DiscoveredTools)
			}
			return nil
		}
	}
	return fmt.Errorf("MCPServerRegistration %s/%s not ready", namespace, name)
}

// MCPServerRegistrationNotReadyWithReason checks Ready=False with expected reason in message
func (v *Verifier) MCPServerRegistrationNotReadyWithReason(name, namespace, expectedReason string) error {
	mcpServer, err := v.getMCPServerRegistration(name, namespace)
	if err != nil {
		return err
	}

	for _, condition := range mcpServer.Status.Conditions {
		if condition.Type == "Ready" {
			if condition.Status == metav1.ConditionTrue {
				return fmt.Errorf("MCPServerRegistration %s/%s is Ready, expected NotReady", namespace, name)
			}
			if !strings.Contains(condition.Message, expectedReason) {
				return fmt.Errorf("message %q does not contain expected reason %q", condition.Message, expectedReason)
			}
			return nil
		}
	}
	return fmt.Errorf("MCPServerRegistration %s/%s has no Ready condition", namespace, name)
}

// MCPServerRegistrationHasCondition checks if the resource has any status condition
func (v *Verifier) MCPServerRegistrationHasCondition(name, namespace string) error {
	mcpServer, err := v.getMCPServerRegistration(name, namespace)
	if err != nil {
		return err
	}

	if len(mcpServer.Status.Conditions) == 0 {
		return fmt.Errorf("MCPServerRegistration %s/%s has no conditions", namespace, name)
	}
	return nil
}

// MCPServerRegistrationStatusMessage returns the Ready condition message
func (v *Verifier) MCPServerRegistrationStatusMessage(name, namespace string) (string, error) {
	mcpServer, err := v.getMCPServerRegistration(name, namespace)
	if err != nil {
		return "", err
	}

	for _, condition := range mcpServer.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Message, nil
		}
	}
	return "", fmt.Errorf("MCPServerRegistration %s/%s has no Ready condition", namespace, name)
}

// HTTPRouteHasProgrammedCondition checks if the HTTPRoute has Programmed=True condition
func (v *Verifier) HTTPRouteHasProgrammedCondition(name, namespace string) error {
	httpRoute, err := v.getHTTPRoute(name, namespace)
	if err != nil {
		return err
	}

	for _, parent := range httpRoute.Status.Parents {
		for _, condition := range parent.Conditions {
			if condition.Type == "Programmed" && condition.Status == metav1.ConditionTrue {
				return nil
			}
		}
	}
	return fmt.Errorf("HTTPRoute %s/%s does not have Programmed condition", namespace, name)
}

// HTTPRouteNoProgrammedCondition checks that HTTPRoute does NOT have Programmed condition
func (v *Verifier) HTTPRouteNoProgrammedCondition(name, namespace string) error {
	httpRoute, err := v.getHTTPRoute(name, namespace)
	if err != nil {
		return err
	}

	for _, parent := range httpRoute.Status.Parents {
		for _, condition := range parent.Conditions {
			if condition.Type == "Programmed" && condition.Status == metav1.ConditionTrue {
				return fmt.Errorf("HTTPRoute %s/%s still has Programmed condition", namespace, name)
			}
		}
	}
	return nil
}

// ToolsListHasPrefix checks if any tool in the list has the given prefix
func ToolsListHasPrefix(toolsList *mcp.ListToolsResult, prefix string) bool {
	return toolsListMatches(toolsList, func(name string) bool {
		return strings.HasPrefix(name, prefix)
	})
}

// ToolsListHasTool checks if the tools list contains a tool with the exact name
func ToolsListHasTool(toolsList *mcp.ListToolsResult, toolName string) bool {
	return toolsListMatches(toolsList, func(name string) bool {
		return name == toolName
	})
}

// toolsListMatches checks if any tool matches the given predicate
func toolsListMatches(toolsList *mcp.ListToolsResult, matcher func(string) bool) bool {
	if toolsList == nil {
		return false
	}
	for _, t := range toolsList.Tools {
		if matcher(t.Name) {
			return true
		}
	}
	return false
}

// HTTPRouteHasHostname checks if the HTTPRoute has the expected hostname
func (v *Verifier) HTTPRouteHasHostname(name, namespace, expectedHostname string) error {
	httpRoute, err := v.getHTTPRoute(name, namespace)
	if err != nil {
		return err
	}
	for _, hn := range httpRoute.Spec.Hostnames {
		if string(hn) == expectedHostname {
			return nil
		}
	}
	return fmt.Errorf("HTTPRoute %s/%s does not have hostname %q, has %v", namespace, name, expectedHostname, httpRoute.Spec.Hostnames)
}

// HTTPRouteHasParentRef checks if the HTTPRoute has a parentRef to the expected gateway and sectionName
func (v *Verifier) HTTPRouteHasParentRef(name, namespace, gatewayName, sectionName string) error {
	httpRoute, err := v.getHTTPRoute(name, namespace)
	if err != nil {
		return err
	}
	for _, ref := range httpRoute.Spec.ParentRefs {
		if string(ref.Name) == gatewayName {
			if ref.SectionName != nil && string(*ref.SectionName) == sectionName {
				return nil
			}
		}
	}
	return fmt.Errorf("HTTPRoute %s/%s does not have parentRef to gateway %q with sectionName %q", namespace, name, gatewayName, sectionName)
}

// HTTPRouteHasOwnerReference checks if the HTTPRoute has an owner reference with the expected name
func (v *Verifier) HTTPRouteHasOwnerReference(name, namespace, ownerName string) error {
	httpRoute, err := v.getHTTPRoute(name, namespace)
	if err != nil {
		return err
	}
	for _, ref := range httpRoute.OwnerReferences {
		if ref.Name == ownerName {
			return nil
		}
	}
	return fmt.Errorf("HTTPRoute %s/%s does not have owner reference %q", namespace, name, ownerName)
}

// HTTPRouteNotFound checks that an HTTPRoute does NOT exist
func (v *Verifier) HTTPRouteNotFound(name, namespace string) error {
	_, err := v.getHTTPRoute(name, namespace)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return fmt.Errorf("HTTPRoute %s/%s exists but should not", namespace, name)
}

// Legacy function wrappers for backwards compatibility during migration
// TODO: Remove these after updating all tests to use Verifier

func VerifyMCPServerRegistrationReady(ctx context.Context, k8sClient client.Client, name, namespace string) error {
	return NewVerifier(ctx, k8sClient).MCPServerRegistrationReady(name, namespace)
}

func VerifyMCPServerRegistrationReadyWithToolsCount(ctx context.Context, k8sClient client.Client, name, namespace string, toolsCount int) error {
	return NewVerifier(ctx, k8sClient).MCPServerRegistrationReadyWithToolsCount(name, namespace, toolsCount)
}

func VerifyMCPServerRegistrationNotReadyWithReason(ctx context.Context, k8sClient client.Client, name, namespace, expectedReason string) error {
	return NewVerifier(ctx, k8sClient).MCPServerRegistrationNotReadyWithReason(name, namespace, expectedReason)
}

func VerifyMCPServerRegistrationHasCondition(ctx context.Context, k8sClient client.Client, name, namespace string) error {
	return NewVerifier(ctx, k8sClient).MCPServerRegistrationHasCondition(name, namespace)
}

func GetMCPServerRegistrationStatusMessage(ctx context.Context, k8sClient client.Client, name, namespace string) (string, error) {
	return NewVerifier(ctx, k8sClient).MCPServerRegistrationStatusMessage(name, namespace)
}

func VerifyHTTPRouteHasProgrammedCondition(ctx context.Context, k8sClient client.Client, name, namespace string) error {
	return NewVerifier(ctx, k8sClient).HTTPRouteHasProgrammedCondition(name, namespace)
}

func VerifyHTTPRouteNoProgrammedCondition(ctx context.Context, k8sClient client.Client, name, namespace string) error {
	return NewVerifier(ctx, k8sClient).HTTPRouteNoProgrammedCondition(name, namespace)
}

// Legacy unexported functions for backwards compatibility
func verifyMCPServerRegistrationToolsPresent(serverPrefix string, toolsList *mcp.ListToolsResult) bool {
	return ToolsListHasPrefix(toolsList, serverPrefix)
}

func verifyMCPServerRegistrationToolPresent(toolName string, toolsList *mcp.ListToolsResult) bool {
	return ToolsListHasTool(toolsList, toolName)
}

// getMCPGatewayExtension fetches an MCPGatewayExtension by name and namespace
func (v *Verifier) getMCPGatewayExtension(name, namespace string) (*mcpv1alpha1.MCPGatewayExtension, error) {
	ext := &mcpv1alpha1.MCPGatewayExtension{}
	err := v.k8sClient.Get(v.ctx, types.NamespacedName{Name: name, Namespace: namespace}, ext)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCPGatewayExtension %s/%s: %w", namespace, name, err)
	}
	return ext, nil
}

// MCPGatewayExtensionReady checks if the MCPGatewayExtension has Ready=True condition
func (v *Verifier) MCPGatewayExtensionReady(name, namespace string) error {
	ext, err := v.getMCPGatewayExtension(name, namespace)
	if err != nil {
		return err
	}

	for _, condition := range ext.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == metav1.ConditionTrue {
			return nil
		}
	}
	return fmt.Errorf("MCPGatewayExtension %s/%s not ready", namespace, name)
}

// MCPGatewayExtensionNotReady checks if the MCPGatewayExtension has Ready=False condition
func (v *Verifier) MCPGatewayExtensionNotReady(name, namespace string) error {
	ext, err := v.getMCPGatewayExtension(name, namespace)
	if err != nil {
		return err
	}

	for _, condition := range ext.Status.Conditions {
		if condition.Type == "Ready" {
			if condition.Status == metav1.ConditionFalse {
				return nil
			}
			return fmt.Errorf("MCPGatewayExtension %s/%s is Ready, expected NotReady", namespace, name)
		}
	}
	return fmt.Errorf("MCPGatewayExtension %s/%s has no Ready condition", namespace, name)
}

// MCPGatewayExtensionNotReadyWithReason checks Ready=False with expected reason in message
func (v *Verifier) MCPGatewayExtensionNotReadyWithReason(name, namespace, expectedReason string) error {
	ext, err := v.getMCPGatewayExtension(name, namespace)
	if err != nil {
		return err
	}
	GinkgoWriter.Println("gateway extension status", ext.Status.Conditions)
	for _, condition := range ext.Status.Conditions {
		if condition.Type == "Ready" {
			if condition.Status == metav1.ConditionTrue {
				return fmt.Errorf("MCPGatewayExtension %s/%s is Ready, expected NotReady", namespace, name)
			}
			if !strings.Contains(condition.Message, expectedReason) && !strings.Contains(condition.Reason, expectedReason) {
				return fmt.Errorf("message %q/reason %q does not contain expected %q", condition.Message, condition.Reason, expectedReason)
			}
			return nil
		}
	}
	return fmt.Errorf("MCPGatewayExtension %s/%s has no Ready condition", namespace, name)
}

// MCPGatewayExtensionStatusMessage returns the Ready condition message
func (v *Verifier) MCPGatewayExtensionStatusMessage(name, namespace string) (string, error) {
	ext, err := v.getMCPGatewayExtension(name, namespace)
	if err != nil {
		return "", err
	}

	for _, condition := range ext.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Message, nil
		}
	}
	return "", fmt.Errorf("MCPGatewayExtension %s/%s has no Ready condition", namespace, name)
}

// Legacy function wrappers for MCPGatewayExtension

func VerifyMCPGatewayExtensionReady(ctx context.Context, k8sClient client.Client, name, namespace string) error {
	return NewVerifier(ctx, k8sClient).MCPGatewayExtensionReady(name, namespace)
}

func VerifyMCPGatewayExtensionNotReady(ctx context.Context, k8sClient client.Client, name, namespace string) error {
	return NewVerifier(ctx, k8sClient).MCPGatewayExtensionNotReady(name, namespace)
}

func VerifyMCPGatewayExtensionNotReadyWithReason(ctx context.Context, k8sClient client.Client, name, namespace, expectedReason string) error {
	return NewVerifier(ctx, k8sClient).MCPGatewayExtensionNotReadyWithReason(name, namespace, expectedReason)
}

func GetMCPGatewayExtensionStatusMessage(ctx context.Context, k8sClient client.Client, name, namespace string) (string, error) {
	return NewVerifier(ctx, k8sClient).MCPGatewayExtensionStatusMessage(name, namespace)
}

// getGateway fetches a Gateway by name and namespace
func (v *Verifier) getGateway(name, namespace string) (*gatewayapiv1.Gateway, error) {
	gateway := &gatewayapiv1.Gateway{}
	err := v.k8sClient.Get(v.ctx, types.NamespacedName{Name: name, Namespace: namespace}, gateway)
	if err != nil {
		return nil, fmt.Errorf("failed to get Gateway %s/%s: %w", namespace, name, err)
	}
	return gateway, nil
}

// GatewayListenerHasMCPCondition checks if the Gateway listener has MCPGatewayExtension condition with status True
func (v *Verifier) GatewayListenerHasMCPCondition(gatewayName, gatewayNamespace, listenerName string) error {
	gateway, err := v.getGateway(gatewayName, gatewayNamespace)
	if err != nil {
		return err
	}

	for _, listener := range gateway.Status.Listeners {
		if string(listener.Name) == listenerName {
			for _, condition := range listener.Conditions {
				if condition.Type == "MCPGatewayExtension" && condition.Status == metav1.ConditionTrue {
					return nil
				}
			}
			return fmt.Errorf("listener %s does not have MCPGatewayExtension condition with status True", listenerName)
		}
	}
	return fmt.Errorf("listener %s not found in Gateway status", listenerName)
}

// GatewayListenerNoMCPCondition checks that the Gateway listener does NOT have MCPGatewayExtension condition
func (v *Verifier) GatewayListenerNoMCPCondition(gatewayName, gatewayNamespace, listenerName string) error {
	gateway, err := v.getGateway(gatewayName, gatewayNamespace)
	if err != nil {
		return err
	}

	for _, listener := range gateway.Status.Listeners {
		if string(listener.Name) == listenerName {
			for _, condition := range listener.Conditions {
				if condition.Type == "MCPGatewayExtension" && condition.Status == metav1.ConditionTrue {
					return fmt.Errorf("listener %s still has MCPGatewayExtension condition", listenerName)
				}
			}
			return nil
		}
	}
	// listener not found in status, which means no condition - that's ok
	return nil
}

// GatewayListenerMCPConditionMessage returns the MCPGatewayExtension condition message for a listener
func (v *Verifier) GatewayListenerMCPConditionMessage(gatewayName, gatewayNamespace, listenerName string) (string, error) {
	gateway, err := v.getGateway(gatewayName, gatewayNamespace)
	if err != nil {
		return "", err
	}

	for _, listener := range gateway.Status.Listeners {
		if string(listener.Name) == listenerName {
			for _, condition := range listener.Conditions {
				if condition.Type == "MCPGatewayExtension" {
					return condition.Message, nil
				}
			}
			return "", fmt.Errorf("listener %s does not have MCPGatewayExtension condition", listenerName)
		}
	}
	return "", fmt.Errorf("listener %s not found in Gateway status", listenerName)
}

// Legacy function wrappers for Gateway listener status

func VerifyGatewayListenerHasMCPCondition(ctx context.Context, k8sClient client.Client, gatewayName, gatewayNamespace, listenerName string) error {
	return NewVerifier(ctx, k8sClient).GatewayListenerHasMCPCondition(gatewayName, gatewayNamespace, listenerName)
}

func VerifyGatewayListenerNoMCPCondition(ctx context.Context, k8sClient client.Client, gatewayName, gatewayNamespace, listenerName string) error {
	return NewVerifier(ctx, k8sClient).GatewayListenerNoMCPCondition(gatewayName, gatewayNamespace, listenerName)
}

func GetGatewayListenerMCPConditionMessage(ctx context.Context, k8sClient client.Client, gatewayName, gatewayNamespace, listenerName string) (string, error) {
	return NewVerifier(ctx, k8sClient).GatewayListenerMCPConditionMessage(gatewayName, gatewayNamespace, listenerName)
}
