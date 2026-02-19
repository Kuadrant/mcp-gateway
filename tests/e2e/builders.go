//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	istiov1beta1 "istio.io/api/networking/v1beta1"
	istionetv1beta1 "istio.io/client-go/pkg/apis/networking/v1beta1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
)

// TestResourcesBuilder is a unified builder for creating test resources
type TestResourcesBuilder struct {
	k8sClient        client.Client
	testName         string
	namespace        string
	hostname         string
	serviceName      string
	port             int32
	toolPrefix       string
	path             string
	credential       *corev1.Secret
	credentialKey    string
	httpRoute        *gatewayapiv1.HTTPRoute
	mcpServer        *mcpv1alpha1.MCPServerRegistration
	serviceEntry     *istionetv1beta1.ServiceEntry
	destinationRule  *istionetv1beta1.DestinationRule
	isExternal       bool
	gatewayName      string
	gatewayNamespace string
	backendNamespace string
	referenceGrant   *gatewayv1beta1.ReferenceGrant
}

// NewTestResources creates a new TestResourcesBuilder with defaults for internal services
func NewTestResources(testName string, k8sClient client.Client) *TestResourcesBuilder {
	return &TestResourcesBuilder{
		k8sClient:        k8sClient,
		testName:         testName,
		namespace:        TestServerNameSpace,
		hostname:         "e2e-server2.mcp.local",
		serviceName:      "mcp-test-server2",
		port:             9090,
		credentialKey:    "token",
		gatewayName:      GatewayName,
		gatewayNamespace: GatewayNamespace,
	}
}

// NewTestResourcesWithDefaults creates a builder with default backend (mcp-test-server2)
func NewTestResourcesWithDefaults(testName string, k8sClient client.Client) *TestResourcesBuilder {
	return NewTestResources(testName, k8sClient)
}

// ForInternalService configures the builder for an internal Kubernetes service
func (b *TestResourcesBuilder) ForInternalService(serviceName string, port int32) *TestResourcesBuilder {
	b.serviceName = serviceName
	b.port = port
	b.hostname = fmt.Sprintf("%s.mcp.local", serviceName)
	b.isExternal = false
	return b
}

// ForExternalService configures the builder for an external service
func (b *TestResourcesBuilder) ForExternalService(externalHost string, port int32) *TestResourcesBuilder {
	b.serviceName = externalHost
	b.port = port
	b.hostname = fmt.Sprintf("e2e-external-%s.mcp.local", b.testName)
	b.isExternal = true
	return b
}

// WithHostname sets a custom hostname
func (b *TestResourcesBuilder) WithHostname(hostname string) *TestResourcesBuilder {
	b.hostname = hostname
	return b
}

// WithToolPrefix sets the tool prefix for the MCPServerRegistration
func (b *TestResourcesBuilder) WithToolPrefix(prefix string) *TestResourcesBuilder {
	b.toolPrefix = prefix
	return b
}

// WithPath sets a custom MCP path
func (b *TestResourcesBuilder) WithPath(path string) *TestResourcesBuilder {
	b.path = path
	return b
}

// WithBackendTarget sets the backend service name and port for internal services
func (b *TestResourcesBuilder) WithBackendTarget(serviceName string, port int32) *TestResourcesBuilder {
	b.serviceName = serviceName
	b.port = port
	b.hostname = fmt.Sprintf("%s.mcp.local", serviceName)
	b.isExternal = false
	return b
}

// WithParentGateway sets the gateway that HTTPRoutes should target
func (b *TestResourcesBuilder) WithParentGateway(name, namespace string) *TestResourcesBuilder {
	b.gatewayName = name
	b.gatewayNamespace = namespace
	return b
}

// InNamespace sets the namespace where resources will be created
func (b *TestResourcesBuilder) InNamespace(namespace string) *TestResourcesBuilder {
	b.namespace = namespace
	return b
}

// WithBackendNamespace sets the namespace of the backend service (for cross-namespace references)
func (b *TestResourcesBuilder) WithBackendNamespace(namespace string) *TestResourcesBuilder {
	b.backendNamespace = namespace
	return b
}

