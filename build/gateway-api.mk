# Gateway API
GATEWAY_API_VERSION ?= v1.4.1

.PHONY: gateway-api-install
gateway-api-install: ## Install Gateway API CRDs
	$(KUBECTL) apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/$(GATEWAY_API_VERSION)/standard-install.yaml
	$(KUBECTL) wait --for condition=Established --timeout=60s crd/gateways.gateway.networking.k8s.io

.PHONY: gateway-api-uninstall
gateway-api-uninstall: ## Uninstall Gateway API CRDs
	-$(KUBECTL) delete -f https://github.com/kubernetes-sigs/gateway-api/releases/download/$(GATEWAY_API_VERSION)/standard-install.yaml
