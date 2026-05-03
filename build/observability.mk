
##@ Observability Setup

.PHONY: observability-setup
observability-setup: $(HELM) ## Setup observability environment with Loki and Grafana
	@echo "========================================="
	@echo "Setting up Observability Environment"
	@echo "========================================="
	@echo "Step 1/4: Deploying Loki via Helm..."
	@$(HELM) repo add grafana https://grafana.github.io/helm-charts
	@$(HELM) repo update
	@$(HELM) upgrade --install loki grafana/loki --namespace loki --create-namespace --values ./tests/e2e/assets/observability/values.yaml
	@echo "Loki deployed"
	@echo ""
	@echo "Step 2/4: Installing grafana-alloy to send logs to Loki..."
	@$(HELM) upgrade --install grafana-alloy grafana/alloy -n loki -f ./tests/e2e/assets/observability/alloy-values.yaml
	@echo "Grafana Alloy deployed for log visualization"
	@echo ""
	@echo "Step 3/4: Deploying Grafana dashboard..."
	@$(HELM) repo add grafana https://grafana.github.io/helm-charts
	@$(HELM) upgrade --install my-grafana grafana/grafana -n monitoring --create-namespace --values ./tests/e2e/assets/observability/dashboard-values.yaml
	@echo "Grafana dashboard deployed"
	@echo ""
	@echo "Step 4/4: Verifying setup..."
	@kubectl get pods -n mcp-system
	@echo "Observability environment setup complete"
	
.PHONY: observability-teardown
observability-teardown: $(HELM) ## Remove observability environment (Loki, Grafana, Alloy)
	@echo "========================================="
	@echo "Uninstalling Observability Environment"
	@echo "========================================="
	@$(HELM) uninstall loki -n loki || true
	@$(HELM) uninstall grafana-alloy -n loki || true
	@$(HELM) uninstall my-grafana -n monitoring || true