// WithCredential sets the credential secret
func (b *TestResourcesBuilder) WithCredential(secret *corev1.Secret, key string) *TestResourcesBuilder {
	b.credential = secret
	b.credentialKey = key
	return b
}

// Build constructs all the resources based on configuration. Must be called before GetObjects() or Register().
func (b *TestResourcesBuilder) Build() *TestResourcesBuilder {
	routeName := UniqueName("e2e-route-" + b.testName)

	if b.isExternal {
		b.buildExternalResources(routeName)
	} else {
		b.buildInternalResources(routeName)
	}

	// build MCPServerRegistration
	b.mcpServer = &mcpv1alpha1.MCPServerRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      UniqueName("e2e-mcp-" + b.testName),
			Namespace: b.namespace,
			Labels:    map[string]string{"e2e": "test", "test": b.testName},
		},
		Spec: mcpv1alpha1.MCPServerRegistrationSpec{
			ToolPrefix: b.toolPrefix,
			Path:       b.path,
			TargetRef: mcpv1alpha1.TargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  routeName,
			},
		},
	}

	if b.credential != nil {
		b.mcpServer.Spec.CredentialRef = &mcpv1alpha1.SecretReference{
			Name: b.credential.Name,
			Key:  b.credentialKey,
		}
	}
	return b
}

func (b *TestResourcesBuilder) buildInternalResources(routeName string) {
	backendRef := gatewayapiv1.BackendObjectReference{
		Name: gatewayapiv1.ObjectName(b.serviceName),
		Port: (*gatewayapiv1.PortNumber)(&b.port),
	}

	// add namespace if cross-namespace backend reference
	if b.backendNamespace != "" && b.backendNamespace != b.namespace {
		backendRef.Namespace = (*gatewayapiv1.Namespace)(&b.backendNamespace)

		// create ReferenceGrant to allow cross-namespace backend reference
		b.referenceGrant = &gatewayv1beta1.ReferenceGrant{
			ObjectMeta: metav1.ObjectMeta{
				Name:      UniqueName("e2e-refgrant-" + b.testName),
				Namespace: b.backendNamespace,
				Labels:    map[string]string{"e2e": "test"},
			},
			Spec: gatewayv1beta1.ReferenceGrantSpec{
				From: []gatewayv1beta1.ReferenceGrantFrom{
					{
						Group:     gatewayv1beta1.Group("gateway.networking.k8s.io"),
						Kind:      gatewayv1beta1.Kind("HTTPRoute"),
						Namespace: gatewayv1beta1.Namespace(b.namespace),
					},
				},
				To: []gatewayv1beta1.ReferenceGrantTo{
					{
						Group: gatewayv1beta1.Group(""),
						Kind:  gatewayv1beta1.Kind("Service"),
					},
				},
			},
		}
	}

	b.httpRoute = &gatewayapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      routeName,
			Namespace: b.namespace,
			Labels:    map[string]string{"e2e": "test"},
		},
		Spec: gatewayapiv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayapiv1.CommonRouteSpec{
				ParentRefs: []gatewayapiv1.ParentReference{
					{
						Name:      gatewayapiv1.ObjectName(b.gatewayName),
						Namespace: (*gatewayapiv1.Namespace)(&b.gatewayNamespace),
					},
				},
			},
			Hostnames: []gatewayapiv1.Hostname{
				gatewayapiv1.Hostname(b.hostname),
				gatewayapiv1.Hostname(strings.Replace(b.hostname, ".mcp.local", ".127-0-0-1.sslip.io", 1)),
			},
			Rules: []gatewayapiv1.HTTPRouteRule{
				{
					BackendRefs: []gatewayapiv1.HTTPBackendRef{
						{
							BackendRef: gatewayapiv1.BackendRef{
								BackendObjectReference: backendRef,
							},
						},
					},
				},
			},
		},
	}
}

