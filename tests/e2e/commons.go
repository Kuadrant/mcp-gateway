//go:build e2e

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	goenv "github.com/caitlinelfring/go-env-default"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Test timeouts and intervals
const (
	TestTimeoutMedium     = time.Second * 60
	TestTimeoutLong       = time.Minute * 3
	TestTimeoutConfigSync = time.Minute * 6
	TestRetryInterval     = time.Second * 5
)

// Namespace constants
const (
	SystemNamespace     = "mcp-system"
	ConfigMapName       = "mcp-gateway-config"
	GatewayNamespace    = "gateway-system"
	GatewayName         = "mcp-gateway"
	GatewayListenerName = "mcp" // listener name on mcp-gateway
	MCPExtensionName    = "mcp-gateway"
	TestServerNameSpace = "mcp-test"
	ReferenceGrantName  = "allow-mcp-gateway"
)

// e2e-1 gateway constants (used by multi-gateway tests)
const (
	E2E1GatewayName  = "e2e-1"
	E2E1ListenerName = "mcp" // listener name on e2e-1 gateway
)

// shared-gateway constants (used by team isolation tests)
const (
	SharedGatewayName = "shared-gateway"
	// Team A listeners
	TeamAMCPListenerName  = "team-a-mcp"
	TeamAMCPSListenerName = "team-a-mcps"
	TeamANamespace        = "team-a"
	TeamANamespaceLabel   = "mcp-team"
	TeamANamespaceValue   = "team-a"
	// Team B listeners
	TeamBMCPListenerName  = "team-b-mcp"
	TeamBMCPSListenerName = "team-b-mcps"
	TeamBNamespace        = "team-b"
	TeamBNamespaceValue   = "team-b"
)

const defaultE2EDomain = "127-0-0-1.sslip.io"

// e2e environment configuration
var (
	e2eDomain = goenv.GetDefault("E2E_DOMAIN", defaultE2EDomain)
	e2eScheme = goenv.GetDefault("E2E_SCHEME", "http")
)

// public hosts - derived from E2E_DOMAIN
var (
	gatewayPublicHost = goenv.GetDefault("GATEWAY_PUBLIC_HOST", "mcp."+e2eDomain)
	E2E1PublicHost    = goenv.GetDefault("E2E1_PUBLIC_HOST", "e2e-1."+e2eDomain)
	TeamAPublicHost   = goenv.GetDefault("TEAM_A_PUBLIC_HOST", "team-a."+e2eDomain)
	TeamBPublicHost   = goenv.GetDefault("TEAM_B_PUBLIC_HOST", "team-b."+e2eDomain)
)

// gateway URLs - on Kind use localhost port mappings, on real clusters derive from public hosts
var (
	gatewayURL      = goenv.GetDefault("GATEWAY_URL", gatewayURLDefault(gatewayPublicHost, "http://localhost:8001/mcp"))
	E2E1GatewayURL  = goenv.GetDefault("E2E1_GATEWAY_URL", gatewayURLDefault(E2E1PublicHost, "http://localhost:8004/mcp"))
	TeamAGatewayURL = goenv.GetDefault("TEAM_A_GATEWAY_URL", gatewayURLDefault(TeamAPublicHost, "http://localhost:8005/mcp"))
	TeamBGatewayURL = goenv.GetDefault("TEAM_B_GATEWAY_URL", gatewayURLDefault(TeamBPublicHost, "http://localhost:8006/mcp"))
)

// gatewayURLDefault returns the Kind-specific localhost URL when using the default domain,
// otherwise derives the URL from the public host.
func gatewayURLDefault(host, kindDefault string) string {
	if e2eDomain == defaultE2EDomain {
		return kindDefault
	}
	return e2eScheme + "://" + host + "/mcp"
}

// UniqueName generates a unique name with the given prefix
func UniqueName(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

// CleanupResource deletes a resource, ignoring not found errors
func CleanupResource(ctx context.Context, k8sClient client.Client, obj client.Object) {
	err := k8sClient.Delete(ctx, obj)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			Expect(err).ToNot(HaveOccurred())
		}
	}
}

// Legacy aliases for backwards compatibility during migration
// These delegate to the new unified builder

// NewMCPServerResourcesWithDefaults creates a new builder with defaults (legacy alias)
func NewMCPServerResourcesWithDefaults(testName string, k8sClient client.Client) *TestResourcesBuilder {
	return NewTestResourcesWithDefaults(testName, k8sClient)
}

// NewMCPServerResources creates a new builder for a specific service (legacy alias)
func NewMCPServerResources(testName, hostName, serviceName string, port int32, k8sClient client.Client) *TestResourcesBuilder {
	return NewTestResources(testName, k8sClient).
		ForInternalService(serviceName, port).
		WithHostname(hostName)
}

// NewExternalMCPServerResources creates a new builder for external services (legacy alias)
func NewExternalMCPServerResources(testName string, k8sClient client.Client, externalHost string, port int32) *TestResourcesBuilder {
	return NewTestResources(testName, k8sClient).
		ForExternalService(externalHost, port)
}

// BuildTestMCPVirtualServer creates a virtual server builder (legacy alias)
func BuildTestMCPVirtualServer(name, namespace string, tools []string) *MCPVirtualServerBuilder {
	return NewMCPVirtualServerBuilder(name, namespace).WithTools(tools)
}

// MCPToolsLister interface for clients that can list tools
type MCPToolsLister interface {
	ListTools(ctx context.Context, req mcp.ListToolsRequest) (*mcp.ListToolsResult, error)
}

// WaitForToolsWithPrefix waits for tools with the given prefix to be present
func WaitForToolsWithPrefix(ctx context.Context, client MCPToolsLister, prefix string) {
	Eventually(func(g Gomega) {
		toolsList, err := client.ListTools(ctx, mcp.ListToolsRequest{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(toolsList).NotTo(BeNil())
		g.Expect(verifyMCPServerRegistrationToolsPresent(prefix, toolsList)).To(BeTrue(),
			"tools with prefix %q should exist", prefix)
	}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
}

// WaitForToolsWithPrefixAbsent waits for tools with the given prefix to be absent
func WaitForToolsWithPrefixAbsent(ctx context.Context, client MCPToolsLister, prefix string) {
	Eventually(func(g Gomega) {
		toolsList, err := client.ListTools(ctx, mcp.ListToolsRequest{})
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(toolsList).NotTo(BeNil())
		g.Expect(verifyMCPServerRegistrationToolsPresent(prefix, toolsList)).To(BeFalse(),
			"tools with prefix %q should NOT exist", prefix)
	}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
}
