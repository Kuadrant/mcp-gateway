//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"maps"
	"os/exec"
	"strings"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	istionetv1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	istiov1beta1 "istio.io/api/networking/v1beta1"

	"github.com/kagenti/mcp-gateway/pkg/apis/mcp/v1alpha1"
	mcpv1alpha1 "github.com/kagenti/mcp-gateway/pkg/apis/mcp/v1alpha1"
)

const (
	TestTimeoutMedium     = time.Second * 60
	TestTimeoutLong       = time.Minute * 2
	TestTimeoutConfigSync = time.Minute * 4
	TestRetryInterval     = time.Second * 5

	TestNamespace   = "mcp-test"
	SystemNamespace = "mcp-system"
	ConfigMapName   = "mcp-gateway-config"
)

// MCPServerBuilder builds MCPServer resources
type MCPServerBuilder struct {
	name            string
	namespace       string
	targetHTTPRoute string
	prefix          string
	secret          *corev1.Secret
	path            string
	credentialKey   string
}

// NewMCPServerBuilder creates a new MCPServerBuilder
func NewMCPServerBuilder(name, namespace string) *MCPServerBuilder {
	return &MCPServerBuilder{
		name:          name,
		namespace:     namespace,
		credentialKey: "token",
	}
}

// WithTargetHTTPRoute sets the target HTTPRoute
func (b *MCPServerBuilder) WithTargetHTTPRoute(route string) *MCPServerBuilder {
	b.targetHTTPRoute = route
	return b
}

// WithToolPrefix sets the tool prefix
func (b *MCPServerBuilder) WithToolPrefix(prefix string) *MCPServerBuilder {
	b.prefix = prefix
	return b
}

// WithSecret sets the credential secret
func (b *MCPServerBuilder) WithSecret(secret *corev1.Secret) *MCPServerBuilder {
	b.secret = secret
	return b
}

// WithPath sets the custom MCP path
func (b *MCPServerBuilder) WithPath(path string) *MCPServerBuilder {
	b.path = path
	return b
}

// WithCredentialKey sets the secret key for credentials
func (b *MCPServerBuilder) WithCredentialKey(key string) *MCPServerBuilder {
	b.credentialKey = key
	return b
}

// Build creates the MCPServer resource
func (b *MCPServerBuilder) Build() *mcpv1alpha1.MCPServer {
	mcpServ := &mcpv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.name,
			Namespace: b.namespace,
			Labels:    map[string]string{"e2e": "test"},
		},
		Spec: mcpv1alpha1.MCPServerSpec{
			ToolPrefix: b.prefix,
			TargetRef: mcpv1alpha1.TargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  b.targetHTTPRoute,
			},
		},
	}
	if b.path != "" {
		mcpServ.Spec.Path = b.path
	}
	if b.secret != nil {
		mcpServ.Spec.CredentialRef = &mcpv1alpha1.SecretReference{
			Name: b.secret.Name,
			Key:  b.credentialKey,
		}
	}
	return mcpServ
}

// BuildTestMCPServer creates a test MCPServer resource (legacy function for backwards compatibility)
func BuildTestMCPServer(name, namespace string, targetHTTPRoute string, prefix string) *MCPServerBuilder {
	return NewMCPServerBuilder(name, namespace).
		WithTargetHTTPRoute(targetHTTPRoute).
		WithToolPrefix(prefix)

}

// MCPVirtualServerBuilder builds MCPVirtualServer resources
type MCPVirtualServerBuilder struct {
	name        string
	namespace   string
	description string
	tools       []string
}

// NewMCPVirtualServerBuilder creates a new MCPVirtualServerBuilder
func NewMCPVirtualServerBuilder(name, namespace string) *MCPVirtualServerBuilder {
	return &MCPVirtualServerBuilder{
		name:      name,
		namespace: namespace,
	}
}

// WithDescription sets the description
func (b *MCPVirtualServerBuilder) WithDescription(desc string) *MCPVirtualServerBuilder {
	b.description = desc
	return b
}

// WithTools sets the tools list
func (b *MCPVirtualServerBuilder) WithTools(tools []string) *MCPVirtualServerBuilder {
	b.tools = tools
	return b
}

// Build creates the MCPVirtualServer resource
func (b *MCPVirtualServerBuilder) Build() *mcpv1alpha1.MCPVirtualServer {
	return &mcpv1alpha1.MCPVirtualServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      UniqueName(b.name),
			Namespace: b.namespace,
		},
		Spec: mcpv1alpha1.MCPVirtualServerSpec{
			Description: b.description,
			Tools:       b.tools,
		},
	}
}

