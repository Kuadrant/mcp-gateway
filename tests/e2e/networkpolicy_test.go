//go:build e2e

package e2e

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Broker-Router NetworkPolicy", func() {
	JustAfterEach(func() {
		if CurrentSpecReport().Failed() {
			DumpClusterState(ctx, SystemNamespace, GatewayNamespace, TestServerNameSpace)
		}
	})

	It("[Happy] controller creates and owns the mcp-gateway NetworkPolicy", func() {
		deploymentName := "mcp-gateway"

		By("Waiting for the shared broker-router deployment to be ready")
		Expect(WaitForDeploymentReady(ctx, SystemNamespace, deploymentName)).To(Succeed())

		By("Verifying the NetworkPolicy exists and is owned by the MCPGatewayExtension")
		ext := getSingleMCPGatewayExtension(ctx, k8sClient, SystemNamespace)
		verifier := NewVerifier(ctx, k8sClient)

		var networkPolicy *networkingv1.NetworkPolicy
		Eventually(func(g Gomega) {
			np := &networkingv1.NetworkPolicy{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: deploymentName, Namespace: SystemNamespace}, np)).To(Succeed())
			networkPolicy = np

			g.Expect(verifier.NetworkPolicyHasOwnerReference(deploymentName, SystemNamespace, ext.Name)).To(Succeed())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())

		By("Verifying ingress allows 8080/50051 from the gateway namespace and 9090 from anywhere")
		Expect(networkPolicy.Spec.Ingress).To(HaveLen(2))

		gatewayNSRule := networkPolicy.Spec.Ingress[0]
		Expect(gatewayNSRule.From).To(HaveLen(1))
		Expect(gatewayNSRule.From[0].NamespaceSelector).NotTo(BeNil())
		Expect(gatewayNSRule.From[0].NamespaceSelector.MatchLabels).To(HaveKeyWithValue("kubernetes.io/metadata.name", GatewayNamespace))
		gatewayNSPorts := []intstr.IntOrString{}
		for i := range gatewayNSRule.Ports {
			gatewayNSPorts = append(gatewayNSPorts, *gatewayNSRule.Ports[i].Port)
		}
		Expect(gatewayNSPorts).To(ConsistOf(intstr.FromInt(8080), intstr.FromInt(50051)))

		metricsRule := networkPolicy.Spec.Ingress[1]
		Expect(metricsRule.From).To(BeEmpty())
		Expect(metricsRule.Ports).To(HaveLen(1))
		Expect(*metricsRule.Ports[0].Port).To(Equal(intstr.FromInt(9090)))

		By("Verifying egress allows all")
		Expect(networkPolicy.Spec.Egress).To(HaveLen(1))
		Expect(networkPolicy.Spec.Egress[0].To).To(BeEmpty())
		Expect(networkPolicy.Spec.Egress[0].Ports).To(BeEmpty())
	})
})