func (b *TestResourcesBuilder) buildExternalResources(routeName string) {
	externalHost := b.serviceName
	istioGroup := gatewayapiv1.Group("networking.istio.io")
	hostnameKind := gatewayapiv1.Kind("Hostname")
	portNum := gatewayapiv1.PortNumber(b.port)

	b.serviceEntry = &istionetv1beta1.ServiceEntry{
		ObjectMeta: metav1.ObjectMeta{
			Name:      UniqueName("e2e-se-" + b.testName),
			Namespace: b.namespace,
			Labels:    map[string]string{"e2e": "test"},
		},
		Spec: istiov1beta1.ServiceEntry{
			Hosts: []string{externalHost},
			Ports: []*istiov1beta1.ServicePort{
				{
					Number:   uint32(b.port),
					Name:     "http",
					Protocol: "HTTP",
				},
			},
			Location:   istiov1beta1.ServiceEntry_MESH_EXTERNAL,
			Resolution: istiov1beta1.ServiceEntry_DNS,
		},
	}

	b.destinationRule = &istionetv1beta1.DestinationRule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      UniqueName("e2e-dr-" + b.testName),
			Namespace: b.namespace,
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

	b.httpRoute = &gatewayapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      routeName,
			Namespace: b.namespace,
			Labels:    map[string]string{"e2e": "test"},
		},
		Spec: gatewayapiv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayapiv1.CommonRouteSpec{
				ParentRefs: []gatewayapiv1.ParentReference{
					{
						Name:      gatewayapiv1.ObjectName(b.gatewayName),
						Namespace: (*gatewayapiv1.Namespace)(&b.gatewayNamespace),
					},
				},
			},
			Hostnames: []gatewayapiv1.Hostname{
				gatewayapiv1.Hostname(b.hostname),
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
								Hostname: (*gatewayapiv1.PreciseHostname)(&externalHost),
							},
						},
					},
					BackendRefs: []gatewayapiv1.HTTPBackendRef{
						{
							BackendRef: gatewayapiv1.BackendRef{
								BackendObjectReference: gatewayapiv1.BackendObjectReference{
									Group: &istioGroup,
									Kind:  &hostnameKind,
									Name:  gatewayapiv1.ObjectName(externalHost),
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

// Register creates all resources in the cluster and returns the MCPServerRegistration.
// Build() must be called before Register().
func (b *TestResourcesBuilder) Register(ctx context.Context) *mcpv1alpha1.MCPServerRegistration {
	if b.credential != nil {
		GinkgoWriter.Println("creating credential", b.credential.Name)
		Expect(b.k8sClient.Create(ctx, b.credential)).To(Succeed())
	}

	if b.referenceGrant != nil {
		GinkgoWriter.Println("creating ReferenceGrant", b.referenceGrant.Name)
		Expect(b.k8sClient.Create(ctx, b.referenceGrant)).To(Succeed())
	}

	if b.serviceEntry != nil {
		GinkgoWriter.Println("creating ServiceEntry", b.serviceEntry.Name)
		Expect(b.k8sClient.Create(ctx, b.serviceEntry)).To(Succeed())
	}

	if b.destinationRule != nil {
		GinkgoWriter.Println("creating DestinationRule", b.destinationRule.Name)
		Expect(b.k8sClient.Create(ctx, b.destinationRule)).To(Succeed())
	}

	GinkgoWriter.Println("creating HTTPRoute", b.httpRoute.Name)
	Expect(b.k8sClient.Create(ctx, b.httpRoute)).To(Succeed())

	GinkgoWriter.Println("creating MCPServerRegistration", b.mcpServer.Name)
	Expect(b.k8sClient.Create(ctx, b.mcpServer)).To(Succeed())

	return b.mcpServer
}

// GetObjects returns all objects that will be created.
// Build() must be called before GetObjects().
func (b *TestResourcesBuilder) GetObjects() []client.Object {
	objects := []client.Object{}
	if b.mcpServer != nil {
		objects = append(objects, b.mcpServer)
	}
	if b.credential != nil {
		objects = append(objects, b.credential)
	}
	if b.httpRoute != nil {
		objects = append(objects, b.httpRoute)
	}
	if b.serviceEntry != nil {
		objects = append(objects, b.serviceEntry)
	}
	if b.destinationRule != nil {
		objects = append(objects, b.destinationRule)
	}
	if b.referenceGrant != nil {
		objects = append(objects, b.referenceGrant)
	}
	return objects
}

// GetHTTPRouteName returns the name of the HTTPRoute
func (b *TestResourcesBuilder) GetHTTPRouteName() string {
	if b.httpRoute != nil {
		return b.httpRoute.Name
	}
	return ""
}

// GetMCPServer returns the MCPServerRegistration (after build)
func (b *TestResourcesBuilder) GetMCPServer() *mcpv1alpha1.MCPServerRegistration {
	return b.mcpServer
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

// BuildCredentialSecret creates a credential secret for testing
func BuildCredentialSecret(name, token string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: TestServerNameSpace,
			Labels: map[string]string{
				"mcp.kuadrant.io/credential": "true",
				"e2e":                        "test",
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"token": fmt.Sprintf("Bearer %s", token),
		},
	}
}

func ptrTo[T any](v T) *T {
	return &v
}

// MCPGatewayExtensionSetup encapsulates the resources needed to set up an MCPGatewayExtension
// in a specific namespace with its required ReferenceGrant
type MCPGatewayExtensionSetup struct {
	k8sClient        client.Client
	name             string
	namespace        string
	gatewayName      string
	gatewayNamespace string
	publicHost       string
	pollInterval     string
	extension        *mcpv1alpha1.MCPGatewayExtension
	referenceGrant   *gatewayv1beta1.ReferenceGrant
	httpRoute        *gatewayapiv1.HTTPRoute
	createHTTPRoute  bool
	createdNamespace bool
	createdRefGrant  bool
	createdExtension bool
	createdHTTPRoute bool
}

// NewMCPGatewayExtensionSetup creates a new setup helper for MCPGatewayExtension
func NewMCPGatewayExtensionSetup(k8sClient client.Client) *MCPGatewayExtensionSetup {
	return &MCPGatewayExtensionSetup{
		k8sClient:        k8sClient,
		gatewayName:      GatewayName,
		gatewayNamespace: GatewayNamespace,
	}
}

// WithName sets the MCPGatewayExtension name
func (s *MCPGatewayExtensionSetup) WithName(name string) *MCPGatewayExtensionSetup {
	s.name = name
	return s
}

// InNamespace sets the namespace where the MCPGatewayExtension will be created
func (s *MCPGatewayExtensionSetup) InNamespace(namespace string) *MCPGatewayExtensionSetup {
	s.namespace = namespace
	return s
}

// TargetingGateway sets the target Gateway
func (s *MCPGatewayExtensionSetup) TargetingGateway(name, namespace string) *MCPGatewayExtensionSetup {
	s.gatewayName = name
	s.gatewayNamespace = namespace
	return s
}

// WithPublicHost sets the public host annotation
func (s *MCPGatewayExtensionSetup) WithPublicHost(host string) *MCPGatewayExtensionSetup {
	s.publicHost = host
	return s
}

// WithPollInterval sets the poll interval annotation
func (s *MCPGatewayExtensionSetup) WithPollInterval(interval string) *MCPGatewayExtensionSetup {
	s.pollInterval = interval
	return s
}

// WithHTTPRoute creates an HTTPRoute with the public host and /mcp path
// pointing to the mcp-gateway service in the same namespace as the MCPGatewayExtension
func (s *MCPGatewayExtensionSetup) WithHTTPRoute() *MCPGatewayExtensionSetup {
	s.createHTTPRoute = true
	return s
}

// Build creates the MCPGatewayExtension and ReferenceGrant objects (without registering them)
func (s *MCPGatewayExtensionSetup) Build() *MCPGatewayExtensionSetup {
	// build MCPGatewayExtension
	annotations := map[string]string{}
	if s.publicHost != "" {
		annotations[mcpv1alpha1.AnnotationPublicHost] = s.publicHost
	}
	if s.pollInterval != "" {
		annotations[mcpv1alpha1.AnnotationPollInterval] = s.pollInterval
	}

	s.extension = &mcpv1alpha1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:        s.name,
			Namespace:   s.namespace,
			Labels:      map[string]string{"e2e": "test"},
			Annotations: annotations,
		},
		Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
			TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
				Group:     "gateway.networking.k8s.io",
				Kind:      "Gateway",
				Name:      s.gatewayName,
				Namespace: s.gatewayNamespace,
			},
		},
	}

	// build ReferenceGrant if cross-namespace
	if s.namespace != s.gatewayNamespace {
		s.referenceGrant = &gatewayv1beta1.ReferenceGrant{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "allow-" + s.name,
				Namespace: s.gatewayNamespace,
				Labels:    map[string]string{"e2e": "test"},
			},
			Spec: gatewayv1beta1.ReferenceGrantSpec{
				From: []gatewayv1beta1.ReferenceGrantFrom{
					{
						Group:     gatewayv1beta1.Group("mcp.kagenti.com"),
						Kind:      gatewayv1beta1.Kind("MCPGatewayExtension"),
						Namespace: gatewayv1beta1.Namespace(s.namespace),
					},
				},
				To: []gatewayv1beta1.ReferenceGrantTo{
					{
						Group: gatewayv1beta1.Group("gateway.networking.k8s.io"),
						Kind:  gatewayv1beta1.Kind("Gateway"),
					},
				},
			},
		}
	}

	// build HTTPRoute if requested
	if s.createHTTPRoute && s.publicHost != "" {
		port := gatewayapiv1.PortNumber(8080)
		pathType := gatewayapiv1.PathMatchPathPrefix
		pathValue := "/mcp"
		s.httpRoute = &gatewayapiv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{
				Name:      s.name + "-route",
				Namespace: s.namespace,
				Labels:    map[string]string{"e2e": "test"},
			},
			Spec: gatewayapiv1.HTTPRouteSpec{
				CommonRouteSpec: gatewayapiv1.CommonRouteSpec{
					ParentRefs: []gatewayapiv1.ParentReference{
						{
							Name:      gatewayapiv1.ObjectName(s.gatewayName),
							Namespace: (*gatewayapiv1.Namespace)(&s.gatewayNamespace),
						},
					},
				},
				Hostnames: []gatewayapiv1.Hostname{
					gatewayapiv1.Hostname(s.publicHost),
				},
				Rules: []gatewayapiv1.HTTPRouteRule{
					{
						Matches: []gatewayapiv1.HTTPRouteMatch{
							{
								Path: &gatewayapiv1.HTTPPathMatch{
									Type:  &pathType,
									Value: &pathValue,
								},
							},
						},
						BackendRefs: []gatewayapiv1.HTTPBackendRef{
							{
								BackendRef: gatewayapiv1.BackendRef{
									BackendObjectReference: gatewayapiv1.BackendObjectReference{
										Name: "mcp-gateway",
										Port: &port,
									},
								},
							},
						},
					},
				},
			},
		}
	}

	return s
}

