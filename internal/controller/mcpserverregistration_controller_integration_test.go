//go:build integration

package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/guardrails"
)

func generateIntegrationTestCACertPEM() []byte {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// mockMCPServerConfigReaderWriter is a mock for testing
type mockMCPServerConfigReaderWriter struct {
	upsertedServers map[string]config.MCPServer
	removedServers  []string
	// unresolvedGuardrails holds config namespaces with no resolved gateway guardrails
	unresolvedGuardrails map[string]bool
}

func newMockMCPServerConfigReaderWriter() *mockMCPServerConfigReaderWriter {
	return &mockMCPServerConfigReaderWriter{
		upsertedServers:      make(map[string]config.MCPServer),
		removedServers:       []string{},
		unresolvedGuardrails: make(map[string]bool),
	}
}

func (m *mockMCPServerConfigReaderWriter) GatewayGuardrailsResolved(_ context.Context, namespaceName types.NamespacedName) (bool, error) {
	return !m.unresolvedGuardrails[namespaceName.Namespace], nil
}

func (m *mockMCPServerConfigReaderWriter) UpsertMCPServer(ctx context.Context, server config.MCPServer, namespaceName types.NamespacedName) error {
	key := fmt.Sprintf("%s/%s", namespaceName.Namespace, server.Name)
	m.upsertedServers[key] = server
	return nil
}

func (m *mockMCPServerConfigReaderWriter) RemoveMCPServer(ctx context.Context, serverName string) error {
	m.removedServers = append(m.removedServers, serverName)
	return nil
}

// createTestHTTPRoute creates an HTTPRoute for testing
func createTestHTTPRoute(name, namespace, hostname, serviceName string, port int32, gatewayName, gatewayNamespace string) *gatewayv1.HTTPRoute {
	return &gatewayv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{
					{
						Name:      gatewayv1.ObjectName(gatewayName),
						Namespace: ptr.To(gatewayv1.Namespace(gatewayNamespace)),
					},
				},
			},
			Hostnames: []gatewayv1.Hostname{
				gatewayv1.Hostname(hostname),
			},
			Rules: []gatewayv1.HTTPRouteRule{
				{
					BackendRefs: []gatewayv1.HTTPBackendRef{
						{
							BackendRef: gatewayv1.BackendRef{
								BackendObjectReference: gatewayv1.BackendObjectReference{
									Name: gatewayv1.ObjectName(serviceName),
									Port: ptr.To(gatewayv1.PortNumber(port)),
								},
							},
						},
					},
				},
			},
		},
	}
}

// createTestNeMoGuardrailsSecret builds a fake Secret.
// resolveGuardrails only accepts it if the type and label match.
func createTestNeMoGuardrailsSecret(name, namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    map[string]string{ManagedSecretLabel: ManagedSecretValue},
		},
		Type: guardrails.SecretTypeNeMo,
		StringData: map[string]string{
			"config.yaml": "url: http://nemo-guardrails-custom.mcp-system.svc.cluster.local:8000\n" +
				"model: phi3-judge\n" +
				"configIDs:\n" +
				"  - phi3-judge\n" +
				"failMode: deny\n",
		},
	}
}

// createTestService creates a Service for testing
func createTestService(name, namespace string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Port: port,
				},
			},
		},
	}
}

// createTestMCPServerRegistration creates an MCPServerRegistration for testing
func createTestMCPServerRegistration(name, namespace, httpRouteName, prefix string) *mcpv1.MCPServerRegistration {
	return &mcpv1.MCPServerRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1.MCPServerRegistrationSpec{
			TargetRef: mcpv1.TargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  httpRouteName,
			},
			Prefix: prefix,
			Path:   "/mcp",
		},
	}
}

// setHTTPRouteAcceptedStatus simulates the gateway accepting the HTTPRoute
func setHTTPRouteAcceptedStatus(ctx context.Context, httpRoute *gatewayv1.HTTPRoute, gatewayName, gatewayNamespace string) error {
	httpRoute.Status.Parents = []gatewayv1.RouteParentStatus{
		{
			ControllerName: gatewayv1.GatewayController("test.example.com/gateway-controller"),
			ParentRef: gatewayv1.ParentReference{
				Name:      gatewayv1.ObjectName(gatewayName),
				Namespace: ptr.To(gatewayv1.Namespace(gatewayNamespace)),
			},
			Conditions: []metav1.Condition{
				{
					Type:               string(gatewayv1.GatewayConditionAccepted),
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
					Reason:             "Accepted",
				},
			},
		},
	}
	return testK8sClient.Status().Update(ctx, httpRoute)
}

func setHTTPRouteParentStatuses(ctx context.Context, httpRoute *gatewayv1.HTTPRoute, parents []gatewayv1.RouteParentStatus) error {
	httpRoute.Status.Parents = parents
	return testK8sClient.Status().Update(ctx, httpRoute)
}

// forceDeleteTestMCPServerRegistration removes finalizers and deletes
func forceDeleteTestMCPServerRegistration(ctx context.Context, name, namespace string) {
	nn := types.NamespacedName{Name: name, Namespace: namespace}
	resource := &mcpv1.MCPServerRegistration{}
	err := testK8sClient.Get(ctx, nn, resource)
	if errors.IsNotFound(err) {
		return
	}
	Expect(err).NotTo(HaveOccurred())

	if controllerutil.ContainsFinalizer(resource, mcpGatewayFinalizer) {
		controllerutil.RemoveFinalizer(resource, mcpGatewayFinalizer)
		Expect(testK8sClient.Update(ctx, resource)).To(Succeed())
	}

	Expect(client.IgnoreNotFound(testK8sClient.Delete(ctx, resource))).To(Succeed())

	Eventually(func(g Gomega) {
		err := testK8sClient.Get(ctx, nn, resource)
		g.Expect(errors.IsNotFound(err)).To(BeTrue())
	}, testTimeout, testRetryInterval).Should(Succeed())
}

// deleteTestHTTPRoute deletes an HTTPRoute
func deleteTestHTTPRoute(ctx context.Context, name, namespace string) {
	httpRoute := &gatewayv1.HTTPRoute{}
	err := testK8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, httpRoute)
	if err == nil {
		_ = testK8sClient.Delete(ctx, httpRoute)
	}
}

// deleteTestService deletes a Service
func deleteTestService(ctx context.Context, name, namespace string) {
	svc := &corev1.Service{}
	err := testK8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, svc)
	if err == nil {
		_ = testK8sClient.Delete(ctx, svc)
	}
}

// newMCPServerReconciler creates an MCPReconciler for testing
func newMCPServerReconciler(configWriter *mockMCPServerConfigReaderWriter) *MCPReconciler {
	return &MCPReconciler{
		Client:             testIndexedClient,
		Scheme:             testK8sClient.Scheme(),
		DirectAPIReader:    testK8sClient,
		ConfigReaderWriter: configWriter,
		MCPExtFinderValidator: &MCPGatewayExtensionValidator{
			Client:          testIndexedClient,
			DirectAPIReader: testK8sClient,
			Logger:          slog.New(slog.NewTextHandler(GinkgoWriter, &slog.HandlerOptions{Level: slog.LevelDebug})),
		},
	}
}

// waitForMCPServerRegistrationCacheSync waits for cache to see the resource
func waitForMCPServerRegistrationCacheSync(ctx context.Context, nn types.NamespacedName) {
	Eventually(func(g Gomega) {
		cached := &mcpv1.MCPServerRegistration{}
		g.Expect(testIndexedClient.Get(ctx, nn, cached)).To(Succeed())
	}, testTimeout, testRetryInterval).Should(Succeed())
}

// waitForMCPServerRegistrationFinalizer waits for cache to see the finalizer added
func waitForMCPServerRegistrationFinalizer(ctx context.Context, nn types.NamespacedName) {
	Eventually(func(g Gomega) {
		cached := &mcpv1.MCPServerRegistration{}
		g.Expect(testIndexedClient.Get(ctx, nn, cached)).To(Succeed())
		g.Expect(controllerutil.ContainsFinalizer(cached, mcpGatewayFinalizer)).To(BeTrue())
	}, testTimeout, testRetryInterval).Should(Succeed())
	// ensure dependent cache (HTTPRoute/Gateway/MCPGatewayExtension indexed state) syncs after finalizer add
	waitForMCPServerRegistrationCacheSync(ctx, nn)
}

