//go:build integration

package controller

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
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
)