// Clean deletes any existing MCPGatewayExtension, ReferenceGrant, HTTPRoute, and namespace that will be used by this extension builder instance. This is useful for ensuring a clean state before setting up new resources
func (s *MCPGatewayExtensionSetup) Clean(ctx context.Context) *MCPGatewayExtensionSetup {
	// delete MCPGatewayExtension if it exists
	if s.name != "" && s.namespace != "" {
		ext := &mcpv1alpha1.MCPGatewayExtension{}
		err := s.k8sClient.Get(ctx, client.ObjectKey{Name: s.name, Namespace: s.namespace}, ext)
		if err == nil {
			GinkgoWriter.Printf("Deleting existing MCPGatewayExtension %s/%s\n", s.namespace, s.name)
			CleanupResource(ctx, s.k8sClient, ext)
		}
	}

	// delete HTTPRoute if it exists
	if s.createHTTPRoute && s.name != "" && s.namespace != "" {
		routeName := s.name + "-route"
		route := &gatewayapiv1.HTTPRoute{}
		err := s.k8sClient.Get(ctx, client.ObjectKey{Name: routeName, Namespace: s.namespace}, route)
		if err == nil {
			GinkgoWriter.Printf("Deleting existing HTTPRoute %s/%s\n", s.namespace, routeName)
			CleanupResource(ctx, s.k8sClient, route)
		}
	}

	// delete ReferenceGrant if cross-namespace
	if s.namespace != s.gatewayNamespace && s.name != "" {
		refGrantName := "allow-" + s.name
		refGrant := &gatewayv1beta1.ReferenceGrant{}
		err := s.k8sClient.Get(ctx, client.ObjectKey{Name: refGrantName, Namespace: s.gatewayNamespace}, refGrant)
		if err == nil {
			GinkgoWriter.Printf("Deleting existing ReferenceGrant %s/%s\n", s.gatewayNamespace, refGrantName)
			CleanupResource(ctx, s.k8sClient, refGrant)
		}
	}

	// delete namespace if it exists and is not a system namespace
	if s.namespace != "" && s.namespace != SystemNamespace && s.namespace != GatewayNamespace && s.namespace != TestServerNameSpace {
		ns := &corev1.Namespace{}
		err := s.k8sClient.Get(ctx, client.ObjectKey{Name: s.namespace}, ns)
		if err == nil {
			GinkgoWriter.Printf("Deleting existing namespace %s\n", s.namespace)
			CleanupResource(ctx, s.k8sClient, ns)
			// wait for namespace to be fully deleted
			Eventually(func() bool {
				err := s.k8sClient.Get(ctx, client.ObjectKey{Name: s.namespace}, &corev1.Namespace{})
				return apierrors.IsNotFound(err)
			}, TestTimeoutMedium, TestRetryInterval).Should(BeTrue(), "namespace should be deleted")
		}
	}

	return s
}

