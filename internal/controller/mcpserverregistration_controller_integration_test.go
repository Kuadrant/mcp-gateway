//go:build integration

package controller

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
	"github.com/Kuadrant/mcp-gateway/internal/config"
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
func createTestMCPServerRegistration(name, namespace, httpRouteName, prefix string) *mcpv1alpha1.MCPServerRegistration {
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
				ext := &mcpv1alpha1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: "test-ext-cacert", Namespace: "default"}, ext)).To(Succeed())
				ext.SetReadyCondition(metav1.ConditionTrue, mcpv1alpha1.ConditionReasonSuccess, "ready")
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
			mcpsr.Spec.CACertSecretRef = &mcpv1alpha1.CACertSecretReference{
				Name: secretName,
				Key:  "ca.crt",
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			for i := 0; i < 3; i++ {
				_, _ = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: mcpsrNamespacedName,
				})
				time.Sleep(100 * time.Millisecond)
			}

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
			mcpsr.Spec.CACertSecretRef = &mcpv1alpha1.CACertSecretReference{
				Name: "unlabeled-ca",
				Key:  "ca.crt",
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

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
				g.Expect(cond.Message).To(ContainSubstring("missing required label"))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should use default key ca.crt when key is not specified", func() {
			mcpsr := createTestMCPServerRegistration(resourceName, "default", httpRouteName, "test_")
			mcpsr.Spec.CACertSecretRef = &mcpv1alpha1.CACertSecretReference{
				Name: secretName,
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			for i := 0; i < 3; i++ {
				_, _ = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: mcpsrNamespacedName,
				})
				time.Sleep(100 * time.Millisecond)
			}

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
			mcpsr.Spec.CACertSecretRef = &mcpv1alpha1.CACertSecretReference{
				Name: "nonexistent-secret",
				Key:  "ca.crt",
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

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
			mcpsr.Spec.CACertSecretRef = &mcpv1alpha1.CACertSecretReference{
				Name: "wrong-key-ca",
				Key:  "ca.crt",
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

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
			mcpsr.Spec.CredentialRef = &mcpv1alpha1.SecretReference{
				Name: "test-cred-with-ca",
				Key:  "token",
			}
			mcpsr.Spec.CACertSecretRef = &mcpv1alpha1.CACertSecretReference{
				Name: secretName,
				Key:  "ca.crt",
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			for i := 0; i < 3; i++ {
				_, _ = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: mcpsrNamespacedName,
				})
				time.Sleep(100 * time.Millisecond)
			}

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
			mcpsr.Spec.CACertSecretRef = &mcpv1alpha1.CACertSecretReference{
				Name: "invalid-pem-ca",
				Key:  "ca.crt",
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			for i := 0; i < 3; i++ {
				_, _ = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: mcpsrNamespacedName,
				})
				time.Sleep(100 * time.Millisecond)
			}

			Eventually(func(g Gomega) {
				mcpsrObj := &mcpv1alpha1.MCPServerRegistration{}
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
			mcpsr.Spec.CACertSecretRef = &mcpv1alpha1.CACertSecretReference{
				Name: "oversized-ca",
				Key:  "ca.crt",
			}
			Expect(testK8sClient.Create(ctx, mcpsr)).To(Succeed())

			configWriter := newMockMCPServerConfigReaderWriter()
			reconciler := newMCPServerReconciler(configWriter)
			waitForMCPServerRegistrationCacheSync(ctx, mcpsrNamespacedName)

			for i := 0; i < 3; i++ {
				_, _ = reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: mcpsrNamespacedName,
				})
				time.Sleep(100 * time.Millisecond)
			}

			Eventually(func(g Gomega) {
				mcpsrObj := &mcpv1alpha1.MCPServerRegistration{}
				g.Expect(testK8sClient.Get(ctx, mcpsrNamespacedName, mcpsrObj)).To(Succeed())
				readyCond := meta.FindStatusCondition(mcpsrObj.Status.Conditions, "Ready")
				g.Expect(readyCond).NotTo(BeNil())
				g.Expect(readyCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(readyCond.Message).To(ContainSubstring("exceeds maximum size"))
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
})
