//go:build e2e

package e2e

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1 "github.com/Kuadrant/mcp-gateway/api/v1"
)

const (
	gatewayCACertSecretName = "e2e-gateway-ca-cert"
	gatewayCACertVolumeName = "gateway-ca-cert-volume"
	gatewayCACertMountPath  = "/gateway-ca-cert"
)

// patchExtension patches the shared MCPGatewayExtension in-place
// to set or clear gatewayCACertSecretRef and privateHost. This avoids
// recreating the extension and losing other deployment patches applied by the suite.
func patchExtension(ref *mcpv1.CACertSecretReference, privateHost string) {
	ext := &mcpv1.MCPGatewayExtension{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{
		Name: MCPExtensionName, Namespace: SystemNamespace,
	}, ext)).To(Succeed())

	specPatch := map[string]interface{}{
		"gatewayCACertSecretRef": ref,
	}
	if privateHost != "" {
		specPatch["privateHost"] = privateHost
	} else {
		specPatch["privateHost"] = nil
	}

	patch := map[string]interface{}{
		"spec": specPatch,
	}
	patchBytes, err := json.Marshal(patch)
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient.Patch(ctx, ext, client.RawPatch(types.MergePatchType, patchBytes))).To(Succeed())
}

// getBrokerRouterDeployment fetches the shared broker-router deployment.
func getBrokerRouterDeployment(g Gomega) *appsv1.Deployment {
	deployment := &appsv1.Deployment{}
	g.Expect(k8sClient.Get(ctx, types.NamespacedName{
		Name: GatewayName, Namespace: SystemNamespace,
	}, deployment)).To(Succeed())
	return deployment
}