// Register creates the namespace (if needed), ReferenceGrant, HTTPRoute, and MCPGatewayExtension in the cluster
func (s *MCPGatewayExtensionSetup) Register(ctx context.Context) *MCPGatewayExtensionSetup {
	// ensure namespace exists
	ns := &corev1.Namespace{}
	err := s.k8sClient.Get(ctx, client.ObjectKey{Name: s.namespace}, ns)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			Expect(err).ToNot(HaveOccurred())
		}
		// create namespace
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:   s.namespace,
				Labels: map[string]string{"e2e": "test"},
			},
		}
		GinkgoWriter.Printf("Creating namespace %s\n", s.namespace)
		Expect(s.k8sClient.Create(ctx, ns)).To(Succeed())
		s.createdNamespace = true
	}

	// create ReferenceGrant if needed
	if s.referenceGrant != nil {
		GinkgoWriter.Printf("Creating ReferenceGrant %s in %s\n", s.referenceGrant.Name, s.referenceGrant.Namespace)
		Expect(client.IgnoreAlreadyExists(s.k8sClient.Create(ctx, s.referenceGrant))).To(Succeed())
		s.createdRefGrant = true
	}

	// create HTTPRoute before MCPGatewayExtension
	if s.httpRoute != nil {
		GinkgoWriter.Printf("Creating HTTPRoute %s in %s\n", s.httpRoute.Name, s.httpRoute.Namespace)
		Expect(s.k8sClient.Create(ctx, s.httpRoute)).To(Succeed())
		s.createdHTTPRoute = true
	}

	// create MCPGatewayExtension
	GinkgoWriter.Printf("Creating MCPGatewayExtension %s in %s\n", s.extension.Name, s.extension.Namespace)
	Expect(s.k8sClient.Create(ctx, s.extension)).To(Succeed())
	s.createdExtension = true

	return s
}