var _ = Describe("MCPServerRegistration Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName  = "test-mcpsr"
			httpRouteName = "test-route"
			gatewayName   = "test-gw"
			serviceName   = "test-svc"
		)

		ctx := context.Background()

		mcpsrNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			// create gateway
			gw := createTestGateway(gatewayName, "default")
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())

			// create service
			svc := createTestService(serviceName, "default", 8080)
			Expect(testK8sClient.Create(ctx, svc)).To(Succeed())

			// create HTTPRoute
			httpRoute := createTestHTTPRoute(httpRouteName, "default", "test.mcp.local", serviceName, 8080, gatewayName, "default")
			Expect(testK8sClient.Create(ctx, httpRoute)).To(Succeed())

			// set HTTPRoute as accepted by gateway
			Eventually(func(g Gomega) {
				route := &gatewayv1.HTTPRoute{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: httpRouteName, Namespace: "default"}, route)).To(Succeed())
				g.Expect(setHTTPRouteAcceptedStatus(ctx, route, gatewayName, "default")).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// create MCPGatewayExtension in same namespace (no ReferenceGrant needed)
			mcpExt := createTestMCPGatewayExtension("test-ext", "default", gatewayName, "default")
			Expect(testK8sClient.Create(ctx, mcpExt)).To(Succeed())

			// set MCPGatewayExtension Ready status directly
			Eventually(func(g Gomega) {
				ext := &mcpv1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: "test-ext", Namespace: "default"}, ext)).To(Succeed())
				ext.SetReadyCondition(metav1.ConditionTrue, mcpv1.ConditionReasonSuccess, "ready")
				g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPServerRegistration(ctx, resourceName, "default")
			forceDeleteTestMCPGatewayExtension(ctx, "test-ext", "default")
			deleteTestHTTPRoute(ctx, httpRouteName, "default")
			deleteTestService(ctx, serviceName, "default")
			deleteTestGateway(ctx, gatewayName, "default")
		})

		It("should add finalizer on first reconcile", func() {
			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(updated, mcpGatewayFinalizer)).To(BeTrue())
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should remove finalizer on deletion", func() {
			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			// first reconcile to add finalizer
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// trigger deletion
			resource := &mcpv1.MCPServerRegistration{}
			Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, resource)).To(Succeed())
			Expect(testK8sClient.Delete(ctx, resource)).To(Succeed())

			// wait for cache to see deletion timestamp
			Eventually(func(g Gomega) {
				cached := &mcpv1.MCPServerRegistration{}
				err := testIndexedClient.Get(ctx, mcpsrNamespacedName, cached)
				if errors.IsNotFound(err) {
					return
				}
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(cached.DeletionTimestamp).NotTo(BeNil())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// reconcile to remove finalizer
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// verify RemoveMCPServer was called
			Expect(configWriter.removedServers).To(ContainElement(fmt.Sprintf("%s/%s", "default", resourceName)))

			Eventually(func(g Gomega) {
				deleted := &mcpv1.MCPServerRegistration{}
				err := testK8sClient.Get(ctx, mcpsrNamespacedName, deleted)
				g.Expect(errors.IsNotFound(err)).To(BeTrue())
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})

	Context("When managing HTTPRoute status parent ownership", func() {
		const (
			resourceName                              = "test-status-owner"
			siblingName                               = "test-status-owner-sibling"
			httpRouteName                             = "test-status-owner-route"
			gatewayName                               = "test-status-owner-gw"
			serviceName                               = "test-status-owner-svc"
			extName                                   = "test-status-owner-ext"
			mcpController gatewayv1.GatewayController = "mcp.kuadrant.io/mcp-gateway"
		)

		ctx := context.Background()
		routeNN := types.NamespacedName{Name: httpRouteName, Namespace: "default"}
		var (
			reconciler     *MCPReconciler
			gatewayParent  gatewayv1.RouteParentStatus
			kuadrantParent gatewayv1.RouteParentStatus
		)

		BeforeEach(func() {
			Expect(testK8sClient.Create(ctx, createTestGateway(gatewayName, "default"))).To(Succeed())
			Expect(testK8sClient.Create(ctx, createTestService(serviceName, "default", 8080))).To(Succeed())
			route := createTestHTTPRoute(httpRouteName, "default", "test.example.com", serviceName, 8080, gatewayName, "default")
			Expect(testK8sClient.Create(ctx, route)).To(Succeed())
			Expect(setHTTPRouteAcceptedStatus(ctx, route, gatewayName, "default")).To(Succeed())
			gatewayParent = *route.Status.Parents[0].DeepCopy()
			kuadrantParent = gatewayv1.RouteParentStatus{
				ControllerName: "kuadrant.io/policy-controller",
				ParentRef:      *gatewayParent.ParentRef.DeepCopy(),
				Conditions: []metav1.Condition{{
					Type:               "Programmed",
					Status:             metav1.ConditionTrue,
					ObservedGeneration: route.Generation,
					LastTransitionTime: metav1.Now(),
					Reason:             "InUseByMCPServerRegistration",
					Message:            "HTTPRoute is referenced by at least one MCPServerRegistration",
				}},
			}
			Expect(setHTTPRouteParentStatuses(ctx, route, []gatewayv1.RouteParentStatus{gatewayParent, kuadrantParent})).To(Succeed())
			Eventually(func(g Gomega) {
				cached := &gatewayv1.HTTPRoute{}
				g.Expect(testIndexedClient.Get(ctx, routeNN, cached)).To(Succeed())
				g.Expect(cached.ResourceVersion).To(Equal(route.ResourceVersion))
			}, testTimeout, testRetryInterval).Should(Succeed())

			Expect(testK8sClient.Create(ctx, createTestMCPGatewayExtension(extName, "default", gatewayName, "default"))).To(Succeed())
			Eventually(func(g Gomega) {
				ext := &mcpv1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extName, Namespace: "default"}, ext)).To(Succeed())
				ext.SetReadyCondition(metav1.ConditionTrue, mcpv1.ConditionReasonSuccess, "ready")
				g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())
			reconciler = newMCPServerReconciler(newMockMCPServerConfigReaderWriter())
		})

		AfterEach(func() {
			forceDeleteTestMCPServerRegistration(ctx, resourceName, "default")
			forceDeleteTestMCPServerRegistration(ctx, siblingName, "default")
			forceDeleteTestMCPGatewayExtension(ctx, extName, "default")
			deleteTestHTTPRoute(ctx, httpRouteName, "default")
			deleteTestService(ctx, serviceName, "default")
			deleteTestGateway(ctx, gatewayName, "default")
		})

		reconcileRegistration := func(name, prefix string) {
			registration := createTestMCPServerRegistration(name, "default", httpRouteName, prefix)
			Expect(testK8sClient.Create(ctx, registration)).To(Succeed())
			nn := client.ObjectKeyFromObject(registration)
			waitForMCPServerRegistrationCacheSync(ctx, nn)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			waitForMCPServerRegistrationFinalizer(ctx, nn)
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
				g.Expect(err).NotTo(HaveOccurred())
				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testIndexedClient.Get(ctx, nn, updated)).To(Succeed())
				g.Expect(meta.IsStatusConditionTrue(updated.Status.Conditions, "Ready")).To(BeTrue())
			}, testTimeout, testRetryInterval).Should(Succeed())
		}

		deleteRegistration := func(name string) {
			nn := types.NamespacedName{Name: name, Namespace: "default"}
			registration := &mcpv1.MCPServerRegistration{}
			Expect(testK8sClient.Get(ctx, nn, registration)).To(Succeed())
			Expect(testK8sClient.Delete(ctx, registration)).To(Succeed())
			Eventually(func(g Gomega) {
				cached := &mcpv1.MCPServerRegistration{}
				g.Expect(testIndexedClient.Get(ctx, nn, cached)).To(Succeed())
				g.Expect(cached.DeletionTimestamp).NotTo(BeNil())
			}, testTimeout, testRetryInterval).Should(Succeed())
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(errors.IsNotFound(testK8sClient.Get(ctx, nn, registration))).To(BeTrue())
			}, testTimeout, testRetryInterval).Should(Succeed())
			// wait for the cached sibling list to observe deletion before deleting the next registration
			Eventually(func() bool {
				return errors.IsNotFound(testIndexedClient.Get(ctx, nn, &mcpv1.MCPServerRegistration{}))
			}, testTimeout, testRetryInterval).Should(BeTrue())
		}

		It("should migrate the legacy orphan into an MCP-owned parent without changing the gateway entry", func() {
			before, err := json.Marshal(gatewayParent)
			Expect(err).NotTo(HaveOccurred())
			reconcileRegistration(resourceName, "owner_")

			route := &gatewayv1.HTTPRoute{}
			Expect(testK8sClient.Get(ctx, routeNN, route)).To(Succeed())
			Expect(route.Status.Parents).To(HaveLen(2))
			after, err := json.Marshal(route.Status.Parents[0])
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
			parent := route.Status.Parents[1]
			Expect(parent.ControllerName).To(Equal(mcpController))
			Expect(parent.ParentRef).To(Equal(gatewayParent.ParentRef))
			Expect(parent.Conditions).To(HaveLen(1))
			condition := meta.FindStatusCondition(parent.Conditions, "Programmed")
			Expect(condition).NotTo(BeNil())
			Expect(condition.Status).To(Equal(metav1.ConditionTrue))
			Expect(condition.Reason).To(Equal("InUseByMCPServerRegistration"))
			Expect(condition.ObservedGeneration).To(Equal(route.Generation))
		})

		It("should remain Ready on a repeated reconcile and ignore its own parent during gateway discovery", func() {
			reconcileRegistration(resourceName, "owner_")
			route := &gatewayv1.HTTPRoute{}
			Expect(testK8sClient.Get(ctx, routeNN, route)).To(Succeed())
			Expect(route.Status.Parents[1].ControllerName).To(Equal(mcpController))
			Eventually(func(g Gomega) {
				cached := &gatewayv1.HTTPRoute{}
				g.Expect(testIndexedClient.Get(ctx, routeNN, cached)).To(Succeed())
				g.Expect(cached.ResourceVersion).To(Equal(route.ResourceVersion))
			}, testTimeout, testRetryInterval).Should(Succeed())

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: resourceName, Namespace: "default"}})
			Expect(err).NotTo(HaveOccurred())
			registration := &mcpv1.MCPServerRegistration{}
			Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: resourceName, Namespace: "default"}, registration)).To(Succeed())
			Expect(meta.IsStatusConditionTrue(registration.Status.Conditions, "Ready")).To(BeTrue())
			gateways, err := reconciler.findValidGatewaysForMCPServer(ctx, route)
			Expect(err).NotTo(HaveOccurred())
			Expect(gateways).To(HaveLen(1))
			Expect(gateways[0].Name).To(Equal(gatewayName))
			updated := &gatewayv1.HTTPRoute{}
			Expect(testK8sClient.Get(ctx, routeNN, updated)).To(Succeed())
			Expect(updated.ResourceVersion).To(Equal(route.ResourceVersion))
		})

		It("should remove its parent when the last gateway rejects the route", func() {
			reconcileRegistration(resourceName, "owner_")
			route := &gatewayv1.HTTPRoute{}
			Expect(testK8sClient.Get(ctx, routeNN, route)).To(Succeed())
			rejectedParent := *gatewayParent.DeepCopy()
			rejectedParent.Conditions[0].Status = metav1.ConditionFalse
			rejectedParent.Conditions[0].Reason = "NotAllowedByListeners"
			route.Status.Parents[0] = rejectedParent
			Expect(testK8sClient.Status().Update(ctx, route)).To(Succeed())
			Eventually(func(g Gomega) {
				cached := &gatewayv1.HTTPRoute{}
				g.Expect(testIndexedClient.Get(ctx, routeNN, cached)).To(Succeed())
				g.Expect(cached.ResourceVersion).To(Equal(route.ResourceVersion))
			}, testTimeout, testRetryInterval).Should(Succeed())

			nn := types.NamespacedName{Name: resourceName, Namespace: "default"}
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).To(MatchError(ContainSubstring("no valid gateways for httproute")))
			registration := &mcpv1.MCPServerRegistration{}
			Expect(testK8sClient.Get(ctx, nn, registration)).To(Succeed())
			Expect(meta.IsStatusConditionFalse(registration.Status.Conditions, "Ready")).To(BeTrue())
			Expect(testK8sClient.Get(ctx, routeNN, route)).To(Succeed())
			Expect(route.Status.Parents).To(Equal([]gatewayv1.RouteParentStatus{rejectedParent}))
		})

		It("should complete deletion after reconciling a route containing a legacy Kuadrant orphan", func() {
			reconcileRegistration(resourceName, "owner_")
			deleteRegistration(resourceName)

			route := &gatewayv1.HTTPRoute{}
			Expect(testK8sClient.Get(ctx, routeNN, route)).To(Succeed())
			Expect(route.Status.Parents).To(Equal([]gatewayv1.RouteParentStatus{gatewayParent}))
		})

		DescribeTable("should handle HTTPRoute status errors during deletion", func(conflict bool) {
			reconcileRegistration(resourceName, "owner_")
			nn := types.NamespacedName{Name: resourceName, Namespace: "default"}
			registration := &mcpv1.MCPServerRegistration{}
			Expect(testK8sClient.Get(ctx, nn, registration)).To(Succeed())
			Expect(testK8sClient.Delete(ctx, registration)).To(Succeed())
			statusErr := fmt.Errorf("HTTPRoute status update rejected")
			if conflict {
				statusErr = errors.NewConflict(schema.GroupResource{Group: gatewayv1.GroupName, Resource: "httproutes"}, httpRouteName, statusErr)
			}
			apiClient, err := client.NewWithWatch(cfg, client.Options{Scheme: testK8sClient.Scheme()})
			Expect(err).NotTo(HaveOccurred())
			reconciler.Client = interceptor.NewClient(apiClient, interceptor.Funcs{
				List: func(ctx context.Context, _ client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					return testIndexedClient.List(ctx, list, opts...)
				},
				SubResourceUpdate: func(ctx context.Context, c client.Client, subresource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
					if _, ok := obj.(*gatewayv1.HTTPRoute); ok && subresource == "status" {
						return statusErr
					}
					return c.SubResource(subresource).Update(ctx, obj, opts...)
				},
			})

			result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			if conflict {
				Expect(result.RequeueAfter).To(Equal(defaultRequeueTime))
				Expect(testK8sClient.Get(ctx, nn, registration)).To(Succeed())
				Expect(controllerutil.ContainsFinalizer(registration, mcpGatewayFinalizer)).To(BeTrue())
			} else {
				Expect(result).To(BeZero())
				Expect(errors.IsNotFound(testK8sClient.Get(ctx, nn, registration))).To(BeTrue())
			}
			route := &gatewayv1.HTTPRoute{}
			Expect(testK8sClient.Get(ctx, routeNN, route)).To(Succeed())
			Expect(route.Status.Parents).To(HaveLen(2))
			Expect(route.Status.Parents[1].ControllerName).To(Equal(mcpController))
		},
			Entry("retains the finalizer and requeues on conflict", true),
			Entry("removes the finalizer on a non-conflict status error", false),
		)

		It("should persist an empty parents array when deleting the sole legacy orphan", func() {
			route := &gatewayv1.HTTPRoute{}
			Expect(testK8sClient.Get(ctx, routeNN, route)).To(Succeed())
			Expect(setHTTPRouteParentStatuses(ctx, route, []gatewayv1.RouteParentStatus{kuadrantParent})).To(Succeed())
			Eventually(func(g Gomega) {
				cached := &gatewayv1.HTTPRoute{}
				g.Expect(testIndexedClient.Get(ctx, routeNN, cached)).To(Succeed())
				g.Expect(cached.ResourceVersion).To(Equal(route.ResourceVersion))
			}, testTimeout, testRetryInterval).Should(Succeed())
			registration := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "owner_")
			registration.Finalizers = []string{mcpGatewayFinalizer}
			Expect(testK8sClient.Create(ctx, registration)).To(Succeed())
			waitForMCPServerRegistrationFinalizer(ctx, client.ObjectKeyFromObject(registration))
			Expect(testK8sClient.Delete(ctx, registration)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(testIndexedClient.Get(ctx, client.ObjectKeyFromObject(registration), registration)).To(Succeed())
				g.Expect(registration.DeletionTimestamp).NotTo(BeNil())
			}, testTimeout, testRetryInterval).Should(Succeed())

			Expect(reconciler.updateHTTPRouteStatus(ctx, registration)).To(Succeed())
			Expect(testK8sClient.Get(ctx, routeNN, route)).To(Succeed())
			Expect(route.Status.Parents).NotTo(BeNil())
			Expect(route.Status.Parents).To(BeEmpty())
		})

		It("should retain the MCP parent until the last registration on a shared route is deleted", func() {
			reconcileRegistration(resourceName, "owner_")
			reconcileRegistration(siblingName, "sibling_")
			route := &gatewayv1.HTTPRoute{}
			Expect(testK8sClient.Get(ctx, routeNN, route)).To(Succeed())
			Expect(route.Status.Parents).To(HaveLen(2))
			Expect(route.Status.Parents[1].ControllerName).To(Equal(mcpController))
			before := route.DeepCopy().Status.Parents

			deleteRegistration(resourceName)
			Expect(testK8sClient.Get(ctx, routeNN, route)).To(Succeed())
			Expect(route.Status.Parents).To(Equal(before))

			deleteRegistration(siblingName)
			Expect(testK8sClient.Get(ctx, routeNN, route)).To(Succeed())
			Expect(route.Status.Parents).To(Equal([]gatewayv1.RouteParentStatus{gatewayParent}))
		})
	})

	Context("When no valid MCPGatewayExtension exists", func() {
		const (
			resourceName  = "test-mcpsr-no-ext"
			httpRouteName = "test-route-no-ext"
			gatewayName   = "test-gw-no-ext"
			serviceName   = "test-svc-no-ext"
		)

		ctx := context.Background()

		mcpsrNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			// create gateway (but no MCPGatewayExtension)
			gw := createTestGateway(gatewayName, "default")
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())

			// create service
			svc := createTestService(serviceName, "default", 8080)
			Expect(testK8sClient.Create(ctx, svc)).To(Succeed())

			// create HTTPRoute
			httpRoute := createTestHTTPRoute(httpRouteName, "default", "test.mcp.local", serviceName, 8080, gatewayName, "default")
			Expect(testK8sClient.Create(ctx, httpRoute)).To(Succeed())

			// set HTTPRoute as accepted by gateway
			Eventually(func(g Gomega) {
				route := &gatewayv1.HTTPRoute{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: httpRouteName, Namespace: "default"}, route)).To(Succeed())
				g.Expect(setHTTPRouteAcceptedStatus(ctx, route, gatewayName, "default")).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPServerRegistration(ctx, resourceName, "default")
			deleteTestHTTPRoute(ctx, httpRouteName, "default")
			deleteTestService(ctx, serviceName, "default")
			deleteTestGateway(ctx, gatewayName, "default")
		})

		It("should set status to NotReady when no MCPGatewayExtension exists", func() {
			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			// reconcile to add finalizer, wait for cache sync, then reconcile again to process
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

			_, _ = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})

			Eventually(func(g Gomega) {
				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Message).To(ContainSubstring("no valid mcpgatewayextensions"))
			}, testTimeout, testRetryInterval).Should(Succeed())

			// verify no config
			Expect(configWriter.upsertedServers).To(BeEmpty())
		})
	})

	Context("When HTTPRoute does not exist", func() {
		const (
			resourceName  = "test-mcpsr-no-route"
			httpRouteName = "nonexistent-route"
		)

		ctx := context.Background()

		mcpsrNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		AfterEach(func() {
			forceDeleteTestMCPServerRegistration(ctx, resourceName, "default")
		})

		It("should set status to NotReady when HTTPRoute does not exist", func() {
			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			// reconcile to add finalizer, wait for cache sync, then reconcile again to process
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

			_, _ = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})

			Eventually(func(g Gomega) {
				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})

	Context("When MCPServerRegistration has caCertSecretRef", func() {
		const (
			resourceName  = "test-mcpsr-cacert"
			httpRouteName = "test-route-cacert"
			gatewayName   = "test-gw-cacert"
			serviceName   = "test-svc-cacert"
			secretName    = "test-ca-bundle"
		)

		ctx := context.Background()

		mcpsrNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			gw := createTestGateway(gatewayName, "default")
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())

			svc := createTestService(serviceName, "default", 8080)
			Expect(testK8sClient.Create(ctx, svc)).To(Succeed())

			httpRoute := createTestHTTPRoute(httpRouteName, "default", "test.example.com", serviceName, 8080, gatewayName, "default")
			Expect(testK8sClient.Create(ctx, httpRoute)).To(Succeed())

			Eventually(func(g Gomega) {
				route := &gatewayv1.HTTPRoute{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: httpRouteName, Namespace: "default"}, route)).To(Succeed())
				g.Expect(setHTTPRouteAcceptedStatus(ctx, route, gatewayName, "default")).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())

			mcpExt := createTestMCPGatewayExtension("test-ext-cacert", "default", gatewayName, "default")
			Expect(testK8sClient.Create(ctx, mcpExt)).To(Succeed())

			Eventually(func(g Gomega) {
				ext := &mcpv1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: "test-ext-cacert", Namespace: "default"}, ext)).To(Succeed())
				ext.SetReadyCondition(metav1.ConditionTrue, mcpv1.ConditionReasonSuccess, "ready")
				g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())

			testCaPEM := generateIntegrationTestCACertPEM()
			caSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: "default",
					Labels: map[string]string{
						"mcp.kuadrant.io/secret": "true",
					},
				},
				Data: map[string][]byte{
					"ca.crt": testCaPEM,
				},
			}
			Expect(testK8sClient.Create(ctx, caSecret)).To(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPServerRegistration(ctx, resourceName, "default")
			forceDeleteTestMCPGatewayExtension(ctx, "test-ext-cacert", "default")
			deleteTestHTTPRoute(ctx, httpRouteName, "default")
			deleteTestService(ctx, serviceName, "default")
			deleteTestGateway(ctx, gatewayName, "default")
			_ = client.IgnoreNotFound(testK8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
			}))
		})

		It("should include CA cert in config when caCertSecretRef is set", func() {
			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			mcpsr.Spec.CACertSecretRef = &mcpv1.CACertSecretReference{
				Name: secretName,
				Key:  "ca.crt",
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			// reconcile to add finalizer, wait for cache sync, then reconcile again to process
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(configWriter.upsertedServers).NotTo(BeEmpty())
				for _, server := range configWriter.upsertedServers {
					if server.Name == fmt.Sprintf("default/%s", resourceName) {
						g.Expect(server.CACert).To(ContainSubstring("BEGIN CERTIFICATE"))
						return
					}
				}
				g.Expect(false).To(BeTrue(), "server not found in upserted configs")
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should fail when CA cert secret is missing required label", func() {
			unlabeledSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unlabeled-ca",
					Namespace: "default",
				},
				Data: map[string][]byte{
					"ca.crt": []byte("-----BEGIN CERTIFICATE-----\ndata\n-----END CERTIFICATE-----"),
				},
			}
			Expect(testK8sClient.Create(ctx, unlabeledSecret)).To(Succeed())
			defer func() {
				_ = testK8sClient.Delete(ctx, unlabeledSecret)
			}()

			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			mcpsr.Spec.CACertSecretRef = &mcpv1.CACertSecretReference{
				Name: "unlabeled-ca",
				Key:  "ca.crt",
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			// reconcile to add finalizer, wait for cache sync, then reconcile again to process
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

			_, _ = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})

			Eventually(func(g Gomega) {
				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Message).To(ContainSubstring("missing required label"))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should use default key ca.crt when key is not specified", func() {
			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			mcpsr.Spec.CACertSecretRef = &mcpv1.CACertSecretReference{
				Name: secretName,
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			// reconcile to add finalizer, wait for cache sync, then reconcile again to process
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(configWriter.upsertedServers).NotTo(BeEmpty())
				for _, server := range configWriter.upsertedServers {
					if server.Name == fmt.Sprintf("default/%s", resourceName) {
						g.Expect(server.CACert).To(ContainSubstring("BEGIN CERTIFICATE"))
						return
					}
				}
				g.Expect(false).To(BeTrue(), "server not found in upserted configs")
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should fail when CA cert secret does not exist", func() {
			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			mcpsr.Spec.CACertSecretRef = &mcpv1.CACertSecretReference{
				Name: "nonexistent-secret",
				Key:  "ca.crt",
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			// reconcile to add finalizer, wait for cache sync, then reconcile again to process
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

			_, _ = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})

			Eventually(func(g Gomega) {
				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Message).To(ContainSubstring("not found"))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should fail when CA cert secret is missing the expected key", func() {
			wrongKeySecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "wrong-key-ca",
					Namespace: "default",
					Labels: map[string]string{
						"mcp.kuadrant.io/secret": "true",
					},
				},
				Data: map[string][]byte{
					"cert.pem": []byte("-----BEGIN CERTIFICATE-----\ndata\n-----END CERTIFICATE-----"),
				},
			}
			Expect(testK8sClient.Create(ctx, wrongKeySecret)).To(Succeed())
			defer func() {
				_ = testK8sClient.Delete(ctx, wrongKeySecret)
			}()

			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			mcpsr.Spec.CACertSecretRef = &mcpv1.CACertSecretReference{
				Name: "wrong-key-ca",
				Key:  "ca.crt",
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			// reconcile to add finalizer, wait for cache sync, then reconcile again to process
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

			_, _ = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})

			Eventually(func(g Gomega) {
				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Message).To(ContainSubstring("missing key"))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should include both credential and CA cert when both refs are set", func() {
			credSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cred-with-ca",
					Namespace: "default",
					Labels: map[string]string{
						"mcp.kuadrant.io/secret": "true",
					},
				},
				Data: map[string][]byte{
					"token": []byte("Bearer test-token"),
				},
			}
			Expect(testK8sClient.Create(ctx, credSecret)).To(Succeed())
			defer func() {
				_ = testK8sClient.Delete(ctx, credSecret)
			}()

			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			mcpsr.Spec.CredentialRef = &mcpv1.SecretReference{
				Name: "test-cred-with-ca",
				Key:  "token",
			}
			mcpsr.Spec.CACertSecretRef = &mcpv1.CACertSecretReference{
				Name: secretName,
				Key:  "ca.crt",
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			// reconcile to add finalizer, wait for cache sync, then reconcile again to process
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				g.Expect(configWriter.upsertedServers).NotTo(BeEmpty())
				for _, server := range configWriter.upsertedServers {
					if server.Name == fmt.Sprintf("default/%s", resourceName) {
						g.Expect(server.CACert).To(ContainSubstring("BEGIN CERTIFICATE"))
						g.Expect(server.Credential).To(Equal("Bearer test-token"))
						return
					}
				}
				g.Expect(false).To(BeTrue(), "server not found in upserted configs")
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should fail when CA cert contains invalid PEM data", func() {
			invalidPEMSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-pem-ca",
					Namespace: "default",
					Labels: map[string]string{
						"mcp.kuadrant.io/secret": "true",
					},
				},
				Data: map[string][]byte{
					"ca.crt": []byte("this is not valid PEM data"),
				},
			}
			Expect(testK8sClient.Create(ctx, invalidPEMSecret)).To(Succeed())
			defer func() {
				_ = testK8sClient.Delete(ctx, invalidPEMSecret)
			}()

			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			mcpsr.Spec.CACertSecretRef = &mcpv1.CACertSecretReference{
				Name: "invalid-pem-ca",
				Key:  "ca.crt",
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			// reconcile to add finalizer, wait for cache sync, then reconcile again to process
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

			_, _ = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})

			Eventually(func(g Gomega) {
				mcpsrObj := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, mcpsrObj)).To(Succeed())
				readyCond := meta.FindStatusCondition(mcpsrObj.Status.Conditions, "Ready")
				g.Expect(readyCond).NotTo(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCond.Message).To(ContainSubstring("invalid"))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should fail when CA cert data exceeds maximum size", func() {
			oversizedData := make([]byte, maxCACertSize+1)
			for i := range oversizedData {
				oversizedData[i] = 'A'
			}
			oversizedSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "oversized-ca",
					Namespace: "default",
					Labels: map[string]string{
						"mcp.kuadrant.io/secret": "true",
					},
				},
				Data: map[string][]byte{
					"ca.crt": oversizedData,
				},
			}
			Expect(testK8sClient.Create(ctx, oversizedSecret)).To(Succeed())
			defer func() {
				_ = testK8sClient.Delete(ctx, oversizedSecret)
			}()

			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			mcpsr.Spec.CACertSecretRef = &mcpv1.CACertSecretReference{
				Name: "oversized-ca",
				Key:  "ca.crt",
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			// reconcile to add finalizer, wait for cache sync, then reconcile again to process
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

			_, _ = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})

			Eventually(func(g Gomega) {
				mcpsrObj := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, mcpsrObj)).To(Succeed())
				readyCond := meta.FindStatusCondition(mcpsrObj.Status.Conditions, "Ready")
				g.Expect(readyCond).NotTo(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCond.Message).To(ContainSubstring("exceeds maximum size"))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})

	Context("When MCPServerRegistration has oauth2ClientCredentials", func() {
		const (
			resourceName  = "test-mcpsr-oauth2"
			httpRouteName = "test-route-oauth2"
			gatewayName   = "test-gw-oauth2"
			serviceName   = "test-svc-oauth2"
			secretName    = "test-oauth2-client"
			tokenURL      = "https://as.example.com/realms/mcp/protocol/openid-connect/token"
			clientSecret  = "initial-client-secret"
		)

		ctx := context.Background()

		mcpsrNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		// reconcileTwice adds the finalizer on the first pass and processes the spec on the second
		reconcileTwice := func(reconciler *MCPReconciler) {
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)
			_, _ = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
		}

		BeforeEach(func() {
			gw := createTestGateway(gatewayName, "default")
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())

			svc := createTestService(serviceName, "default", 8080)
			Expect(testK8sClient.Create(ctx, svc)).To(Succeed())

			httpRoute := createTestHTTPRoute(httpRouteName, "default", "test.example.com", serviceName, 8080, gatewayName, "default")
			Expect(testK8sClient.Create(ctx, httpRoute)).To(Succeed())

			Eventually(func(g Gomega) {
				route := &gatewayv1.HTTPRoute{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: httpRouteName, Namespace: "default"}, route)).To(Succeed())
				g.Expect(setHTTPRouteAcceptedStatus(ctx, route, gatewayName, "default")).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())

			mcpExt := createTestMCPGatewayExtension("test-ext-oauth2", "default", gatewayName, "default")
			Expect(testK8sClient.Create(ctx, mcpExt)).To(Succeed())

			Eventually(func(g Gomega) {
				ext := &mcpv1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: "test-ext-oauth2", Namespace: "default"}, ext)).To(Succeed())
				ext.SetReadyCondition(metav1.ConditionTrue, mcpv1.ConditionReasonSuccess, "ready")
				g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())

			oauthSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      secretName,
					Namespace: "default",
					Labels: map[string]string{
						"mcp.kuadrant.io/secret": "true",
					},
				},
				Data: map[string][]byte{
					"clientID":     []byte("mcp-broker"),
					"clientSecret": []byte(clientSecret),
				},
			}
			Expect(testK8sClient.Create(ctx, oauthSecret)).To(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPServerRegistration(ctx, resourceName, "default")
			forceDeleteTestMCPGatewayExtension(ctx, "test-ext-oauth2", "default")
			deleteTestHTTPRoute(ctx, httpRouteName, "default")
			deleteTestService(ctx, serviceName, "default")
			deleteTestGateway(ctx, gatewayName, "default")
			_ = client.IgnoreNotFound(testK8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "default"},
			}))
		})

		createOAuth2Registration := func(secretRefName string) {
			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			mcpsr.Spec.OAuth2ClientCredentials = &mcpv1.OAuth2ClientCredentialsConfig{
				TokenURL:  tokenURL,
				SecretRef: mcpv1.ClientCredentialsSecretReference{Name: secretRefName},
				Scopes:    []string{"mcp.read"},
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())
		}

		expectUpsertedOAuth2 := func(configWriter *mockMCPServerConfigReaderWriter, wantClientSecret string) {
			Eventually(func(g Gomega) {
				server, ok := configWriter.upsertedServers[fmt.Sprintf("default/default/%s", resourceName)]
				g.Expect(ok).To(BeTrue(), "server not found in upserted configs")
				g.Expect(server.OAuth2).NotTo(BeNil())
				g.Expect(server.OAuth2.TokenURL).To(Equal(tokenURL))
				g.Expect(server.OAuth2.ClientID).To(Equal("mcp-broker"))
				g.Expect(server.OAuth2.ClientSecret).To(Equal(wantClientSecret))
				g.Expect(server.OAuth2.Scopes).To(Equal([]string{"mcp.read"}))
				g.Expect(server.Credential).To(BeEmpty())
			}, testTimeout, testRetryInterval).Should(Succeed())
		}

		It("should include the resolved client credentials in the config", func() {
			createOAuth2Registration(secretName)

			configWriter := newMockMCPServerConfigReaderWriter()
			reconcileTwice(newMCPServerReconciler(configWriter))

			expectUpsertedOAuth2(configWriter, clientSecret)
		})

		// one representative failure: resolution errors are covered by
		// TestResolveOAuth2ClientCredentials, this proves one reaches the condition
		It("should fail without leaking the client secret when clientID is missing", func() {
			noID := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-client-id",
					Namespace: "default",
					Labels:    map[string]string{"mcp.kuadrant.io/secret": "true"},
				},
				Data: map[string][]byte{"clientSecret": []byte(clientSecret)},
			}
			Expect(testK8sClient.Create(ctx, noID)).To(Succeed())
			defer func() {
				_ = testK8sClient.Delete(ctx, noID)
			}()

			createOAuth2Registration("no-client-id")

			reconcileTwice(newMCPServerReconciler(newMockMCPServerConfigReaderWriter()))

			Eventually(func(g Gomega) {
				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Message).To(ContainSubstring("missing or empty key clientID"))
				g.Expect(cond.Message).NotTo(ContainSubstring(clientSecret))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})

	Context("When HTTPRoute has no accepted gateways", func() {
		const (
			resourceName  = "test-mcpsr-not-accepted"
			httpRouteName = "test-route-not-accepted"
			gatewayName   = "test-gw-not-accepted"
			serviceName   = "test-svc-not-accepted"
		)

		ctx := context.Background()

		mcpsrNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			// create gateway
			gw := createTestGateway(gatewayName, "default")
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())

			// create service
			svc := createTestService(serviceName, "default", 8080)
			Expect(testK8sClient.Create(ctx, svc)).To(Succeed())

			// create HTTPRoute (without setting accepted status)
			httpRoute := createTestHTTPRoute(httpRouteName, "default", "test.mcp.local", serviceName, 8080, gatewayName, "default")
			Expect(testK8sClient.Create(ctx, httpRoute)).To(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPServerRegistration(ctx, resourceName, "default")
			deleteTestHTTPRoute(ctx, httpRouteName, "default")
			deleteTestService(ctx, serviceName, "default")
			deleteTestGateway(ctx, gatewayName, "default")
		})

		It("should set status to NotReady when no gateways have accepted the HTTPRoute", func() {
			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			// reconcile to add finalizer, wait for cache sync, then reconcile again to process
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

			_, _ = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpsrNamespacedName,
			})

			Eventually(func(g Gomega) {
				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Message).To(ContainSubstring("no valid gateways"))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})

	Context("prefix field CRD validation", func() {
		ctx := context.Background()

		AfterEach(func() {
			forceDeleteTestMCPServerRegistration(ctx, "prefix-valid", "default")
		})

		DescribeTable("rejects invalid prefix values",
			func(prefix string) {
				mcpsr := createTestMCPServerRegistration("prefix-invalid", "default", "some-route", prefix)
				err := testK8sClient.Create(ctx, mcpsr)
				Expect(err).To(HaveOccurred())
				Expect(errors.IsInvalid(err)).To(BeTrue(), "expected Invalid error, got: %v", err)
			},
			Entry("uppercase letters", "MyServer_"),
			Entry("hyphen", "my-server"),
			Entry("starts with underscore", "_test1"),
			Entry("space", "my server"),
			Entry("special characters", "test!@#"),
			Entry("mixed case", "testServer"),
		)

		DescribeTable("accepts valid prefix values",
			func(prefix string) {
				mcpsr := createTestMCPServerRegistration("prefix-valid", "default", "some-route", prefix)
				Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())
				forceDeleteTestMCPServerRegistration(ctx, "prefix-valid", "default")
			},
			Entry("lowercase with trailing underscore", "test_"),
			Entry("alphanumeric with underscore", "server1_"),
			Entry("single letter", "a"),
			Entry("digits and underscores", "s1_prefix_"),
			Entry("all lowercase", "weatherserver"),
		)
	})

	Context("oauth2ClientCredentials field CRD validation", func() {
		ctx := context.Background()

		newOAuth2Registration := func(name string, oauth2 *mcpv1.OAuth2ClientCredentialsConfig) *mcpv1.MCPServerRegistration {
			mcpsr := createTestMCPServerRegistration(name, "default", "some-route", "cc_")
			mcpsr.Spec.OAuth2ClientCredentials = oauth2
			return mcpsr
		}

		AfterEach(func() {
			forceDeleteTestMCPServerRegistration(ctx, "oauth2-valid", "default")
		})

		It("accepts oauth2ClientCredentials on its own", func() {
			mcpsr := newOAuth2Registration("oauth2-valid", &mcpv1.OAuth2ClientCredentialsConfig{
				TokenURL:  "https://as.example.com/token",
				SecretRef: mcpv1.ClientCredentialsSecretReference{Name: "oauth-client"},
				Scopes:    []string{"mcp.read"},
			})
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())
		})

		It("rejects credentialRef and oauth2ClientCredentials set together", func() {
			mcpsr := newOAuth2Registration("oauth2-invalid", &mcpv1.OAuth2ClientCredentialsConfig{
				TokenURL:  "https://as.example.com/token",
				SecretRef: mcpv1.ClientCredentialsSecretReference{Name: "oauth-client"},
			})
			mcpsr.Spec.CredentialRef = &mcpv1.SecretReference{Name: "static-token", Key: "token"}

			err := testK8sClient.Create(ctx, mcpsr)
			Expect(err).To(HaveOccurred())
			Expect(errors.IsInvalid(err)).To(BeTrue(), "expected Invalid error, got: %v", err)
			Expect(err.Error()).To(ContainSubstring("mutually exclusive"))
		})

		DescribeTable("rejects invalid oauth2ClientCredentials values",
			func(oauth2 *mcpv1.OAuth2ClientCredentialsConfig) {
				err := testK8sClient.Create(ctx, newOAuth2Registration("oauth2-invalid", oauth2))
				Expect(err).To(HaveOccurred())
				Expect(errors.IsInvalid(err)).To(BeTrue(), "expected Invalid error, got: %v", err)
			},
			Entry("http tokenURL", &mcpv1.OAuth2ClientCredentialsConfig{
				TokenURL:  "http://as.example.com/token",
				SecretRef: mcpv1.ClientCredentialsSecretReference{Name: "oauth-client"},
			}),
			Entry("empty tokenURL", &mcpv1.OAuth2ClientCredentialsConfig{
				SecretRef: mcpv1.ClientCredentialsSecretReference{Name: "oauth-client"},
			}),
			Entry("empty secretRef name", &mcpv1.OAuth2ClientCredentialsConfig{
				TokenURL: "https://as.example.com/token",
			}),
			Entry("more than 10 scopes", &mcpv1.OAuth2ClientCredentialsConfig{
				TokenURL:  "https://as.example.com/token",
				SecretRef: mcpv1.ClientCredentialsSecretReference{Name: "oauth-client"},
				Scopes:    []string{"s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9", "s10", "s11"},
			}),
		)
	})

	Context("When two MCPServerRegistrations feeding the same MCPGatewayExtension share a prefix", func() {
		const (
			resourceName1 = "test-prefix-conflict-1"
			resourceName2 = "test-prefix-conflict-2"
			httpRouteName = "test-prefix-conflict-route"
			gatewayName   = "test-prefix-conflict-gw"
			serviceName   = "test-prefix-conflict-svc"
			extName       = "test-prefix-conflict-ext"
		)

		ctx := context.Background()

		mcpsrNamespacedName1 := types.NamespacedName{Name: resourceName1, Namespace: "default"}
		mcpsrNamespacedName2 := types.NamespacedName{Name: resourceName2, Namespace: "default"}

		BeforeEach(func() {
			gw := createTestGateway(gatewayName, "default")
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())

			svc := createTestService(serviceName, "default", 8080)
			Expect(testK8sClient.Create(ctx, svc)).To(Succeed())

			httpRoute := createTestHTTPRoute(httpRouteName, "default", "test.example.com", serviceName, 8080, gatewayName, "default")
			Expect(testK8sClient.Create(ctx, httpRoute)).To(Succeed())

			Eventually(func(g Gomega) {
				route := &gatewayv1.HTTPRoute{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: httpRouteName, Namespace: "default"}, route)).To(Succeed())
				g.Expect(setHTTPRouteAcceptedStatus(ctx, route, gatewayName, "default")).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())

			mcpExt := createTestMCPGatewayExtension(extName, "default", gatewayName, "default")
			Expect(testK8sClient.Create(ctx, mcpExt)).To(Succeed())

			Eventually(func(g Gomega) {
				ext := &mcpv1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extName, Namespace: "default"}, ext)).To(Succeed())
				ext.SetReadyCondition(metav1.ConditionTrue, mcpv1.ConditionReasonSuccess, "ready")
				g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPServerRegistration(ctx, resourceName1, "default")
			forceDeleteTestMCPServerRegistration(ctx, resourceName2, "default")
			forceDeleteTestMCPGatewayExtension(ctx, extName, "default")
			deleteTestHTTPRoute(ctx, httpRouteName, "default")
			deleteTestService(ctx, serviceName, "default")
			deleteTestGateway(ctx, gatewayName, "default")
		})

		It("rejects the second registration with a PrefixConflict status condition", func() {
			reg1 := createTestMCPServerRegistration(resourceName1, "default", httpRouteName, "dup_")
			Expect(testK8sClient.Create(ctx, reg1)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName1)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName1})
			Expect(err).NotTo(HaveOccurred())
			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName1)

			Eventually(func(g Gomega) {
				_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName1})
				g.Expect(reconcileErr).NotTo(HaveOccurred())

				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName1, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}, testTimeout, testRetryInterval).Should(Succeed())

			// ensure a distinct, later CreationTimestamp for the second registration
			time.Sleep(1100 * time.Millisecond)

			reg2 := createTestMCPServerRegistration(resourceName2, "default", httpRouteName, "dup_")
			Expect(testK8sClient.Create(ctx, reg2)).To(Succeed())

			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName2)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName2})
			Expect(err).NotTo(HaveOccurred())
			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName2)

			Eventually(func(g Gomega) {
				_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName2})
				g.Expect(reconcileErr).NotTo(HaveOccurred())

				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName2, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(conditionReasonPrefixConflict))
				g.Expect(cond.Message).To(ContainSubstring("conflict"))
			}, testTimeout, testRetryInterval).Should(Succeed())

			// re-reconciling (what an update to any watched object triggers) must not
			// flip the older, already-Ready registration to conflicted
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName1})
			Expect(err).NotTo(HaveOccurred())

			updated1 := &mcpv1.MCPServerRegistration{}
			Expect(testK8sClient.Get(ctx, mcpsrNamespacedName1, updated1)).To(Succeed())
			cond1 := meta.FindStatusCondition(updated1.Status.Conditions, "Ready")
			Expect(cond1).NotTo(BeNil())
			Expect(cond1.Status).To(Equal(metav1.ConditionTrue))
		})

		It("does not reject a sibling with a substring-colliding but non-identical prefix", func() {
			reg1 := createTestMCPServerRegistration(resourceName1, "default", httpRouteName, "app_")
			Expect(testK8sClient.Create(ctx, reg1)).To(Succeed())
			reg2 := createTestMCPServerRegistration(resourceName2, "default", httpRouteName, "app_admin_")
			Expect(testK8sClient.Create(ctx, reg2)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName1)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName2)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName1})
			Expect(err).NotTo(HaveOccurred())
			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName1)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName2})
			Expect(err).NotTo(HaveOccurred())
			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName2)

			for _, nn := range []types.NamespacedName{mcpsrNamespacedName1, mcpsrNamespacedName2} {
				Eventually(func(g Gomega) {
					_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
					g.Expect(reconcileErr).NotTo(HaveOccurred())

					updated := &mcpv1.MCPServerRegistration{}
					g.Expect(testK8sClient.Get(ctx, nn, updated)).To(Succeed())
					cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
					g.Expect(cond).NotTo(BeNil())
					g.Expect(cond.Status).To(Equal(metav1.ConditionTrue),
						"substring-colliding prefixes must not be rejected here - resolved deterministically by longest-prefix-match instead")
				}, testTimeout, testRetryInterval).Should(Succeed())
			}
		})

		It("stops rejecting once the conflicting sibling is deleted", func() {
			reg1 := createTestMCPServerRegistration(resourceName1, "default", httpRouteName, "dup_")
			Expect(testK8sClient.Create(ctx, reg1)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName1)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName1})
			Expect(err).NotTo(HaveOccurred())
			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName1)
			Eventually(func(g Gomega) {
				_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName1})
				g.Expect(reconcileErr).NotTo(HaveOccurred())
				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName1, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}, testTimeout, testRetryInterval).Should(Succeed())

			time.Sleep(1100 * time.Millisecond)

			reg2 := createTestMCPServerRegistration(resourceName2, "default", httpRouteName, "dup_")
			Expect(testK8sClient.Create(ctx, reg2)).To(Succeed())
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName2)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName2})
			Expect(err).NotTo(HaveOccurred())
			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName2)
			Eventually(func(g Gomega) {
				_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName2})
				g.Expect(reconcileErr).NotTo(HaveOccurred())
				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName2, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Reason).To(Equal(conditionReasonPrefixConflict))
			}, testTimeout, testRetryInterval).Should(Succeed())

			// delete the conflicting sibling (reg1) entirely
			forceDeleteTestMCPServerRegistration(ctx, resourceName1, "default")

			// reconciling reg2 again must now succeed - no active sibling shares its prefix
			Eventually(func(g Gomega) {
				_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName2})
				g.Expect(reconcileErr).NotTo(HaveOccurred())
				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName2, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})

	Context("When two MCPServerRegistrations feeding different MCPGatewayExtensions share a prefix", func() {
		const (
			resourceNameA = "test-prefix-noconflict-a"
			resourceNameB = "test-prefix-noconflict-b"
			routeNameA    = "test-prefix-noconflict-route-a"
			routeNameB    = "test-prefix-noconflict-route-b"
			gatewayName   = "test-prefix-noconflict-gw"
			serviceName   = "test-prefix-noconflict-svc"
			extNamespaceA = "test-prefix-noconflict-ext-ns-a"
			extNamespaceB = "test-prefix-noconflict-ext-ns-b"
			extNameA      = "test-prefix-noconflict-ext-a"
			extNameB      = "test-prefix-noconflict-ext-b"
		)

		ctx := context.Background()

		mcpsrNamespacedNameA := types.NamespacedName{Name: resourceNameA, Namespace: "default"}
		mcpsrNamespacedNameB := types.NamespacedName{Name: resourceNameB, Namespace: "default"}

		var refGrantA, refGrantB client.Object

		BeforeEach(func() {
			createTestNamespace(ctx, extNamespaceA)
			createTestNamespace(ctx, extNamespaceB)

			// one Gateway, two listeners on different ports - mirrors how a single
			// shared Gateway can host multiple independent MCPGatewayExtensions
			// (see the resources-federation e2e listeners for the real-world example)
			hostnameA := gatewayv1.Hostname("a.test.example.com")
			hostnameB := gatewayv1.Hostname("b.test.example.com")
			gw := &gatewayv1.Gateway{
				ObjectMeta: metav1.ObjectMeta{Name: gatewayName, Namespace: "default"},
				Spec: gatewayv1.GatewaySpec{
					GatewayClassName: "test-class",
					Listeners: []gatewayv1.Listener{
						{Name: "listener-a", Port: 8080, Protocol: gatewayv1.HTTPProtocolType, Hostname: &hostnameA},
						{Name: "listener-b", Port: 8081, Protocol: gatewayv1.HTTPProtocolType, Hostname: &hostnameB},
					},
				},
			}
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())

			svc := createTestService(serviceName, "default", 8080)
			Expect(testK8sClient.Create(ctx, svc)).To(Succeed())

			routeA := createTestHTTPRoute(routeNameA, "default", "a.test.example.com", serviceName, 8080, gatewayName, "default")
			Expect(testK8sClient.Create(ctx, routeA)).To(Succeed())
			routeB := createTestHTTPRoute(routeNameB, "default", "b.test.example.com", serviceName, 8080, gatewayName, "default")
			Expect(testK8sClient.Create(ctx, routeB)).To(Succeed())

			Eventually(func(g Gomega) {
				route := &gatewayv1.HTTPRoute{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: routeNameA, Namespace: "default"}, route)).To(Succeed())
				g.Expect(setHTTPRouteAcceptedStatus(ctx, route, gatewayName, "default")).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())
			Eventually(func(g Gomega) {
				route := &gatewayv1.HTTPRoute{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: routeNameB, Namespace: "default"}, route)).To(Succeed())
				g.Expect(setHTTPRouteAcceptedStatus(ctx, route, gatewayName, "default")).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// extensions live in different namespaces than the Gateway, so each needs a ReferenceGrant
			gwNameForGrant := gatewayName
			refGrantA = createTestReferenceGrant("allow-ext-a", "default", extNamespaceA, &gwNameForGrant)
			Expect(testK8sClient.Create(ctx, refGrantA)).To(Succeed())
			refGrantB = createTestReferenceGrant("allow-ext-b", "default", extNamespaceB, &gwNameForGrant)
			Expect(testK8sClient.Create(ctx, refGrantB)).To(Succeed())

			extA := createTestMCPGatewayExtension(extNameA, extNamespaceA, gatewayName, "default")
			extA.Spec.TargetRef.SectionName = "listener-a"
			Expect(testK8sClient.Create(ctx, extA)).To(Succeed())
			extB := createTestMCPGatewayExtension(extNameB, extNamespaceB, gatewayName, "default")
			extB.Spec.TargetRef.SectionName = "listener-b"
			Expect(testK8sClient.Create(ctx, extB)).To(Succeed())

			Eventually(func(g Gomega) {
				ext := &mcpv1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extNameA, Namespace: extNamespaceA}, ext)).To(Succeed())
				ext.SetReadyCondition(metav1.ConditionTrue, mcpv1.ConditionReasonSuccess, "ready")
				g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())
			Eventually(func(g Gomega) {
				ext := &mcpv1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extNameB, Namespace: extNamespaceB}, ext)).To(Succeed())
				ext.SetReadyCondition(metav1.ConditionTrue, mcpv1.ConditionReasonSuccess, "ready")
				g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPServerRegistration(ctx, resourceNameA, "default")
			forceDeleteTestMCPServerRegistration(ctx, resourceNameB, "default")
			forceDeleteTestMCPGatewayExtension(ctx, extNameA, extNamespaceA)
			forceDeleteTestMCPGatewayExtension(ctx, extNameB, extNamespaceB)
			_ = testK8sClient.Delete(ctx, refGrantA)
			_ = testK8sClient.Delete(ctx, refGrantB)
			deleteTestHTTPRoute(ctx, routeNameA, "default")
			deleteTestHTTPRoute(ctx, routeNameB, "default")
			deleteTestService(ctx, serviceName, "default")
			deleteTestGateway(ctx, gatewayName, "default")
		})

		It("does not reject either registration - they feed separate broker instances", func() {
			regA := createTestMCPServerRegistration(resourceNameA, "default", routeNameA, "dup_")
			Expect(testK8sClient.Create(ctx, regA)).To(Succeed())
			regB := createTestMCPServerRegistration(resourceNameB, "default", routeNameB, "dup_")
			Expect(testK8sClient.Create(ctx, regB)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedNameA)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedNameB)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedNameA})
			Expect(err).NotTo(HaveOccurred())
			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedNameA)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedNameB})
			Expect(err).NotTo(HaveOccurred())
			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedNameB)

			Eventually(func(g Gomega) {
				_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedNameA})
				g.Expect(reconcileErr).NotTo(HaveOccurred())

				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedNameA, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}, testTimeout, testRetryInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedNameB})
				g.Expect(reconcileErr).NotTo(HaveOccurred())

				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedNameB, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).NotTo(Equal(conditionReasonPrefixConflict))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})

	Context("When the target MCPGatewayExtension has NeMo guardrails configured", func() {
		// tests per-server NeMo guardrails: the gateway enables guardrails via
		// a Secret + "guardrails-ref" annotation, and a server can add its own
		// config IDs via "guardrails-config-ids".
		const (
			resourceName         = "test-mcpsr-nemo"
			httpRouteName        = "test-route-nemo"
			gatewayName          = "test-gw-nemo"
			serviceName          = "test-svc-nemo"
			extName              = "test-ext-nemo"
			guardrailsSecretName = "nemo-guardrails-config"
		)

		ctx := context.Background()

		mcpsrNamespacedName := types.NamespacedName{Name: resourceName, Namespace: "default"}

		BeforeEach(func() {
			gw := createTestGateway(gatewayName, "default")
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())

			svc := createTestService(serviceName, "default", 8080)
			Expect(testK8sClient.Create(ctx, svc)).To(Succeed())

			httpRoute := createTestHTTPRoute(httpRouteName, "default", "test.example.com", serviceName, 8080, gatewayName, "default")
			Expect(testK8sClient.Create(ctx, httpRoute)).To(Succeed())

			Eventually(func(g Gomega) {
				route := &gatewayv1.HTTPRoute{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: httpRouteName, Namespace: "default"}, route)).To(Succeed())
				g.Expect(setHTTPRouteAcceptedStatus(ctx, route, gatewayName, "default")).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPServerRegistration(ctx, resourceName, "default")
			forceDeleteTestMCPGatewayExtension(ctx, extName, "default")
			deleteTestHTTPRoute(ctx, httpRouteName, "default")
			deleteTestService(ctx, serviceName, "default")
			deleteTestGateway(ctx, gatewayName, "default")
			_ = client.IgnoreNotFound(testK8sClient.Delete(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: guardrailsSecretName, Namespace: "default"},
			}))
		})

		// assertFailsClosedForServerWithNoOverride is shared by the
		// GuardrailsSecretNotFound and GuardrailsSecretInvalid. both
		// expect a server with no per-server override to be rejected
		// once the extension's GuardrailsResolved condition carries a
		// guardrails-specific not-ready reason.
		assertFailsClosedForServerWithNoOverride := func() {
			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "nemo_")
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

			_, _ = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})

			Eventually(func(g Gomega) {
				updated := &mcpv1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).To(Equal(conditionReasonGatewayGuardrailsNotConfigured))
				g.Expect(cond.Message).To(ContainSubstring("guardrails secret"))
			}, testTimeout, testRetryInterval).Should(Succeed())

			// fails closed - no config written for this server
			Expect(configWriter.upsertedServers).To(BeEmpty())
		}
		Context("and the gateway's guardrails-ref resolves to a valid NeMo secret", func() {
			BeforeEach(func() {
				secret := createTestNeMoGuardrailsSecret(guardrailsSecretName, "default")
				Expect(testK8sClient.Create(ctx, secret)).To(Succeed())

				mcpExt := createTestMCPGatewayExtension(extName, "default", gatewayName, "default")
				mcpExt.Annotations = map[string]string{labelGuardrailsReference: guardrailsSecretName}
				Expect(testK8sClient.Create(ctx, mcpExt)).To(Succeed())

				// mark the extension Ready directly, as if it already picked up
				// the secret.
				Eventually(func(g Gomega) {
					ext := &mcpv1.MCPGatewayExtension{}
					g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extName, Namespace: "default"}, ext)).To(Succeed())
					ext.SetReadyCondition(metav1.ConditionTrue, mcpv1.ConditionReasonSuccess, "ready")
					g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
				}, testTimeout, testRetryInterval).Should(Succeed())
			})

			It("merges the per-server guardrails-config-ids annotation into the server config", func() {
				mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "nemo_")
				mcpsr.Annotations = map[string]string{ManagedGuardrailsAnnotation: "pii-detection,strict-input-checking"}
				Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

				configWriter := newMockMCPServerConfigReaderWriter()
				reconciler := newMCPServerReconciler(configWriter)
				waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
				Expect(err).NotTo(HaveOccurred())
				waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

				Eventually(func(g Gomega) {
					_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
					g.Expect(reconcileErr).NotTo(HaveOccurred())

					updated := &mcpv1.MCPServerRegistration{}
					g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
					cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
					g.Expect(cond).NotTo(BeNil())
					g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				}, testTimeout, testRetryInterval).Should(Succeed())

				Eventually(func(g Gomega) {
					g.Expect(configWriter.upsertedServers).NotTo(BeEmpty())
					for _, server := range configWriter.upsertedServers {
						if server.Name == fmt.Sprintf("default/%s", resourceName) {
							g.Expect(server.GuardrailsConfigIDs).To(Equal([]string{"pii-detection", "strict-input-checking"}))
							return
						}
					}
					g.Expect(false).To(BeTrue(), "server not found in upserted configs")
				}, testTimeout, testRetryInterval).Should(Succeed())
			})

			It("succeeds using only the gateway-level NeMo config when no per-server override is set", func() {
				mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "nemo_")
				Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

				configWriter := newMockMCPServerConfigReaderWriter()
				reconciler := newMCPServerReconciler(configWriter)
				waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
				Expect(err).NotTo(HaveOccurred())
				waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

				Eventually(func(g Gomega) {
					_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
					g.Expect(reconcileErr).NotTo(HaveOccurred())

					updated := &mcpv1.MCPServerRegistration{}
					g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
					cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
					g.Expect(cond).NotTo(BeNil())
					g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				}, testTimeout, testRetryInterval).Should(Succeed())

				Eventually(func(g Gomega) {
					g.Expect(configWriter.upsertedServers).NotTo(BeEmpty())
					for _, server := range configWriter.upsertedServers {
						if server.Name == fmt.Sprintf("default/%s", resourceName) {
							g.Expect(server.GuardrailsConfigIDs).To(BeEmpty())
							return
						}
					}
					g.Expect(false).To(BeTrue(), "server not found in upserted configs")
				}, testTimeout, testRetryInterval).Should(Succeed())
			})
		})

		Context("and an unrelated extension secret is invalid, so gateway guardrails were never resolved", func() {
			BeforeEach(func() {
				secret := createTestNeMoGuardrailsSecret(guardrailsSecretName, "default")
				Expect(testK8sClient.Create(ctx, secret)).To(Succeed())

				mcpExt := createTestMCPGatewayExtension(extName, "default", gatewayName, "default")
				mcpExt.Annotations = map[string]string{labelGuardrailsReference: guardrailsSecretName}
				Expect(testK8sClient.Create(ctx, mcpExt)).To(Succeed())

				// reconcileActive stopped before resolveGuardrails/WriteGatewayConfig
				Eventually(func(g Gomega) {
					ext := &mcpv1.MCPGatewayExtension{}
					g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extName, Namespace: "default"}, ext)).To(Succeed())
					ext.SetReadyCondition(metav1.ConditionFalse, mcpv1.ConditionReasonSecretInvalid, "ca bundle secret invalid")
					g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
				}, testTimeout, testRetryInterval).Should(Succeed())
			})

			It("rejects a server with no per-server override until gateway guardrails are resolved", func() {
				mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "nemo_")
				Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

				configWriter := newMockMCPServerConfigReaderWriter()
				configWriter.unresolvedGuardrails["default"] = true
				reconciler := newMCPServerReconciler(configWriter)
				waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
				Expect(err).NotTo(HaveOccurred())
				waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

				Eventually(func(g Gomega) {
					result, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
					g.Expect(reconcileErr).NotTo(HaveOccurred())
					g.Expect(result.RequeueAfter).To(Equal(guardrailsUnresolvedRequeueTime))

					updated := &mcpv1.MCPServerRegistration{}
					g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
					cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
					g.Expect(cond).NotTo(BeNil())
					g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
					g.Expect(cond.Reason).To(Equal(conditionReasonGatewayGuardrailsNotConfigured))
					g.Expect(cond.Message).To(ContainSubstring("not yet resolved"))
				}, testTimeout, testRetryInterval).Should(Succeed())
				Expect(configWriter.upsertedServers).To(BeEmpty())

				// gateway config written once the extension recovers
				delete(configWriter.unresolvedGuardrails, "default")
				Eventually(func(g Gomega) {
					_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
					g.Expect(reconcileErr).NotTo(HaveOccurred())

					updated := &mcpv1.MCPServerRegistration{}
					g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
					cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
					g.Expect(cond).NotTo(BeNil())
					g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
				}, testTimeout, testRetryInterval).Should(Succeed())
				Expect(configWriter.upsertedServers).NotTo(BeEmpty())
			})
		})
		Context("and the gateway has no guardrails-ref annotation at all", func() {
			BeforeEach(func() {
				mcpExt := createTestMCPGatewayExtension(extName, "default", gatewayName, "default")
				Expect(testK8sClient.Create(ctx, mcpExt)).To(Succeed())

				Eventually(func(g Gomega) {
					ext := &mcpv1.MCPGatewayExtension{}
					g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extName, Namespace: "default"}, ext)).To(Succeed())
					ext.SetReadyCondition(metav1.ConditionTrue, mcpv1.ConditionReasonSuccess, "ready")
					g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
				}, testTimeout, testRetryInterval).Should(Succeed())
			})

			It("rejects a per-server guardrails-config-ids annotation with GatewayGuardrailsNotConfigured", func() {
				mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "nemo_")
				mcpsr.Annotations = map[string]string{ManagedGuardrailsAnnotation: "strict-input-checking"}
				Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

				configWriter := newMockMCPServerConfigReaderWriter()
				reconciler := newMCPServerReconciler(configWriter)
				waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
				Expect(err).NotTo(HaveOccurred())
				waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

				_, _ = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})

				Eventually(func(g Gomega) {
					updated := &mcpv1.MCPServerRegistration{}
					g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
					cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
					g.Expect(cond).NotTo(BeNil())
					g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
					g.Expect(cond.Reason).To(Equal(conditionReasonGatewayGuardrailsNotConfigured))
					g.Expect(cond.Message).To(ContainSubstring(labelGuardrailsReference))
				}, testTimeout, testRetryInterval).Should(Succeed())

				// fails closed - no config written for this server
				Expect(configWriter.upsertedServers).To(BeEmpty())
			})
		})

		Context("and the gateway's guardrails secret has gone missing (GuardrailsSecretNotFound)", func() {
			BeforeEach(func() {
				// no Secret created - mark the extension's dedicated
				// GuardrailsResolved condition unresolved, as if its
				// guardrails secret was deleted. requireGatewayGuardrails
				// keys off this condition, not Ready, so a server can fail
				// closed here without depending on why Ready itself is
				// false (which may be unrelated - see resolveGuardrails).
				mcpExt := createTestMCPGatewayExtension(extName, "default", gatewayName, "default")
				mcpExt.Annotations = map[string]string{labelGuardrailsReference: guardrailsSecretName}
				Expect(testK8sClient.Create(ctx, mcpExt)).To(Succeed())

				Eventually(func(g Gomega) {
					ext := &mcpv1.MCPGatewayExtension{}
					g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extName, Namespace: "default"}, ext)).To(Succeed())
					ext.SetGuardrailsResolvedCondition(metav1.ConditionFalse, mcpv1.GuardrailsSecretNotFound,
						fmt.Sprintf("guardrails secret %s not found", guardrailsSecretName))
					g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
				}, testTimeout, testRetryInterval).Should(Succeed())
			})

			It("fails closed for a server with no per-server override", assertFailsClosedForServerWithNoOverride)

			It("removes a server's config once the gateway's guardrails secret goes missing", func() {
				secret := createTestNeMoGuardrailsSecret(guardrailsSecretName, "default")
				Expect(testK8sClient.Create(ctx, secret)).To(Succeed())
				Eventually(func(g Gomega) {
					ext := &mcpv1.MCPGatewayExtension{}
					g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extName, Namespace: "default"}, ext)).To(Succeed())
					ext.SetReadyCondition(metav1.ConditionTrue, mcpv1.ConditionReasonSuccess, "ready")
					ext.SetGuardrailsResolvedCondition(metav1.ConditionTrue, mcpv1.ConditionReasonGuardrailsResolved,
						fmt.Sprintf("guardrails secret %s resolved", guardrailsSecretName))
					g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
				}, testTimeout, testRetryInterval).Should(Succeed())

				mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "nemo_")
				Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

				configWriter := newMockMCPServerConfigReaderWriter()
				reconciler := newMCPServerReconciler(configWriter)
				waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
				Expect(err).NotTo(HaveOccurred())
				waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

				Eventually(func(g Gomega) {
					_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
					g.Expect(reconcileErr).NotTo(HaveOccurred())
					g.Expect(configWriter.upsertedServers).NotTo(BeEmpty())
				}, testTimeout, testRetryInterval).Should(Succeed())

				// simulate the secret disappearing and re-reconcile
				Expect(testK8sClient.Delete(ctx, secret)).To(Succeed())
				Eventually(func(g Gomega) {
					ext := &mcpv1.MCPGatewayExtension{}
					g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extName, Namespace: "default"}, ext)).To(Succeed())
					ext.SetGuardrailsResolvedCondition(metav1.ConditionFalse, mcpv1.GuardrailsSecretNotFound,
						fmt.Sprintf("guardrails secret %s not found", guardrailsSecretName))
					g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
				}, testTimeout, testRetryInterval).Should(Succeed())

				Eventually(func(g Gomega) {
					_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
					g.Expect(reconcileErr).NotTo(HaveOccurred())
					g.Expect(configWriter.removedServers).To(ContainElement(fmt.Sprintf("default/%s", resourceName)))

					updated := &mcpv1.MCPServerRegistration{}
					g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
					cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
					g.Expect(cond).NotTo(BeNil())
					g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
					g.Expect(cond.Reason).To(Equal(conditionReasonGatewayGuardrailsNotConfigured))
				}, testTimeout, testRetryInterval).Should(Succeed())
			})
		})

		Context("and the gateway's guardrails secret is malformed (GuardrailsSecretInvalid)", func() {
			BeforeEach(func() {
				// mark the extension's guardrails unresolved, as if
				// resolveGuardrails found the secret but rejected it (missing
				// the managed label, or undecodable NeMo config data).
				mcpExt := createTestMCPGatewayExtension(extName, "default", gatewayName, "default")
				mcpExt.Annotations = map[string]string{labelGuardrailsReference: guardrailsSecretName}
				Expect(testK8sClient.Create(ctx, mcpExt)).To(Succeed())

				Eventually(func(g Gomega) {
					ext := &mcpv1.MCPGatewayExtension{}
					g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extName, Namespace: "default"}, ext)).To(Succeed())
					ext.SetGuardrailsResolvedCondition(metav1.ConditionFalse, mcpv1.GuardrailsSecretInvalid,
						fmt.Sprintf("guardrails secret %s missing required label", guardrailsSecretName))
					g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
				}, testTimeout, testRetryInterval).Should(Succeed())
			})

			It("fails closed for a server with no per-server override", assertFailsClosedForServerWithNoOverride)

			It("removes a server's config once the gateway's guardrails secret becomes invalid", func() {
				// bring a valid secret in first so the server reconciles fine.
				secret := createTestNeMoGuardrailsSecret(guardrailsSecretName, "default")
				Expect(testK8sClient.Create(ctx, secret)).To(Succeed())
				Eventually(func(g Gomega) {
					ext := &mcpv1.MCPGatewayExtension{}
					g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extName, Namespace: "default"}, ext)).To(Succeed())
					ext.SetReadyCondition(metav1.ConditionTrue, mcpv1.ConditionReasonSuccess, "ready")
					ext.SetGuardrailsResolvedCondition(metav1.ConditionTrue, mcpv1.ConditionReasonGuardrailsResolved,
						fmt.Sprintf("guardrails secret %s resolved", guardrailsSecretName))
					g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
				}, testTimeout, testRetryInterval).Should(Succeed())

				mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "nemo_")
				Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

				configWriter := newMockMCPServerConfigReaderWriter()
				reconciler := newMCPServerReconciler(configWriter)
				waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
				Expect(err).NotTo(HaveOccurred())
				waitForMCPServerRegistrationFinalizer(ctx, mcpsrNamespacedName)

				Eventually(func(g Gomega) {
					_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
					g.Expect(reconcileErr).NotTo(HaveOccurred())
					g.Expect(configWriter.upsertedServers).NotTo(BeEmpty())
				}, testTimeout, testRetryInterval).Should(Succeed())

				// simulate the secret becoming invalid (e.g. edited to drop
				// the managed label) and re-reconcile
				Eventually(func(g Gomega) {
					ext := &mcpv1.MCPGatewayExtension{}
					g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: extName, Namespace: "default"}, ext)).To(Succeed())
					ext.SetGuardrailsResolvedCondition(metav1.ConditionFalse, mcpv1.GuardrailsSecretInvalid,
						fmt.Sprintf("guardrails secret %s missing required label", guardrailsSecretName))
					g.Expect(testK8sClient.Status().Update(ctx, ext)).To(Succeed())
				}, testTimeout, testRetryInterval).Should(Succeed())

				Eventually(func(g Gomega) {
					_, reconcileErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpsrNamespacedName})
					g.Expect(reconcileErr).NotTo(HaveOccurred())
					g.Expect(configWriter.removedServers).To(ContainElement(fmt.Sprintf("default/%s", resourceName)))

					updated := &mcpv1.MCPServerRegistration{}
					g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
					cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
					g.Expect(cond).NotTo(BeNil())
					g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
					g.Expect(cond.Reason).To(Equal(conditionReasonGatewayGuardrailsNotConfigured))
				}, testTimeout, testRetryInterval).Should(Succeed())
			})
		})
	})

})