var _ = Describe("Gateway CA Cert", Ordered, Serial, func() {
	var (
		testResources []client.Object
		correctCAPEM  []byte
	)

	BeforeAll(func() {
		By("Extracting correct CA cert from cert-manager")
		caSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{
			Name: caKeypairSecret, Namespace: certManagerNS,
		}, caSecret)).To(Succeed())
		var ok bool
		correctCAPEM, ok = caSecret.Data["ca.crt"]
		Expect(ok).To(BeTrue(), "private-ca-keypair should have ca.crt")
	})

	BeforeEach(func() {
		testResources = []client.Object{}
	})

	AfterEach(func() {
		for _, obj := range testResources {
			CleanupResource(ctx, k8sClient, obj)
		}
		testResources = []client.Object{}

		By("Removing gatewayCACertSecretRef and privateHost from MCPGatewayExtension")
		patchExtension(nil, "")

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPGatewayExtensionReady(ctx, k8sClient, MCPExtensionName, SystemNamespace)).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Verifying the managed gateway CA cert volume/mount are removed from the broker deployment")
		Eventually(func(g Gomega) {
			deployment := getBrokerRouterDeployment(g)
			g.Expect(hasVolume(deployment, gatewayCACertVolumeName)).To(BeFalse())
			g.Expect(hasVolumeMount(deployment, gatewayCACertVolumeName)).To(BeFalse())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	})

	createLabeledCASecret := func(name, key string, pemData []byte) *corev1.Secret {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: SystemNamespace,
				Labels: map[string]string{
					"mcp.kuadrant.io/secret": "true",
					"e2e":                    "test",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{key: pemData},
		}
		_ = k8sClient.Delete(ctx, secret)
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		return secret
	}

	setupRef := func(ref *mcpv1.CACertSecretReference, privateHost string) {
		By("Patching MCPGatewayExtension with gatewayCACertSecretRef and privateHost")
		patchExtension(ref, privateHost)

		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPGatewayExtensionReady(ctx, k8sClient, MCPExtensionName, SystemNamespace)).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	}

	It("[Happy,GatewayCACert] Gateway CA cert secret is wired into the broker deployment along with HTTPS privateHost", func() {
		By("Creating gateway CA cert secret with default key ca.crt")
		caSecret := createLabeledCASecret(gatewayCACertSecretName, "ca.crt", correctCAPEM)
		testResources = append(testResources, caSecret)

		httpsHost := "https://mcp-gateway-istio.gateway-system.svc.cluster.local:443"
		setupRef(&mcpv1.CACertSecretReference{Name: gatewayCACertSecretName, Key: "ca.crt"}, httpsHost)

		By("Verifying the broker deployment mounts the gateway CA cert and sets --gateway-ca-cert and --mcp-gateway-private-host")
		Eventually(func(g Gomega) {
			deployment := getBrokerRouterDeployment(g)

			g.Expect(commandContains(deployment, "--gateway-ca-cert="+gatewayCACertMountPath+"/ca.crt")).
				To(BeTrue(), "expected --gateway-ca-cert flag in broker command")
			g.Expect(commandContains(deployment, "--mcp-gateway-private-host="+httpsHost)).
				To(BeTrue(), "expected --mcp-gateway-private-host flag in broker command to match https privateHost")

			vol, ok := findVolume(deployment, gatewayCACertVolumeName)
			g.Expect(ok).To(BeTrue(), "expected gateway-ca-cert-volume on deployment")
			g.Expect(vol.Secret).NotTo(BeNil())
			g.Expect(vol.Secret.SecretName).To(Equal(gatewayCACertSecretName))

			mount, ok := findVolumeMount(deployment, gatewayCACertVolumeName)
			g.Expect(ok).To(BeTrue(), "expected gateway-ca-cert-volume mount on broker container")
			g.Expect(mount.MountPath).To(Equal(gatewayCACertMountPath))
			g.Expect(mount.ReadOnly).To(BeTrue())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	})

	It("[Full,GatewayCACert] Custom secret key is honored in the --gateway-ca-cert path", func() {
		const customKey = "tls-ca.crt"

		By("Creating gateway CA cert secret with a custom key")
		caSecret := createLabeledCASecret(gatewayCACertSecretName, customKey, correctCAPEM)
		testResources = append(testResources, caSecret)

		setupRef(&mcpv1.CACertSecretReference{Name: gatewayCACertSecretName, Key: customKey}, "")

		By("Verifying the --gateway-ca-cert path uses the custom key")
		Eventually(func(g Gomega) {
			deployment := getBrokerRouterDeployment(g)
			g.Expect(commandContains(deployment, "--gateway-ca-cert="+gatewayCACertMountPath+"/"+customKey)).
				To(BeTrue(), "expected --gateway-ca-cert flag with custom key in broker command")
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	})

	It("[Full,GatewayCACert] Invalid gateway CA cert secret — MCPGatewayExtension reports error", func() {
		By("Patching MCPGatewayExtension to reference a non-existent secret")
		patchExtension(&mcpv1.CACertSecretReference{Name: "nonexistent-gateway-ca", Key: "ca.crt"}, "")

		By("Verifying MCPGatewayExtension reports SecretNotFound")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPGatewayExtensionNotReadyWithReason(ctx, k8sClient,
				MCPExtensionName, SystemNamespace, string(mcpv1.ConditionReasonSecretNotFound))).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Creating a CA cert secret WITHOUT the required label")
		unlabeled := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "gwca-no-label",
				Namespace: SystemNamespace,
				Labels:    map[string]string{"e2e": "test"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"ca.crt": correctCAPEM},
		}
		_ = k8sClient.Delete(ctx, unlabeled)
		Expect(k8sClient.Create(ctx, unlabeled)).To(Succeed())
		testResources = append(testResources, unlabeled)

		By("Patching extension to reference the unlabeled secret")
		patchExtension(&mcpv1.CACertSecretReference{Name: "gwca-no-label", Key: "ca.crt"}, "")

		By("Verifying MCPGatewayExtension reports SecretInvalid for the missing label")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPGatewayExtensionNotReadyWithReason(ctx, k8sClient,
				MCPExtensionName, SystemNamespace, string(mcpv1.ConditionReasonSecretInvalid))).To(Succeed())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())
	})
})

func commandContains(deployment *appsv1.Deployment, arg string) bool {
	if len(deployment.Spec.Template.Spec.Containers) == 0 {
		return false
	}
	for _, a := range deployment.Spec.Template.Spec.Containers[0].Command {
		if a == arg {
			return true
		}
	}
	return false
}

func findVolume(deployment *appsv1.Deployment, name string) (corev1.Volume, bool) {
	for i := range deployment.Spec.Template.Spec.Volumes {
		if deployment.Spec.Template.Spec.Volumes[i].Name == name {
			return deployment.Spec.Template.Spec.Volumes[i], true
		}
	}
	return corev1.Volume{}, false
}

func hasVolume(deployment *appsv1.Deployment, name string) bool {
	_, ok := findVolume(deployment, name)
	return ok
}

func findVolumeMount(deployment *appsv1.Deployment, name string) (corev1.VolumeMount, bool) {
	if len(deployment.Spec.Template.Spec.Containers) == 0 {
		return corev1.VolumeMount{}, false
	}
	mounts := deployment.Spec.Template.Spec.Containers[0].VolumeMounts
	for i := range mounts {
		if mounts[i].Name == name {
			return mounts[i], true
		}
	}
	return corev1.VolumeMount{}, false
}

func hasVolumeMount(deployment *appsv1.Deployment, name string) bool {
	_, ok := findVolumeMount(deployment, name)
	return ok
}