// TearDown deletes only resources created by this setup
func (s *MCPGatewayExtensionSetup) TearDown(ctx context.Context) {
	if s.createdExtension && s.extension != nil {
		GinkgoWriter.Printf("Deleting MCPGatewayExtension %s/%s\n", s.namespace, s.name)
		CleanupResource(ctx, s.k8sClient, s.extension)
	}

	if s.createdHTTPRoute && s.httpRoute != nil {
		GinkgoWriter.Printf("Deleting HTTPRoute %s/%s\n", s.httpRoute.Namespace, s.httpRoute.Name)
		CleanupResource(ctx, s.k8sClient, s.httpRoute)
	}

	if s.createdRefGrant && s.referenceGrant != nil {
		GinkgoWriter.Printf("Deleting ReferenceGrant %s/%s\n", s.referenceGrant.Namespace, s.referenceGrant.Name)
		CleanupResource(ctx, s.k8sClient, s.referenceGrant)
	}

	if s.createdNamespace {
		GinkgoWriter.Printf("Deleting namespace %s\n", s.namespace)
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: s.namespace}}
		CleanupResource(ctx, s.k8sClient, ns)
	}
}

// GetExtension returns the MCPGatewayExtension object
func (s *MCPGatewayExtensionSetup) GetExtension() *mcpv1alpha1.MCPGatewayExtension {
	return s.extension
}

