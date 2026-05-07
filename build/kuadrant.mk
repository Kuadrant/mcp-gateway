# Kuadrant API Gateway Policy Management

KUADRANT_NAMESPACE = kuadrant-system
HELM ?= bin/helm
KUSTOMIZE ?= bin/kustomize
KUBECTL ?= kubectl

.PHONY: kuadrant-install
kuadrant-install-impl: $(HELM)
	@if kubectl get crd kuadrants.kuadrant.io >/dev/null 2>&1; then \
		echo "Kuadrant CRDs already present (installed via OLM or Helm), skipping Helm install."; \
	else \
		echo "Installing Kuadrant operator..."; \
		$(HELM) repo add kuadrant https://kuadrant.io/helm-charts 2>/dev/null || true; \
		$(HELM) repo update; \
		if $(HELM) list -n $(KUADRANT_NAMESPACE) | grep -q kuadrant-operator; then \
			echo "Kuadrant operator already installed, upgrading..."; \
			$(HELM) upgrade \
				kuadrant-operator kuadrant/kuadrant-operator \
				--wait \
				--timeout=600s \
				--namespace $(KUADRANT_NAMESPACE); \
		else \
			$(HELM) install \
				kuadrant-operator kuadrant/kuadrant-operator \
				--create-namespace \
				--wait \
				--timeout=600s \
				--namespace $(KUADRANT_NAMESPACE); \
		fi; \
	fi
	@if kubectl get namespace kuadrant-system >/dev/null 2>&1; then KUADRANT_NS=kuadrant-system; else KUADRANT_NS=mcp-system; fi; \
	if kubectl get kuadrant -A 2>/dev/null | grep -q kuadrant; then \
		echo "Kuadrant CR already exists, skipping."; \
	else \
		echo "Instantiating Kuadrant in namespace $$KUADRANT_NS..."; \
		$(KUBECTL) apply -f config/kuadrant/kuadrant.yaml -n $$KUADRANT_NS; \
		echo "Kuadrant CR created."; \
	fi

.PHONY: kuadrant-uninstall
kuadrant-uninstall-impl: $(HELM)
	@echo "Uninstalling Kuadrant operator..."
	@$(HELM) uninstall \
		kuadrant-operator \
		--namespace $(KUADRANT_NAMESPACE)
	@kubectl delete namespace $(KUADRANT_NAMESPACE)

.PHONY: kuadrant-configure
kuadrant-configure-impl: $(KUSTOMIZE)
	@echo "Applying Kuadrant configuration..."
	@$(KUSTOMIZE) build config/kuadrant | kubectl apply -f -

.PHONY: kuadrant-status
kuadrant-status-impl:
	@if kubectl get deployments -n $(KUADRANT_NAMESPACE) 2>/dev/null | grep -q kuadrant; then \
		echo "========================================"; \
		echo "Kuadrant Status"; \
		echo "========================================"; \
		echo ""; \
		echo "Status: Installed"; \
		echo ""; \
		echo "Operator:"; \
		kubectl get deployments -n $(KUADRANT_NAMESPACE); \
		echo ""; \
		echo "Available CRDs:"; \
		kubectl get crd | grep kuadrant.io || echo "  No Kuadrant CRDs found"; \
		echo ""; \
		echo "To create policies:"; \
		echo "  - RateLimitPolicy: Controls rate limiting"; \
		echo "  - AuthPolicy: Manages authentication"; \
		echo "  - DNSPolicy: Handles DNS configuration"; \
		echo "  - TLSPolicy: Manages TLS certificates"; \
		echo "========================================"; \
	else \
		echo "Kuadrant is not installed. Run: make kuadrant-install"; \
	fi
