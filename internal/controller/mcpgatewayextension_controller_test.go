//go:build integration

package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	istionetv1alpha3 "istio.io/client-go/pkg/apis/networking/v1alpha3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
)

const (
	testTimeout       = 10 * time.Second
	testRetryInterval = 100 * time.Millisecond
)

// createTestNamespace creates a namespace for testing
func createTestNamespace(ctx context.Context, name string) {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
	Expect(client.IgnoreAlreadyExists(testK8sClient.Create(ctx, ns))).To(Succeed())
}

// createTestGateway creates a Gateway for testing
func createTestGateway(name, namespace string) *gatewayv1.Gateway {
	gateway := &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: "test-class",
			Listeners: []gatewayv1.Listener{
				{
					Name:     "http",
					Port:     80,
					Protocol: gatewayv1.HTTPProtocolType,
				},
			},
		},
	}
	return gateway
}

// createTestReferenceGrant creates a ReferenceGrant allowing MCPGatewayExtension to reference Gateways
func createTestReferenceGrant(name, namespace, fromNamespace string, gatewayName *string) *gatewayv1beta1.ReferenceGrant {
	var nameRef *gatewayv1beta1.ObjectName
	if gatewayName != nil {
		// name is optional and this will result in an empty string if not set
		ref := gatewayv1beta1.ObjectName(*gatewayName)
		nameRef = &ref
	}

	refGrant := &gatewayv1beta1.ReferenceGrant{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gatewayv1beta1.ReferenceGrantSpec{
			From: []gatewayv1beta1.ReferenceGrantFrom{
				{
					Group:     gatewayv1beta1.Group(mcpv1alpha1.GroupVersion.Group),
					Kind:      "MCPGatewayExtension",
					Namespace: gatewayv1beta1.Namespace(fromNamespace),
				},
			},
			To: []gatewayv1beta1.ReferenceGrantTo{
				{
					Group: gatewayv1beta1.Group(gatewayv1.GroupVersion.Group),
					Kind:  "Gateway",
					Name:  nameRef,
				},
			},
		},
	}
	return refGrant
}

// createTestMCPGatewayExtension creates an MCPGatewayExtension targeting a Gateway
func createTestMCPGatewayExtension(name, namespace, gatewayName, gatewayNamespace string) *mcpv1alpha1.MCPGatewayExtension {
	resource := &mcpv1alpha1.MCPGatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPGatewayExtensionSpec{
			TargetRef: mcpv1alpha1.MCPGatewayExtensionTargetReference{
				Group:     "gateway.networking.k8s.io",
				Kind:      "Gateway",
				Name:      gatewayName,
				Namespace: gatewayNamespace,
			},
		},
	}
	return resource
}

// deleteTestGateway deletes a Gateway if it exists
func deleteTestGateway(ctx context.Context, name, namespace string) {
	gateway := &gatewayv1.Gateway{}
	err := testK8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, gateway)
	if err == nil {
		_ = testK8sClient.Delete(ctx, gateway)
	}
}

// forceDeleteTestMCPGatewayExtension removes the finalizer and deletes the MCPGatewayExtension without going through the reconciler
func forceDeleteTestMCPGatewayExtension(ctx context.Context, name, namespace string) {
	nn := types.NamespacedName{Name: name, Namespace: namespace}
	resource := &mcpv1alpha1.MCPGatewayExtension{}
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

// deleteTestReferenceGrant deletes a ReferenceGrant if it exists
func deleteTestReferenceGrant(ctx context.Context, name, namespace string) error {
	refGrant := &gatewayv1beta1.ReferenceGrant{}
	err := testK8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, refGrant)
	if err == nil {
		return testK8sClient.Delete(ctx, refGrant)
	}
	return err
}

// mockConfigWriterDeleter is a mock implementation of ConfigWriterDeleter for testing
type mockConfigWriterDeleter struct{}

func (m *mockConfigWriterDeleter) DeleteConfig(ctx context.Context, namespaceName types.NamespacedName) error {
	return nil
}