// GetReferenceGrant returns the ReferenceGrant object (may be nil if same namespace)
func (s *MCPGatewayExtensionSetup) GetReferenceGrant() *gatewayv1beta1.ReferenceGrant {
	return s.referenceGrant
}

// GetNamespace returns the namespace where the MCPGatewayExtension is created
func (s *MCPGatewayExtensionSetup) GetNamespace() string {
	return s.namespace
}

// GetName returns the name of the MCPGatewayExtension
func (s *MCPGatewayExtensionSetup) GetName() string {
	return s.name
}

// GetHTTPRoute returns the HTTPRoute object (may be nil if WithHTTPRoute was not called)
func (s *MCPGatewayExtensionSetup) GetHTTPRoute() *gatewayapiv1.HTTPRoute {
	return s.httpRoute
}

// MCPGatewayExtensionBuilder builds MCPGatewayExtension resources
type MCPGatewayExtensionBuilder struct {
	name            string
	namespace       string
	targetGateway   string
	targetNamespace string
}

// NewMCPGatewayExtensionBuilder creates a new MCPGatewayExtensionBuilder
func NewMCPGatewayExtensionBuilder(name, namespace string) *MCPGatewayExtensionBuilder {
	return &MCPGatewayExtensionBuilder{
		name:      name,
		namespace: namespace,
	}
}

// WithTarget sets the target Gateway reference
func (b *MCPGatewayExtensionBuilder) WithTarget(gatewayName, gatewayNamespace string) *MCPGatewayExtensionBuilder {
	b.targetGateway = gatewayName
	b.targetNamespace = gatewayNamespace
	return b
}

// Build creates the MCPGatewayExtension resource
func (b *MCPGatewayExtensionBuilder) Build() *mcpv1alpha1.MCPGatewayExtension {
	return &mcpv1alpha1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.name,
			Namespace: b.namespace,
			Labels:    map[string]string{"e2e": "test"},
		},
		Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
			TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
				Group:     "gateway.networking.k8s.io",
				Kind:      "Gateway",
				Name:      b.targetGateway,
				Namespace: b.targetNamespace,
			},
		},
	}
}

// ReferenceGrantBuilder builds ReferenceGrant resources for cross-namespace references
type ReferenceGrantBuilder struct {
	name          string
	namespace     string
	fromNamespace string
	fromKind      string
	fromGroup     string
	toKind        string
	toGroup       string
}

// NewReferenceGrantBuilder creates a new ReferenceGrantBuilder
func NewReferenceGrantBuilder(name, namespace string) *ReferenceGrantBuilder {
	return &ReferenceGrantBuilder{
		name:      name,
		namespace: namespace,
		fromGroup: "mcp.kagenti.com",
		fromKind:  "MCPGatewayExtension",
		toGroup:   "gateway.networking.k8s.io",
		toKind:    "Gateway",
	}
}

// FromNamespace sets the namespace allowed to reference resources in this namespace
func (b *ReferenceGrantBuilder) FromNamespace(ns string) *ReferenceGrantBuilder {
	b.fromNamespace = ns
	return b
}

// Build creates the ReferenceGrant resource
func (b *ReferenceGrantBuilder) Build() *gatewayv1beta1.ReferenceGrant {
	return &gatewayv1beta1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      b.name,
			Namespace: b.namespace,
			Labels:    map[string]string{"e2e": "test"},
		},
		Spec: gatewayv1beta1.ReferenceGrantSpec{
			From: []gatewayv1beta1.ReferenceGrantFrom{
				{
					Group:     gatewayv1beta1.Group(b.fromGroup),
					Kind:      gatewayv1beta1.Kind(b.fromKind),
					Namespace: gatewayv1beta1.Namespace(b.fromNamespace),
				},
			},
			To: []gatewayv1beta1.ReferenceGrantTo{
				{
					Group: gatewayv1beta1.Group(b.toGroup),
					Kind:  gatewayv1beta1.Kind(b.toKind),
				},
			},
		},
	}
}
