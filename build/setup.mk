# Common setup targets for cluster initialization

# Base cluster setup - infrastructure only (Kind, Istio, MetalLB, Gateway API, CRDs)
.PHONY: setup-cluster-base
setup-cluster-base: tools kind-create-cluster build-and-load-image gateway-api-install istio-install metallb-install install-crd ## Setup base cluster infrastructure
	@echo "Base cluster setup complete"

# Deploy controller only (no MCPGatewayExtension - for e2e tests)
.PHONY: deploy-controller-only
deploy-controller-only: ## Deploy only the controller (tests create their own MCPGatewayExtensions)
	$(KUBECTL) apply -k config/mcp-gateway/overlays/ci/
	@echo "Waiting for controller to be ready..."
	@$(KUBECTL) wait --for=condition=available --timeout=180s deployment/mcp-controller -n mcp-system

# Wait for test server deployments
.PHONY: wait-test-servers
wait-test-servers: ## Wait for all test server deployments to be available
	@echo "Waiting for test servers..."
	@$(KUBECTL) wait --for=condition=available --timeout=180s deployment/mcp-test-server1 -n mcp-test
	@$(KUBECTL) wait --for=condition=available --timeout=180s deployment/mcp-test-server2 -n mcp-test
	@$(KUBECTL) wait --for=condition=available --timeout=180s deployment/mcp-test-server3 -n mcp-test
	@$(KUBECTL) wait --for=condition=available --timeout=180s deployment/mcp-api-key-server -n mcp-test
	@$(KUBECTL) wait --for=condition=available --timeout=180s deployment/mcp-custom-path-server -n mcp-test
	@$(KUBECTL) wait --for=condition=available --timeout=180s deployment/mcp-oidc-server -n mcp-test
	@$(KUBECTL) wait --for=condition=available --timeout=180s deployment/everything-server -n mcp-test
	@echo "All test servers ready"
