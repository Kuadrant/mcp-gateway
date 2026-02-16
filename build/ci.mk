# CI-specific targets for GitHub Actions

# CI setup for e2e tests
# Deploys e2e gateways (gateway-1, gateway-2) and controller only
# Tests create their own MCPGatewayExtensions
.PHONY: ci-setup
ci-setup: setup-cluster-base ## Setup environment for CI e2e tests
	@echo "Setting up CI environment..."
	# Deploy standard mcp-gateway (mcp.127-0-0-1.sslip.io)
	"$(MAKE)" deploy-gateway
	# Deploy e2e gateways (gateway-1, gateway-2)
	"$(MAKE)" deploy-e2e-gateways
	# Deploy controller only (no MCPGatewayExtension)
	"$(MAKE)" deploy-controller-only
	# Deploy and wait for test servers
	"$(MAKE)" deploy-test-servers-ci
	@echo "CI setup complete (3 gateways: mcp-gateway, e2e-1, e2e-2)"

# Deploy test servers for CI
.PHONY: deploy-test-servers-ci
deploy-test-servers-ci: kind-load-test-servers ## Deploy test servers for CI
	$(KUBECTL) apply -k config/test-servers/
	"$(MAKE)" wait-test-servers

# Collect debug info on failure
.PHONY: ci-debug-logs
ci-debug-logs: ## Collect logs for debugging CI failures
	@echo "=== Controller logs ==="
	-$(KUBECTL) logs -n mcp-system deployment/mcp-controller --tail=100
	@echo "=== MCPGatewayExtensions ==="
	-$(KUBECTL) get mcpgatewayextensions -A
	@echo "=== MCPServerRegistrations ==="
	-$(KUBECTL) get mcpserverregistrations -A
	@echo "=== HTTPRoutes ==="
	-$(KUBECTL) get httproutes -A
	@echo "=== Gateways ==="
	-$(KUBECTL) get gateways -A
	@echo "=== Pods ==="
	-$(KUBECTL) get pods -A

.PHONY: ci-debug-test-servers-logs
ci-debug-test-servers-logs: ## Collect test server logs for debugging CI failures
	@echo "=== Test server logs ==="
	-$(KUBECTL) logs -n mcp-test deployment/mcp-test-server1 --tail=50
	-$(KUBECTL) logs -n mcp-test deployment/mcp-test-server2 --tail=50
	-$(KUBECTL) logs -n mcp-test deployment/mcp-test-server3 --tail=50