// BuildTestMCPVirtualServer creates a test MCPVirtualServer resource
func BuildTestMCPVirtualServer(name, namespace string, tools []string) *MCPVirtualServerBuilder {
	return NewMCPVirtualServerBuilder(name, namespace).
		WithTools(tools)
}

// BuildTestHTTPRoute creates a test HTTPRoute
func BuildTestHTTPRoute(name, namespace, hostname, serviceName string, port int32) *gatewayapiv1.HTTPRoute {
	gatewayNamespace := "gateway-system"
	return &gatewayapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      UniqueName(name),
			Namespace: namespace,
			Labels:    map[string]string{"e2e": "test"},
		},
		Spec: gatewayapiv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayapiv1.CommonRouteSpec{
				ParentRefs: []gatewayapiv1.ParentReference{
					{
						Name:      "mcp-gateway",
						Namespace: (*gatewayapiv1.Namespace)(&gatewayNamespace),
					},
				},
			},
			Hostnames: []gatewayapiv1.Hostname{
				gatewayapiv1.Hostname(hostname),
				// add second hostname to match real deployments
				gatewayapiv1.Hostname(strings.Replace(hostname, ".mcp.example.com", ".127-0-0-1.sslip.io", 1)),
			},
			Rules: []gatewayapiv1.HTTPRouteRule{
				{
					BackendRefs: []gatewayapiv1.HTTPBackendRef{
						{
							BackendRef: gatewayapiv1.BackendRef{
								BackendObjectReference: gatewayapiv1.BackendObjectReference{
									Name: gatewayapiv1.ObjectName(serviceName),
									Port: (*gatewayapiv1.PortNumber)(&port),
								},
							},
						},
					},
				},
			},
		},
	}
}

// BuildCredentialSecret creates a credential secret for testing
func BuildCredentialSecret(name, token string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: TestNamespace,
			Labels: map[string]string{
				"mcp.kagenti.com/credential": "true",
				"e2e":                        "test",
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"token": fmt.Sprintf("Bearer %s", token), // valid token
		},
	}
}

// MCPServerName returns the full name of an MCP server from its HTTPRoute
func MCPServerName(route *gatewayapiv1.HTTPRoute) string {
	return fmt.Sprintf("%s/%s", route.Namespace, route.Name)
}

// VerifyMCPServerReady checks if the MCPServer has Ready condition. Once ready it should be able to be invoked
func VerifyMCPServerReady(ctx context.Context, k8sClient client.Client, name, namespace string) error {
	mcpServer := &mcpv1alpha1.MCPServer{}

	err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, mcpServer)

	if err != nil {
		return fmt.Errorf("failed to verify mcp server %s ready %w", mcpServer.Name, err)
	}

	for _, condition := range mcpServer.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == metav1.ConditionTrue {
			return nil
		}
	}
	return fmt.Errorf("mcpserver %s not ready ", mcpServer.Name)

}

// VerifyMCPVerifyMCPServerReadyWithToolsCountServerReady checks if the MCPServer has Ready condition. Once ready it should be able to be invoked
func VerifyMCPServerReadyWithToolsCount(ctx context.Context, k8sClient client.Client, name, namespace string, toolsCount int) error {
	mcpServer := &mcpv1alpha1.MCPServer{}

	err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, mcpServer)

	if err != nil {
		return fmt.Errorf("failed to verify mcp server %s ready %w", mcpServer.Name, err)
	}

	for _, condition := range mcpServer.Status.Conditions {
		if condition.Type == "Ready" && condition.Status == metav1.ConditionTrue {
			if mcpServer.Status.DiscoveredTools != toolsCount {
				return fmt.Errorf("status tool count does not match expected %d got %d", toolsCount, mcpServer.Status.DiscoveredTools)
			}
			return nil
		}
	}

	return fmt.Errorf("mcpserver %s not ready ", mcpServer.Name)

}

// GetMCPServerStatusMessage returns the Ready condition message for an MCPServer
func GetMCPServerStatusMessage(ctx context.Context, k8sClient client.Client, name, namespace string) (string, error) {
	mcpServer := &mcpv1alpha1.MCPServer{}

	err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, mcpServer)

	if err != nil {
		return "", fmt.Errorf("failed to get mcp server %s: %w", name, err)
	}

	for _, condition := range mcpServer.Status.Conditions {
		if condition.Type == "Ready" {
			return condition.Message, nil
		}
	}
	return "", fmt.Errorf("mcpserver %s has no Ready condition", name)
}

