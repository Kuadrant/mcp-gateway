# Istio

SAIL_VERSION = 1.27.0
ISTIO_NAMESPACE = istio-system
ISTIO_VERSION = 1.27.0

# istioctl tool
ISTIOCTL = bin/istioctl
$(ISTIOCTL):
	mkdir -p bin
	curl -sL https://istio.io/downloadIstio | ISTIO_VERSION=$(ISTIO_VERSION) TARGET_ARCH=$(ARCH) sh -
	mv istio-$(ISTIO_VERSION)/bin/istioctl bin/
	rm -rf istio-$(ISTIO_VERSION)

istioctl-impl: $(ISTIOCTL)
	@echo "istioctl installed at: $(ISTIOCTL)"
	@echo "Version: $$($(ISTIOCTL) version --remote=false)"

.PHONY: istio-install
istio-install: $(HELM) # Install Istio using Sail operator
	$(HELM) upgrade --install sail-operator \
		--create-namespace \
		--namespace $(ISTIO_NAMESPACE) \
		--wait \
		--timeout=300s \
		https://github.com/istio-ecosystem/sail-operator/releases/download/$(SAIL_VERSION)/sail-operator-$(SAIL_VERSION).tgz
	@echo "Applying minimal Istio configuration..."
	@kubectl apply -f - <<EOF
apiVersion: install.istio.io/v1alpha1
kind: Istio
metadata:
  name: default
  namespace: $(ISTIO_NAMESPACE)
spec:
  values:
    meshConfig:
      accessLogFile: /dev/stdout
EOF
	kubectl -n $(ISTIO_NAMESPACE) wait --for=condition=Ready istio/default --timeout=300s

.PHONY: istio-uninstall
istio-uninstall: $(HELM) # Uninstall Istio and Sail operator
	- kubectl delete istio/default -n $(ISTIO_NAMESPACE)
	$(HELM) uninstall sail-operator -n $(ISTIO_NAMESPACE)
	- kubectl delete namespace $(ISTIO_NAMESPACE)
