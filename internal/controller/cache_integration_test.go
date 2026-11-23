//go:build integration

package controller

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networking "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = ginkgo.DescribeTable("Controller cache membership", func(ctx ginkgo.SpecContext, object client.Object, labelKey, labelValue string) {
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "cache-membership-"}}
	gomega.Expect(testK8sClient.Create(ctx, namespace)).To(gomega.Succeed())
	ginkgo.DeferCleanup(testK8sClient.Delete, namespace)

	unlabeled := object.DeepCopyObject().(client.Object)
	unlabeled.SetNamespace(namespace.Name)
	unlabeled.SetName("unlabeled")
	gomega.Expect(testK8sClient.Create(ctx, unlabeled)).To(gomega.Succeed())
	ginkgo.DeferCleanup(testK8sClient.Delete, unlabeled)

	labeled := object.DeepCopyObject().(client.Object)
	labeled.SetNamespace(namespace.Name)
	labeled.SetName("labeled")
	labeled.SetLabels(map[string]string{labelKey: labelValue})
	gomega.Expect(testK8sClient.Create(ctx, labeled)).To(gomega.Succeed())
	ginkgo.DeferCleanup(testK8sClient.Delete, labeled)

	c := testMgr.GetCache()
	// observing the later object confirms the cache has caught up for this kind
	gomega.Eventually(ctx, func() error {
		return c.Get(ctx, client.ObjectKeyFromObject(labeled), labeled)
	}, testTimeout, testRetryInterval).Should(gomega.Succeed())

	gomega.Expect(c.Get(ctx, client.ObjectKeyFromObject(unlabeled), unlabeled)).To(gomega.MatchError(errors.IsNotFound, "IsNotFound"))
},
	ginkgo.Entry("only caches labeled Secrets", &corev1.Secret{}, ManagedSecretLabel, ManagedSecretValue, ginkgo.SpecTimeout(testTimeout)),
	ginkgo.Entry("only caches labeled Deployments", &appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "cache-membership"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "cache-membership"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "test", Image: "test-image"}}},
			},
		},
	}, labelManagedBy, labelManagedByValue, ginkgo.SpecTimeout(testTimeout)),
	ginkgo.Entry("only caches labeled Services", &corev1.Service{
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}, labelManagedBy, labelManagedByValue, ginkgo.SpecTimeout(testTimeout)),
	ginkgo.Entry("only caches labeled ServiceAccounts", &corev1.ServiceAccount{}, labelManagedBy, labelManagedByValue, ginkgo.SpecTimeout(testTimeout)),
	ginkgo.Entry("only caches labeled NetworkPolicies", &networking.NetworkPolicy{}, labelManagedBy, labelManagedByValue, ginkgo.SpecTimeout(testTimeout)),
)