// VerifyMCPServerNotReadyWithReason checks if MCPServer has Ready=False with message containing reason
func VerifyMCPServerNotReadyWithReason(ctx context.Context, k8sClient client.Client, name, namespace, expectedReason string) error {
	mcpServer := &mcpv1alpha1.MCPServer{}

	err := k8sClient.Get(ctx, types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}, mcpServer)

	if err != nil {
		return fmt.Errorf("failed to get mcp server %s: %w", name, err)
	}

	for _, condition := range mcpServer.Status.Conditions {
		if condition.Type == "Ready" {
			if condition.Status == metav1.ConditionTrue {
				return fmt.Errorf("mcpserver %s is Ready, expected NotReady with reason: %s", name, expectedReason)
			}
			if !strings.Contains(condition.Message, expectedReason) {
				return fmt.Errorf("mcpserver %s message %q does not contain expected reason %q", name, condition.Message, expectedReason)
			}
			return nil
		}
	}
	return fmt.Errorf("mcpserver %s has no Ready condition", name)
}

// verifies controller processed the MCPServer by checking it has a status condition
func VerifyMCPServerHasCondition(ctx context.Context, k8sClient client.Client, name, namespace string) error {
	mcpServer := &mcpv1alpha1.MCPServer{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, mcpServer)
	if err != nil {
		return fmt.Errorf("failed to get mcpserver %s: %w", name, err)
	}
	if len(mcpServer.Status.Conditions) == 0 {
		return fmt.Errorf("mcpserver %s has no conditions yet", name)
	}
	return nil
}

// MCPServerRegistrationBuilder builds and registers MCP server resources
type MCPServerRegistrationBuilder struct {
	k8sClient     client.Client
	credential    *corev1.Secret
	credentialKey string
	httpRoute     *gatewayapiv1.HTTPRoute
	mcpServer     *mcpv1alpha1.MCPServer
}

// NewMCPServerRegistrationWithDefaults creates a new registration builder with defaults
func NewMCPServerRegistrationWithDefaults(testName string, k8sClient client.Client) *MCPServerRegistrationBuilder {
	return NewMCPServerRegistration(testName, "e2e-server2.mcp.local", "mcp-test-server2", 9090, k8sClient)
}

// NewMCPServerRegistration creates a new registration builder
func NewMCPServerRegistration(testName, hostName, serviceName string, port int32, k8sClient client.Client) *MCPServerRegistrationBuilder {
	httpRoute := BuildTestHTTPRoute("e2e-server2-route-"+testName, TestNamespace,
		hostName, serviceName, port)
	mcpServer := BuildTestMCPServer(httpRoute.Name, TestNamespace,
		httpRoute.Name, httpRoute.Name).Build()
	mcpServer.Labels["test"] = testName

	return &MCPServerRegistrationBuilder{
		k8sClient: k8sClient,
		httpRoute: httpRoute,
		mcpServer: mcpServer,
	}
}

// GetTestHeaderSigningKey will return a key to sign a header with to be trusted by the gateway
func GetTestHeaderSigningKey() string {
	return `-----BEGIN EC PRIVATE KEY-----
MHcCAQEEIEY3QeiP9B9Bm3NHG3SgyiDHcbckwsGsQLKgv4fJxjJWoAoGCCqGSM49
AwEHoUQDQgAE7WdMdvC8hviEAL4wcebqaYbLEtVOVEiyi/nozagw7BaWXmzbOWyy
95gZLirTkhUb1P4Z4lgKLU2rD5NCbGPHAA==
-----END EC PRIVATE KEY-----`
}

// CreateAuthorizedToolsJWT creates a signed JWT for the x-authorized-tools header
// allowedTools is a map of server hostname to list of tool names
func CreateAuthorizedToolsJWT(allowedTools map[string][]string) (string, error) {
	keyBytes := []byte(GetTestHeaderSigningKey())
	claimPayload, err := json.Marshal(allowedTools)
	if err != nil {
		return "", fmt.Errorf("failed to marshal allowed tools: %w", err)
	}
	block, _ := pem.Decode(keyBytes)
	if block == nil {
		return "", fmt.Errorf("failed to decode PEM block")
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"allowed-tools": string(claimPayload)})
	parsedKey, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("failed to parse EC private key: %w", err)
	}
	jwtToken, err := token.SignedString(parsedKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %w", err)
	}
	return jwtToken, nil
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

