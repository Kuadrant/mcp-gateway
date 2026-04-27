//go:build integration

package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
)

// mockMCPServerConfigReaderWriter is a mock for testing
type mockMCPServerConfigReaderWriter struct {
	upsertedServers map[string]config.MCPServer
	removedServers  []string
}

func newMockMCPServerConfigReaderWriter() *mockMCPServerConfigReaderWriter {
	return &mockMCPServerConfigReaderWriter{
		upsertedServers: make(map[string]config.MCPServer),
		removedServers:  []string{},
	}
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
func createTestMCPServerRegistration(name, namespace, httpRouteName, toolPrefix string) *mcpv1alpha1.MCPServerRegistration {
	return &mcpv1alpha1.MCPServerRegistration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPServerRegistrationSpec{
			TargetRef: mcpv1alpha1.TargetReference{
				Group: "gateway.networking.k8s.io",
				Kind:  "HTTPRoute",
				Name:  httpRouteName,
			},
			ToolPrefix: toolPrefix,
			Path:       "/mcp",
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

// forceDeleteTestMCPServerRegistration removes finalizers and deletes
func forceDeleteTestMCPServerRegistration(ctx context.Context, name, namespace string) {
	nn := types.NamespacedName{Name: name, Namespace: namespace}
	resource := &mcpv1alpha1.MCPServerRegistration{}
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
		cached := &mcpv1alpha1.MCPServerRegistration{}
		g.Expect(testIndexedClient.Get(ctx, nn, cached)).To(Succeed())
	}, testTimeout, testRetryInterval).Should(Succeed())
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
				ext := &mcpv1alpha1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: "test-ext", Namespace: "default"}, ext)).To(Succeed())
				ext.SetReadyCondition(metav1.ConditionTrue, mcpv1alpha1.ConditionReasonSuccess, "ready")
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
				updated := &mcpv1alpha1.MCPServerRegistration{}
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
			resource := &mcpv1alpha1.MCPServerRegistration{}
			Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, resource)).To(Succeed())
			Expect(testK8sClient.Delete(ctx, resource)).To(Succeed())

			// wait for cache to see deletion timestamp
			Eventually(func(g Gomega) {
				cached := &mcpv1alpha1.MCPServerRegistration{}
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
				deleted := &mcpv1alpha1.MCPServerRegistration{}
				err := testK8sClient.Get(ctx, mcpsrNamespacedName, deleted)
				g.Expect(errors.IsNotFound(err)).To(BeTrue())
			}, testTimeout, testRetryInterval).Should(Succeed())
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

			// reconcile multiple times to get past finalizer addition
			for i := 0; i < 3; i++ {
				_, _ = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: mcpsrNamespacedName,
				})
				time.Sleep(100 * time.Millisecond)
			}

			Eventually(func(g Gomega) {
				updated := &mcpv1alpha1.MCPServerRegistration{}
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

			// reconcile multiple times to get past finalizer addition
			for i := 0; i < 3; i++ {
				_, _ = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: mcpsrNamespacedName,
				})
				time.Sleep(100 * time.Millisecond)
			}

			Eventually(func(g Gomega) {
				updated := &mcpv1alpha1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
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

			// reconcile multiple times to get past finalizer addition
			for i := 0; i < 3; i++ {
				_, _ = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: mcpsrNamespacedName,
				})
				time.Sleep(100 * time.Millisecond)
			}

			Eventually(func(g Gomega) {
				updated := &mcpv1alpha1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, updated)).To(Succeed())
				cond := meta.FindStatusCondition(updated.Status.Conditions, "Ready")
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(cond.Message).To(ContainSubstring("no valid gateways"))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})
})