func (m *mockConfigWriterDeleter) EnsureConfigExists(ctx context.Context, namespaceName types.NamespacedName) error {
	return nil
}

func (m *mockConfigWriterDeleter) WriteEmptyConfig(ctx context.Context, namespaceName types.NamespacedName) error {
	return nil
}

// newTestReconciler creates a new MCPGatewayExtensionReconciler for testing
func newTestReconciler() *MCPGatewayExtensionReconciler {
	return &MCPGatewayExtensionReconciler{
		Client:              testIndexedClient,
		Scheme:              testK8sClient.Scheme(),
		ConfigWriterDeleter: &mockConfigWriterDeleter{},
		BrokerRouterImage:   DefaultBrokerRouterImage,
		MCPExtFinderValidator: &MCPGatewayExtensionValidator{
			Client: testIndexedClient,
		},
		log: slog.New(slog.NewTextHandler(GinkgoWriter, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}
}

// waitForCacheSync waits for the cache to see an MCPGatewayExtension
func waitForCacheSync(ctx context.Context, nn types.NamespacedName) {
	Eventually(func(g Gomega) {
		cached := &mcpv1alpha1.MCPGatewayExtension{}
		g.Expect(testIndexedClient.Get(ctx, nn, cached)).To(Succeed())
	}, testTimeout, testRetryInterval).Should(Succeed())
}

// setDeploymentStatus updates the broker-router deployment status to simulate readiness in envtest
func setDeploymentStatus(ctx context.Context, namespace string, replicas, readyReplicas int32) {
	deployment := &appsv1.Deployment{}
	deploymentNN := types.NamespacedName{Name: brokerRouterName, Namespace: namespace}
	Eventually(func(g Gomega) {
		g.Expect(testK8sClient.Get(ctx, deploymentNN, deployment)).To(Succeed())
	}, testTimeout, testRetryInterval).Should(Succeed())

	deployment.Status.Replicas = replicas
	deployment.Status.ReadyReplicas = readyReplicas
	Expect(testK8sClient.Status().Update(ctx, deployment)).To(Succeed())
}

var _ = Describe("MCPGatewayExtension Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"
		const gatewayName = "test-gateway"

		ctx := context.Background()

		mcpExtNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			gw := createTestGateway(gatewayName, "default")
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())
			ext := createTestMCPGatewayExtension(resourceName, "default", gatewayName, "default")
			Expect(testK8sClient.Create(ctx, ext)).To(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPGatewayExtension(ctx, resourceName, "default")
			deleteTestGateway(ctx, gatewayName, "default")
		})

		It("should successfully reconcile the resource", func() {
			reconciler := newTestReconciler()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpExtNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("When managing finalizers", func() {
		const resourceName = "test-finalizer-resource"
		const gatewayName = "test-finalizer-gateway"

		ctx := context.Background()

		mcpExtNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			gw := createTestGateway(gatewayName, "default")
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())
			ext := createTestMCPGatewayExtension(resourceName, "default", gatewayName, "default")
			Expect(testK8sClient.Create(ctx, ext)).To(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPGatewayExtension(ctx, resourceName, "default")
			deleteTestGateway(ctx, gatewayName, "default")
		})

		It("should add finalizer on first reconcile", func() {
			reconciler := newTestReconciler()
			waitForCacheSync(ctx, mcpExtNamespacedName)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpExtNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				updated := &mcpv1alpha1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, mcpExtNamespacedName, updated)).To(Succeed())
				g.Expect(controllerutil.ContainsFinalizer(updated, mcpGatewayFinalizer)).To(BeTrue())
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should remove finalizer on deletion", func() {
			reconciler := newTestReconciler()
			waitForCacheSync(ctx, mcpExtNamespacedName)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpExtNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// trigger deletion
			resource := &mcpv1alpha1.MCPGatewayExtension{}
			Expect(testK8sClient.Get(ctx, mcpExtNamespacedName, resource)).To(Succeed())
			Expect(testK8sClient.Delete(ctx, resource)).To(Succeed())

			// wait for cache to see deletion timestamp
			Eventually(func(g Gomega) {
				cached := &mcpv1alpha1.MCPGatewayExtension{}
				err := testIndexedClient.Get(ctx, mcpExtNamespacedName, cached)
				if errors.IsNotFound(err) {
					return
				}
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(cached.DeletionTimestamp).NotTo(BeNil())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// reconcile to remove finalizer
			_, err = reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpExtNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				deleted := &mcpv1alpha1.MCPGatewayExtension{}
				err := testK8sClient.Get(ctx, mcpExtNamespacedName, deleted)
				g.Expect(errors.IsNotFound(err)).To(BeTrue())
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})

	Context("When multiple MCPGatewayExtensions target the same Gateway", func() {
		const resourceName1 = "test-conflict-resource-1"
		const resourceName2 = "test-conflict-resource-2"
		const gatewayName = "test-conflict-gateway"

		ctx := context.Background()

		mcpExtNamespacedName1 := types.NamespacedName{
			Name:      resourceName1,
			Namespace: "default",
		}
		mcpExtNamespacedName2 := types.NamespacedName{
			Name:      resourceName2,
			Namespace: "default",
		}

		BeforeEach(func() {
			gw := createTestGateway(gatewayName, "default")
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPGatewayExtension(ctx, resourceName1, "default")
			forceDeleteTestMCPGatewayExtension(ctx, resourceName2, "default")
			deleteTestGateway(ctx, gatewayName, "default")
		})

		It("should mark the second MCPGatewayExtension as not ready due to conflict", func() {
			ext1 := createTestMCPGatewayExtension(resourceName1, "default", gatewayName, "default")
			Expect(testK8sClient.Create(ctx, ext1)).To(Succeed())

			reconciler := newTestReconciler()
			waitForCacheSync(ctx, mcpExtNamespacedName1)

			// reconcile until status is set (handles finalizer add + cache sync)
			// in envtest, deployments don't become ready so we expect DeploymentNotReady
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: mcpExtNamespacedName1,
				})
				g.Expect(err).NotTo(HaveOccurred())

				updated1 := &mcpv1alpha1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, mcpExtNamespacedName1, updated1)).To(Succeed())
				condition := meta.FindStatusCondition(updated1.Status.Conditions, mcpv1alpha1.ConditionTypeReady)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonDeploymentNotReady))
			}, testTimeout, testRetryInterval).Should(Succeed())

			// ensure distinct CreationTimestamp for second extension
			time.Sleep(1100 * time.Millisecond)

			ext2 := createTestMCPGatewayExtension(resourceName2, "default", gatewayName, "default")
			Expect(testK8sClient.Create(ctx, ext2)).To(Succeed())

			// wait for cache to sync and see both extensions via field index
			Eventually(func(g Gomega) {
				cached := &mcpv1alpha1.MCPGatewayExtension{}
				g.Expect(testIndexedClient.Get(ctx, mcpExtNamespacedName2, cached)).To(Succeed())
				extList := &mcpv1alpha1.MCPGatewayExtensionList{}
				g.Expect(testIndexedClient.List(ctx, extList,
					client.MatchingFields{gatewayIndexKey: fmt.Sprintf("%s/%s", "default", gatewayName)},
				)).To(Succeed())
				g.Expect(len(extList.Items)).To(Equal(2), "both extensions should be indexed")
			}, testTimeout, testRetryInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: mcpExtNamespacedName2,
				})
				g.Expect(err).NotTo(HaveOccurred())

				updated2 := &mcpv1alpha1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, mcpExtNamespacedName2, updated2)).To(Succeed())
				condition := meta.FindStatusCondition(updated2.Status.Conditions, mcpv1alpha1.ConditionTypeReady)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonInvalid))
				g.Expect(condition.Message).To(ContainSubstring("conflict"))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})

	Context("When checking ReferenceGrant for cross-namespace references", func() {
		const resourceName = "test-cross-ns-resource"
		const gatewayName = "test-cross-ns-gateway"
		const gatewayNamespace = "gateway-system"

		ctx := context.Background()

		mcpExtNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			createTestNamespace(ctx, gatewayNamespace)
			gw := createTestGateway(gatewayName, gatewayNamespace)
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())
			ext := createTestMCPGatewayExtension(resourceName, "default", gatewayName, gatewayNamespace)
			Expect(testK8sClient.Create(ctx, ext)).To(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPGatewayExtension(ctx, resourceName, "default")
			deleteTestGateway(ctx, gatewayName, gatewayNamespace)
		})

		It("should set RefGrantRequired status when no ReferenceGrant exists", func() {
			reconciler := newTestReconciler()
			waitForCacheSync(ctx, mcpExtNamespacedName)

			// reconcile until status is set (handles finalizer add + cache sync)
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: mcpExtNamespacedName,
				})
				g.Expect(err).NotTo(HaveOccurred())

				updated := &mcpv1alpha1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, mcpExtNamespacedName, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionTypeReady)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonRefGrantRequired))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})

	Context("When a valid ReferenceGrant exists for cross-namespace reference", func() {
		const resourceName = "test-refgrant-valid-resource"
		const gatewayName = "test-refgrant-valid-gateway"
		const gatewayNamespace = "refgrant-ns"
		const refGrantName = "allow-mcp-extension"

		ctx := context.Background()

		mcpExtNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			createTestNamespace(ctx, gatewayNamespace)
			gw := createTestGateway(gatewayName, gatewayNamespace)
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPGatewayExtension(ctx, resourceName, "default")
			Expect(deleteTestReferenceGrant(ctx, refGrantName, gatewayNamespace)).To(Succeed())
			deleteTestGateway(ctx, gatewayName, gatewayNamespace)
		})

		Context("with wildcard ReferenceGrant", func() {
			BeforeEach(func() {
				refGrant := createTestReferenceGrant(refGrantName, gatewayNamespace, "default", nil)
				Expect(testK8sClient.Create(ctx, refGrant)).To(Succeed())
				ext := createTestMCPGatewayExtension(resourceName, "default", gatewayName, gatewayNamespace)
				Expect(testK8sClient.Create(ctx, ext)).To(Succeed())
			})

			It("should become Ready when ReferenceGrant allows cross-namespace reference", func() {
				reconciler := newTestReconciler()
				waitForCacheSync(ctx, mcpExtNamespacedName)

				// reconcile until deployment is created
				Eventually(func(g Gomega) {
					_, err := reconciler.Reconcile(ctx, reconcile.Request{
						NamespacedName: mcpExtNamespacedName,
					})
					g.Expect(err).NotTo(HaveOccurred())

					deployment := &appsv1.Deployment{}
					g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: brokerRouterName, Namespace: "default"}, deployment)).To(Succeed())
				}, testTimeout, testRetryInterval).Should(Succeed())

				// simulate deployment readiness
				var replicas, readyReplicas int32 = 1, 1
				setDeploymentStatus(ctx, "default", replicas, readyReplicas)

				// reconcile again to pick up deployment readiness
				Eventually(func(g Gomega) {
					_, err := reconciler.Reconcile(ctx, reconcile.Request{
						NamespacedName: mcpExtNamespacedName,
					})
					g.Expect(err).NotTo(HaveOccurred())

					updated := &mcpv1alpha1.MCPGatewayExtension{}
					g.Expect(testK8sClient.Get(ctx, mcpExtNamespacedName, updated)).To(Succeed())
					condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionTypeReady)
					g.Expect(condition).NotTo(BeNil())
					g.Expect(condition.Status).To(Equal(metav1.ConditionTrue))
					g.Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonSuccess))
				}, testTimeout, testRetryInterval).Should(Succeed())
			})
		})

		Context("with specific Gateway name in ReferenceGrant", func() {
			BeforeEach(func() {
				gwName := gatewayName
				refGrant := createTestReferenceGrant(refGrantName, gatewayNamespace, "default", &gwName)
				Expect(testK8sClient.Create(ctx, refGrant)).To(Succeed())
				ext := createTestMCPGatewayExtension(resourceName, "default", gatewayName, gatewayNamespace)
				Expect(testK8sClient.Create(ctx, ext)).To(Succeed())
			})

			It("should become Ready when ReferenceGrant specifies a specific Gateway name", func() {
				reconciler := newTestReconciler()
				waitForCacheSync(ctx, mcpExtNamespacedName)

				// reconcile until deployment is created
				Eventually(func(g Gomega) {
					_, err := reconciler.Reconcile(ctx, reconcile.Request{
						NamespacedName: mcpExtNamespacedName,
					})
					g.Expect(err).NotTo(HaveOccurred())

					deployment := &appsv1.Deployment{}
					g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: brokerRouterName, Namespace: "default"}, deployment)).To(Succeed())
				}, testTimeout, testRetryInterval).Should(Succeed())

				// simulate deployment readiness
				var replicas, readyReplicas int32 = 1, 1
				setDeploymentStatus(ctx, "default", replicas, readyReplicas)

				// reconcile again to pick up deployment readiness
				Eventually(func(g Gomega) {
					_, err := reconciler.Reconcile(ctx, reconcile.Request{
						NamespacedName: mcpExtNamespacedName,
					})
					g.Expect(err).NotTo(HaveOccurred())

					updated := &mcpv1alpha1.MCPGatewayExtension{}
					g.Expect(testK8sClient.Get(ctx, mcpExtNamespacedName, updated)).To(Succeed())
					condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionTypeReady)
					g.Expect(condition).NotTo(BeNil())
					g.Expect(condition.Status).To(Equal(metav1.ConditionTrue))
					g.Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonSuccess))
				}, testTimeout, testRetryInterval).Should(Succeed())
			})
		})
	})

	Context("When target Gateway does not exist", func() {
		const resourceName = "test-no-gateway-resource"
		const gatewayName = "nonexistent-gateway"

		ctx := context.Background()

		mcpExtNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			ext := createTestMCPGatewayExtension(resourceName, "default", gatewayName, "default")
			Expect(testK8sClient.Create(ctx, ext)).To(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPGatewayExtension(ctx, resourceName, "default")
		})

		It("should mark MCPGatewayExtension as invalid when Gateway does not exist", func() {
			reconciler := newTestReconciler()
			waitForCacheSync(ctx, mcpExtNamespacedName)

			// reconcile until status is set (handles finalizer add + cache sync)
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: mcpExtNamespacedName,
				})
				g.Expect(err).NotTo(HaveOccurred())

				updated := &mcpv1alpha1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, mcpExtNamespacedName, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionTypeReady)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonInvalid))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})

	Context("When the target Gateway is deleted", func() {
		const resourceName = "test-gateway-deleted-resource"
		const gatewayName = "test-gateway-deleted-gateway"

		ctx := context.Background()

		mcpExtNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		var gateway *gatewayv1.Gateway

		BeforeEach(func() {
			gateway = createTestGateway(gatewayName, "default")
			Expect(testK8sClient.Create(ctx, gateway)).To(Succeed())
			ext := createTestMCPGatewayExtension(resourceName, "default", gatewayName, "default")
			Expect(testK8sClient.Create(ctx, ext)).To(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPGatewayExtension(ctx, resourceName, "default")
			deleteTestGateway(ctx, gatewayName, "default")
		})

		It("should mark MCPGatewayExtension as invalid when Gateway is deleted", func() {
			reconciler := newTestReconciler()
			waitForCacheSync(ctx, mcpExtNamespacedName)

			// reconcile until deployment is created (handles finalizer add + cache sync)
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: mcpExtNamespacedName,
				})
				g.Expect(err).NotTo(HaveOccurred())

				deployment := &appsv1.Deployment{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{Name: brokerRouterName, Namespace: "default"}, deployment)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// simulate deployment readiness
			var replicas, readyReplicas int32 = 1, 1
			setDeploymentStatus(ctx, "default", replicas, readyReplicas)

			// reconcile again to pick up deployment readiness
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: mcpExtNamespacedName,
				})
				g.Expect(err).NotTo(HaveOccurred())
			}, testTimeout, testRetryInterval).Should(Succeed())

			Eventually(func(g Gomega) {
				updated := &mcpv1alpha1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, mcpExtNamespacedName, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionTypeReady)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionTrue))
				g.Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonSuccess))
			}, testTimeout, testRetryInterval).Should(Succeed())

			Expect(testK8sClient.Delete(ctx, gateway)).To(Succeed())

			gatewayNN := types.NamespacedName{Name: gatewayName, Namespace: "default"}
			Eventually(func(g Gomega) {
				deleted := &gatewayv1.Gateway{}
				err := testK8sClient.Get(ctx, gatewayNN, deleted)
				g.Expect(errors.IsNotFound(err)).To(BeTrue())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// use direct client for post-deletion reconcile (bypasses cache sync issues)
			directReconciler := &MCPGatewayExtensionReconciler{
				Client:              testK8sClient,
				Scheme:              testK8sClient.Scheme(),
				ConfigWriterDeleter: &mockConfigWriterDeleter{},
				BrokerRouterImage:   DefaultBrokerRouterImage,
				log:                 slog.New(slog.NewTextHandler(GinkgoWriter, &slog.HandlerOptions{Level: slog.LevelDebug})),
			}

			_, err := directReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: mcpExtNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			Eventually(func(g Gomega) {
				updated := &mcpv1alpha1.MCPGatewayExtension{}
				g.Expect(testK8sClient.Get(ctx, mcpExtNamespacedName, updated)).To(Succeed())
				condition := meta.FindStatusCondition(updated.Status.Conditions, mcpv1alpha1.ConditionTypeReady)
				g.Expect(condition).NotTo(BeNil())
				g.Expect(condition.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(condition.Reason).To(Equal(mcpv1alpha1.ConditionReasonInvalid))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})

	Context("When reconciling broker-router resources", func() {
		const resourceName = "test-broker-router-resource"
		const gatewayName = "test-broker-router-gateway"

		ctx := context.Background()

		mcpExtNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			gw := createTestGateway(gatewayName, "default")
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())
			ext := createTestMCPGatewayExtension(resourceName, "default", gatewayName, "default")
			Expect(testK8sClient.Create(ctx, ext)).To(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPGatewayExtension(ctx, resourceName, "default")
			deleteTestGateway(ctx, gatewayName, "default")
			// clean up deployment and service
			deployment := &appsv1.Deployment{}
			if err := testK8sClient.Get(ctx, types.NamespacedName{Name: brokerRouterName, Namespace: "default"}, deployment); err == nil {
				_ = testK8sClient.Delete(ctx, deployment)
			}
			service := &corev1.Service{}
			if err := testK8sClient.Get(ctx, types.NamespacedName{Name: brokerRouterName, Namespace: "default"}, service); err == nil {
				_ = testK8sClient.Delete(ctx, service)
			}
		})

		It("should create broker-router deployment and service", func() {
			reconciler := newTestReconciler()
			waitForCacheSync(ctx, mcpExtNamespacedName)

			// reconcile until deployment and service are created
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpExtNamespacedName})
				g.Expect(err).NotTo(HaveOccurred())
				// check if deployment and service exist
				deployment := &appsv1.Deployment{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{
					Name:      brokerRouterName,
					Namespace: "default",
				}, deployment)).To(Succeed())
				service := &corev1.Service{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{
					Name:      brokerRouterName,
					Namespace: "default",
				}, service)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// verify deployment details
			deployment := &appsv1.Deployment{}
			Expect(testK8sClient.Get(ctx, types.NamespacedName{
				Name:      brokerRouterName,
				Namespace: "default",
			}, deployment)).To(Succeed())
			Expect(deployment.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(deployment.Spec.Template.Spec.Containers[0].Image).To(Equal(DefaultBrokerRouterImage))

			// verify service details
			service := &corev1.Service{}
			Expect(testK8sClient.Get(ctx, types.NamespacedName{
				Name:      brokerRouterName,
				Namespace: "default",
			}, service)).To(Succeed())
			Expect(service.Spec.Ports).To(HaveLen(2))
		})

		It("should set owner reference on deployment and service", func() {
			reconciler := newTestReconciler()
			waitForCacheSync(ctx, mcpExtNamespacedName)

			// reconcile until deployment and service are created
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpExtNamespacedName})
				g.Expect(err).NotTo(HaveOccurred())
				// check if deployment and service exist
				deployment := &appsv1.Deployment{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{
					Name:      brokerRouterName,
					Namespace: "default",
				}, deployment)).To(Succeed())
				service := &corev1.Service{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{
					Name:      brokerRouterName,
					Namespace: "default",
				}, service)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// get the MCPGatewayExtension to check UID
			mcpExt := &mcpv1alpha1.MCPGatewayExtension{}
			Expect(testK8sClient.Get(ctx, mcpExtNamespacedName, mcpExt)).To(Succeed())

			// verify deployment owner reference
			deployment := &appsv1.Deployment{}
			Expect(testK8sClient.Get(ctx, types.NamespacedName{
				Name:      brokerRouterName,
				Namespace: "default",
			}, deployment)).To(Succeed())
			Expect(deployment.OwnerReferences).To(HaveLen(1))
			Expect(deployment.OwnerReferences[0].UID).To(Equal(mcpExt.UID))

			// verify service owner reference
			service := &corev1.Service{}
			Expect(testK8sClient.Get(ctx, types.NamespacedName{
				Name:      brokerRouterName,
				Namespace: "default",
			}, service)).To(Succeed())
			Expect(service.OwnerReferences).To(HaveLen(1))
			Expect(service.OwnerReferences[0].UID).To(Equal(mcpExt.UID))
		})
	})

	Context("When reconciling EnvoyFilter for cross-namespace Gateway", func() {
		const resourceName = "test-envoyfilter-resource"
		const gatewayName = "test-envoyfilter-gateway"
		const gatewayNamespace = "envoyfilter-gateway-ns"
		const refGrantName = "allow-mcp-extension-envoyfilter"

		ctx := context.Background()

		mcpExtNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			createTestNamespace(ctx, gatewayNamespace)
			gw := createTestGateway(gatewayName, gatewayNamespace)
			Expect(testK8sClient.Create(ctx, gw)).To(Succeed())
			refGrant := createTestReferenceGrant(refGrantName, gatewayNamespace, "default", nil)
			Expect(testK8sClient.Create(ctx, refGrant)).To(Succeed())
			ext := createTestMCPGatewayExtension(resourceName, "default", gatewayName, gatewayNamespace)
			Expect(testK8sClient.Create(ctx, ext)).To(Succeed())
		})

		AfterEach(func() {
			forceDeleteTestMCPGatewayExtension(ctx, resourceName, "default")
			_ = deleteTestReferenceGrant(ctx, refGrantName, gatewayNamespace)
			deleteTestGateway(ctx, gatewayName, gatewayNamespace)
			// clean up EnvoyFilter
			envoyFilterList := &istionetv1alpha3.EnvoyFilterList{}
			if err := testK8sClient.List(ctx, envoyFilterList, client.InNamespace(gatewayNamespace)); err == nil {
				for _, ef := range envoyFilterList.Items {
					_ = testK8sClient.Delete(ctx, ef)
				}
			}
			// clean up deployment and service
			deployment := &appsv1.Deployment{}
			if err := testK8sClient.Get(ctx, types.NamespacedName{Name: brokerRouterName, Namespace: "default"}, deployment); err == nil {
				_ = testK8sClient.Delete(ctx, deployment)
			}
			service := &corev1.Service{}
			if err := testK8sClient.Get(ctx, types.NamespacedName{Name: brokerRouterName, Namespace: "default"}, service); err == nil {
				_ = testK8sClient.Delete(ctx, service)
			}
		})

		It("should create EnvoyFilter in the Gateway namespace", func() {
			reconciler := newTestReconciler()
			waitForCacheSync(ctx, mcpExtNamespacedName)

			// reconcile until deployment is created
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpExtNamespacedName})
				g.Expect(err).NotTo(HaveOccurred())
				// check if deployment exists
				deployment := &appsv1.Deployment{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{
					Name:      brokerRouterName,
					Namespace: "default",
				}, deployment)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// simulate deployment readiness
			setDeploymentStatus(ctx, "default", 1, 1)

			// reconcile again to create EnvoyFilter
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpExtNamespacedName})
				g.Expect(err).NotTo(HaveOccurred())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// verify EnvoyFilter was created in gateway namespace
			expectedEnvoyFilterName := fmt.Sprintf("mcp-ext-proc-%s-gateway", "default")
			Eventually(func(g Gomega) {
				envoyFilter := &istionetv1alpha3.EnvoyFilter{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedEnvoyFilterName,
					Namespace: gatewayNamespace,
				}, envoyFilter)).To(Succeed())
				g.Expect(envoyFilter.Labels[labelManagedBy]).To(Equal(labelManagedByValue))
				g.Expect(envoyFilter.Labels["mcp.kagenti.com/extension-name"]).To(Equal(resourceName))
				g.Expect(envoyFilter.Labels["mcp.kagenti.com/extension-namespace"]).To(Equal("default"))
			}, testTimeout, testRetryInterval).Should(Succeed())
		})

		It("should delete EnvoyFilter when MCPGatewayExtension is deleted", func() {
			reconciler := newTestReconciler()
			waitForCacheSync(ctx, mcpExtNamespacedName)

			// reconcile until deployment is created
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpExtNamespacedName})
				g.Expect(err).NotTo(HaveOccurred())
				// check if deployment exists
				deployment := &appsv1.Deployment{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{
					Name:      brokerRouterName,
					Namespace: "default",
				}, deployment)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// simulate deployment readiness
			setDeploymentStatus(ctx, "default", 1, 1)

			// reconcile to create EnvoyFilter
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpExtNamespacedName})
				g.Expect(err).NotTo(HaveOccurred())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// verify EnvoyFilter exists
			expectedEnvoyFilterName := fmt.Sprintf("mcp-ext-proc-%s-gateway", "default")
			Eventually(func(g Gomega) {
				envoyFilter := &istionetv1alpha3.EnvoyFilter{}
				g.Expect(testK8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedEnvoyFilterName,
					Namespace: gatewayNamespace,
				}, envoyFilter)).To(Succeed())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// trigger deletion
			resource := &mcpv1alpha1.MCPGatewayExtension{}
			Expect(testK8sClient.Get(ctx, mcpExtNamespacedName, resource)).To(Succeed())
			Expect(testK8sClient.Delete(ctx, resource)).To(Succeed())

			// wait for cache to see deletion timestamp
			Eventually(func(g Gomega) {
				cached := &mcpv1alpha1.MCPGatewayExtension{}
				err := testIndexedClient.Get(ctx, mcpExtNamespacedName, cached)
				if errors.IsNotFound(err) {
					return
				}
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(cached.DeletionTimestamp).NotTo(BeNil())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// reconcile to handle deletion (retry in case of conflict)
			Eventually(func(g Gomega) {
				_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: mcpExtNamespacedName})
				g.Expect(err).NotTo(HaveOccurred())
			}, testTimeout, testRetryInterval).Should(Succeed())

			// verify EnvoyFilter was deleted
			Eventually(func(g Gomega) {
				envoyFilter := &istionetv1alpha3.EnvoyFilter{}
				err := testK8sClient.Get(ctx, types.NamespacedName{
					Name:      expectedEnvoyFilterName,
					Namespace: gatewayNamespace,
				}, envoyFilter)
				g.Expect(errors.IsNotFound(err)).To(BeTrue())
			}, testTimeout, testRetryInterval).Should(Succeed())
		})
	})
})