// WithBackendTarget sets the backend service and port for the HTTPRoute
func (b *MCPServerRegistrationBuilder) WithBackendTarget(backend string, port int32) *MCPServerRegistrationBuilder {
	if b.httpRoute != nil {
		p := gatewayapiv1.PortNumber(port)
		b.httpRoute.Spec.Rules[0].BackendRefs[0].BackendObjectReference = gatewayapiv1.BackendObjectReference{
			Name: gatewayapiv1.ObjectName(backend),
			Port: &p,
		}
		b.httpRoute.Spec.Hostnames = []gatewayapiv1.Hostname{gatewayapiv1.Hostname(fmt.Sprintf("%s.mcp.local", backend))}
	}
	// regen the mcp server
	b.mcpServer = BuildTestMCPServer(b.httpRoute.Name, TestNamespace,
		b.httpRoute.Name, b.httpRoute.Name).Build()
	return b
}

// WithCredential overrides the default credential secret
func (b *MCPServerRegistrationBuilder) WithCredential(secret *corev1.Secret, key string) *MCPServerRegistrationBuilder {
	b.credential = secret
	return b
}

// WithHTTPRoute overrides the default HTTPRoute
func (b *MCPServerRegistrationBuilder) WithHTTPRoute(route *gatewayapiv1.HTTPRoute) *MCPServerRegistrationBuilder {
	b.httpRoute = route
	return b
}

// WithToolPrefix overrides the default tool prefix
func (b *MCPServerRegistrationBuilder) WithToolPrefix(prefix string) *MCPServerRegistrationBuilder {
	if b.mcpServer != nil {
		b.mcpServer.Spec.ToolPrefix = prefix
	}
	return b
}

// Register creates all resources and returns them
func (b *MCPServerRegistrationBuilder) Register(ctx context.Context) *v1alpha1.MCPServer {

	if b.credential != nil {
		GinkgoWriter.Println("creating credential ", b.credential.Name)
		Expect(b.k8sClient.Create(ctx, b.credential)).To(Succeed())
		b.mcpServer.Spec.CredentialRef = &mcpv1alpha1.SecretReference{
			Name: b.credential.Name,
			Key:  b.credentialKey,
		}
	}
	Expect(b.k8sClient.Create(ctx, b.httpRoute)).To(Succeed())
	Expect(b.k8sClient.Create(ctx, b.mcpServer)).To(Succeed())

	return b.mcpServer
}

// GetObjects returns all objects defined in the builder
func (b *MCPServerRegistrationBuilder) GetObjects() []client.Object {
	objects := []client.Object{}
	if b.credential != nil {
		objects = append(objects, b.credential)
	}
	if b.httpRoute != nil {
		objects = append(objects, b.httpRoute)
	}
	if b.mcpServer != nil {
		objects = append(objects, b.mcpServer)
	}
	return objects
}

// CleanupResource deletes a resource and waits for it to be gone
func CleanupResource(ctx context.Context, k8sClient client.Client, obj client.Object) {
	err := k8sClient.Delete(ctx, obj)
	if err != nil {
		// ignore not found errors
		if client.IgnoreNotFound(err) != nil {
			Expect(err).ToNot(HaveOccurred())
		}
	}
}

// DumpComponentLogs dumps logs for mcp-gateway components on test failure
func DumpComponentLogs() {
	GinkgoWriter.Println("=== Dumping Component Logs ===")

	// dump controller logs
	GinkgoWriter.Println("\n--- Controller Logs ---")
	cmd := exec.Command("kubectl", "logs", "-n", SystemNamespace,
		"deployment/mcp-controller", "--tail=50")
	output, err := cmd.CombinedOutput()
	if err != nil {
		GinkgoWriter.Printf("Failed to get controller logs: %v\n", err)
	} else {
		GinkgoWriter.Printf("%s\n", output)
	}

	// dump broker/router logs
	GinkgoWriter.Println("\n--- Broker/Router Logs ---")
	cmd = exec.Command("kubectl", "logs", "-n", SystemNamespace,
		"deployment/mcp-broker-router", "--tail=100")
	output, err = cmd.CombinedOutput()
	if err != nil {
		GinkgoWriter.Printf("Failed to get broker/router logs: %v\n", err)
	} else {
		GinkgoWriter.Printf("%s\n", output)
	}

	// dump configmap content
	GinkgoWriter.Println("\n--- ConfigMap Content ---")
	cmd = exec.Command("kubectl", "get", "configmap", "-n", SystemNamespace,
		ConfigMapName, "-o", "yaml")
	output, err = cmd.CombinedOutput()
	if err != nil {
		GinkgoWriter.Printf("Failed to get configmap: %v\n", err)
	} else {
		GinkgoWriter.Printf("%s\n", output)
	}

	// dump pod status
	GinkgoWriter.Println("\n--- Pod Status ---")
	cmd = exec.Command("kubectl", "get", "pods", "-n", SystemNamespace, "-o", "wide")
	output, err = cmd.CombinedOutput()
	if err != nil {
		GinkgoWriter.Printf("Failed to get pod status: %v\n", err)
	} else {
		GinkgoWriter.Printf("%s\n", output)
	}

	// dump events
	GinkgoWriter.Println("\n--- Recent Events ---")
	cmd = exec.Command("kubectl", "get", "events", "-n", SystemNamespace,
		"--sort-by=lastTimestamp", "--tail=20")
	output, err = cmd.CombinedOutput()
	if err != nil {
		GinkgoWriter.Printf("Failed to get events: %v\n", err)
	} else {
		GinkgoWriter.Printf("%s\n", output)
	}

	GinkgoWriter.Println("=== End Component Logs ===")
}

// DumpTestServerLogs dumps logs for test MCP servers
func DumpTestServerLogs() {
	GinkgoWriter.Println("\n=== Test Server Logs ===")

	servers := []string{"mcp-test-server1", "mcp-test-server2", "mcp-test-server3"}
	for _, server := range servers {
		GinkgoWriter.Printf("\n--- %s Logs ---\n", server)
		cmd := exec.Command("kubectl", "logs", "-n", TestNamespace,
			fmt.Sprintf("deployment/%s", server), "--tail=30")
		output, err := cmd.CombinedOutput()
		if err != nil {
			GinkgoWriter.Printf("Failed to get %s logs: %v\n", server, err)
		} else {
			GinkgoWriter.Printf("%s\n", output)
		}
	}

	GinkgoWriter.Println("=== End Test Server Logs ===")
}

// UniqueName generates a unique name with the given prefix.
func UniqueName(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// NotifyingMCPClient wraps an MCP client with notification handling
type NotifyingMCPClient struct {
	*mcpclient.Client
	notifications chan mcp.JSONRPCNotification
	sessionID     string
}

// GetNotifications returns the notification channel
func (c *NotifyingMCPClient) GetNotifications() <-chan mcp.JSONRPCNotification {
	return c.notifications
}

// NewMCPGatewayClient creates a new MCP client connected to the gateway
func NewMCPGatewayClient(ctx context.Context, gatewayHost string) (*mcpclient.Client, error) {
	return NewMCPGatewayClientWithHeaders(ctx, gatewayHost, nil)
}

// NewMCPGatewayClientWithNotifications creates an MCP client that captures notifications
func NewMCPGatewayClientWithNotifications(ctx context.Context, gatewayHost string, notificationFunc func(mcp.JSONRPCNotification)) (*NotifyingMCPClient, error) {
	client, err := NewMCPGatewayClientWithHeaders(ctx, gatewayHost, nil)
	if err != nil {
		return nil, err
	}

	notifications := make(chan mcp.JSONRPCNotification, 10)
	client.OnNotification(func(notification mcp.JSONRPCNotification) {
		if notificationFunc != nil {
			notificationFunc(notification)
			return
		}
		GinkgoWriter.Println("default on notification handler", notification)
	})

	client.OnConnectionLost(func(err error) {
		GinkgoWriter.Println("connection LOST ", err)
	})

	return &NotifyingMCPClient{
		Client:        client,
		notifications: notifications,
		sessionID:     client.GetSessionId(),
	}, nil
}

// NewMCPGatewayClientWithHeaders creates a new MCP client with custom headers
func NewMCPGatewayClientWithHeaders(ctx context.Context, gatewayHost string, headers map[string]string) (*mcpclient.Client, error) {
	allHeaders := map[string]string{"e2e": "client"}
	maps.Copy(allHeaders, headers)

	gatewayClient, err := mcpclient.NewStreamableHttpClient(gatewayHost, transport.
		WithHTTPHeaders(allHeaders), transport.WithContinuousListening())
	if err != nil {
		return nil, err
	}
	err = gatewayClient.Start(ctx)
	if err != nil {
		return nil, err
	}
	_, err = gatewayClient.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo: mcp.Implementation{
				Name:    "e2e",
				Version: "0.0.1",
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return gatewayClient, nil
}

// verifyMCPServerToolsPresent this will ensure at least one tool in the tools list is from the MCPServer that uses the prefix
func verifyMCPServerToolsPresent(serverPrefix string, toolsList *mcp.ListToolsResult) bool {
	if toolsList == nil {
		return false
	}
	for _, t := range toolsList.Tools {
		if strings.HasPrefix(t.Name, serverPrefix) {
			return true
		}
	}
	return false
}

// verifyMCPServerToolPresent this will ensure at least one tool in the tools list is from the MCPServer that uses the prefix
func verifyMCPServerToolPresent(toolName string, toolsList *mcp.ListToolsResult) bool {
	if toolsList == nil {
		return false
	}
	for _, t := range toolsList.Tools {
		if t.Name == toolName {
			return true
		}
	}
	return false
}

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

// WaitForDeploymentReady waits for a deployment to have the expected number of ready replicas
func WaitForDeploymentReady(namespace, name string, expectedReplicas int) error {
	cmd := exec.Command("kubectl", "rollout", "status", "deployment", name,
		"-n", namespace, "--timeout=60s")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("deployment %s not ready: %s: %w", name, string(output), err)
	}
	return nil
}

// WaitForDeploymentScaledDown waits for a deployment to have 0 available replicas
func WaitForDeploymentScaledDown(namespace, name string) error {
	cmd := exec.Command("kubectl", "wait", "deployment", name,
		"-n", namespace, "--for=jsonpath={.status.availableReplicas}=0", "--timeout=60s")
	// kubectl wait doesn't work well with 0 replicas, use rollout status instead
	output, err := cmd.CombinedOutput()
	if err != nil {
		// fallback: check replicas directly
		checkCmd := exec.Command("kubectl", "get", "deployment", name,
			"-n", namespace, "-o", "jsonpath={.status.availableReplicas}")
		checkOutput, checkErr := checkCmd.CombinedOutput()
		if checkErr != nil {
			return fmt.Errorf("failed to check deployment %s: %s: %w", name, string(output), err)
		}
		if strings.TrimSpace(string(checkOutput)) == "" || strings.TrimSpace(string(checkOutput)) == "0" {
			return nil
		}
		return fmt.Errorf("deployment %s still has replicas: %s", name, string(checkOutput))
	}
	return nil
}

func BuildServiceEntry(name, namespace, externalHost string, port uint32) *istionetv1beta1.ServiceEntry {
	return &istionetv1beta1.ServiceEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      UniqueName(name),
			Namespace: namespace,
			Labels:    map[string]string{"e2e": "test"},
		},
		Spec: istiov1beta1.ServiceEntry{
			Hosts: []string{externalHost},
			Ports: []*istiov1beta1.ServicePort{
				{
					Number:   port,
					Name:     "http",
					Protocol: "HTTP",
				},
			},
			Location:   istiov1beta1.ServiceEntry_MESH_EXTERNAL,
			Resolution: istiov1beta1.ServiceEntry_DNS,
		},
	}
}

func BuildDestinationRule(name, namespace, externalHost string) *istionetv1beta1.DestinationRule {
	return &istionetv1beta1.DestinationRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      UniqueName(name),
			Namespace: namespace,
			Labels:    map[string]string{"e2e": "test"},
		},
		Spec: istiov1beta1.DestinationRule{
			Host: externalHost,
			TrafficPolicy: &istiov1beta1.TrafficPolicy{
				Tls: &istiov1beta1.ClientTLSSettings{
					Mode: istiov1beta1.ClientTLSSettings_DISABLE,
				},
			},
		},
	}
}

func BuildHostnameBackendHTTPRoute(name, namespace, internalHostname, externalHostname string, port int32) *gatewayapiv1.HTTPRoute {
	gatewayNamespace := "gateway-system"
	istioGroup := gatewayapiv1.Group("networking.istio.io")
	hostnameKind := gatewayapiv1.Kind("Hostname")
	portNum := gatewayapiv1.PortNumber(port)

	return &gatewayapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      UniqueName(name),
			Namespace: namespace,
			Labels:    map[string]string{"e2e": "test"},
		},
		Spec: gatewayapiv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayapiv1.CommonRouteSpec{
				ParentRefs: []gatewayapiv1.ParentReference{
					{
						Name:      "mcp-gateway",
						Namespace: (*gatewayapiv1.Namespace)(&gatewayNamespace),
					},
				},
			},
			Hostnames: []gatewayapiv1.Hostname{
				gatewayapiv1.Hostname(internalHostname),
			},
			Rules: []gatewayapiv1.HTTPRouteRule{
				{
					Matches: []gatewayapiv1.HTTPRouteMatch{
						{
							Path: &gatewayapiv1.HTTPPathMatch{
								Type:  ptrTo(gatewayapiv1.PathMatchPathPrefix),
								Value: ptrTo("/mcp"),
							},
						},
					},
					Filters: []gatewayapiv1.HTTPRouteFilter{
						{
							Type: gatewayapiv1.HTTPRouteFilterURLRewrite,
							URLRewrite: &gatewayapiv1.HTTPURLRewriteFilter{
								Hostname: (*gatewayapiv1.PreciseHostname)(&externalHostname),
							},
						},
					},
					BackendRefs: []gatewayapiv1.HTTPBackendRef{
						{
							BackendRef: gatewayapiv1.BackendRef{
								BackendObjectReference: gatewayapiv1.BackendObjectReference{
									Group: &istioGroup,
									Kind:  &hostnameKind,
									Name:  gatewayapiv1.ObjectName(externalHostname),
									Port:  &portNum,
								},
							},
						},
					},
				},
			},
		},
	}
}

func ptrTo[T any](v T) *T {
	return &v
}

type ExternalMCPServerRegistrationBuilder struct {
	k8sClient        client.Client
	serviceEntry     *istionetv1beta1.ServiceEntry
	destinationRule  *istionetv1beta1.DestinationRule
	httpRoute        *gatewayapiv1.HTTPRoute
	mcpServer        *mcpv1alpha1.MCPServer
	internalHostname string
	externalHostname string
}

func NewExternalMCPServerRegistration(testName string, k8sClient client.Client, externalHost string, port int32) *ExternalMCPServerRegistrationBuilder {
	internalHostname := fmt.Sprintf("e2e-external-%s.mcp.local", testName)

	serviceEntry := BuildServiceEntry("e2e-se-"+testName, TestNamespace, externalHost, uint32(port))
	destinationRule := BuildDestinationRule("e2e-dr-"+testName, TestNamespace, externalHost)
	httpRoute := BuildHostnameBackendHTTPRoute("e2e-ext-route-"+testName, TestNamespace, internalHostname, externalHost, port)

	mcpServer := NewMCPServerBuilder("e2e-ext-mcp-"+testName, TestNamespace).
		WithTargetHTTPRoute(httpRoute.Name).
		WithToolPrefix(httpRoute.Name).
		Build()

	return &ExternalMCPServerRegistrationBuilder{
		k8sClient:        k8sClient,
		serviceEntry:     serviceEntry,
		destinationRule:  destinationRule,
		httpRoute:        httpRoute,
		mcpServer:        mcpServer,
		internalHostname: internalHostname,
		externalHostname: externalHost,
	}
}

func (b *ExternalMCPServerRegistrationBuilder) Register(ctx context.Context) *v1alpha1.MCPServer {
	GinkgoWriter.Println("creating ServiceEntry", b.serviceEntry.Name)
	Expect(b.k8sClient.Create(ctx, b.serviceEntry)).To(Succeed())

	GinkgoWriter.Println("creating DestinationRule", b.destinationRule.Name)
	Expect(b.k8sClient.Create(ctx, b.destinationRule)).To(Succeed())

	GinkgoWriter.Println("creating HTTPRoute with Hostname backendRef", b.httpRoute.Name)
	Expect(b.k8sClient.Create(ctx, b.httpRoute)).To(Succeed())

	GinkgoWriter.Println("creating MCPServer", b.mcpServer.Name)
	Expect(b.k8sClient.Create(ctx, b.mcpServer)).To(Succeed())

	return b.mcpServer
}

func (b *ExternalMCPServerRegistrationBuilder) GetObjects() []client.Object {
	return []client.Object{
		b.serviceEntry,
		b.destinationRule,
		b.httpRoute,
		b.mcpServer,
	}
}
