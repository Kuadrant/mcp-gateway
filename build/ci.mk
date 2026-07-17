# CI-specific targets for GitHub Actions

# How the gateway/controller images reach the Kind cluster: "build" (default)
# builds them locally first, "prebuilt" only loads images already present in
# the local container engine (CI builds them in a workflow step so the builds
# get buildx gha layer caching).
GATEWAY_IMAGE_SOURCE ?= build

.PHONY: ci-gateway-images
ifeq ($(GATEWAY_IMAGE_SOURCE),prebuilt)
ci-gateway-images: load-image
else ifeq ($(GATEWAY_IMAGE_SOURCE),build)
ci-gateway-images: build-and-load-image
else
$(error GATEWAY_IMAGE_SOURCE must be "build" or "prebuilt", got "$(GATEWAY_IMAGE_SOURCE)")
endif

# redis lives in mcp-system, so the namespace must exist before the controller
# overlay applies it again (idempotent) later in ci-setup
.PHONY: ci-deploy-redis
ci-deploy-redis:
	$(KUBECTL) apply -f config/mcp-gateway/overlays/ci/namespace.yaml
	"$(MAKE)" deploy-redis

# the installs are independent and each carries its own readiness wait, so run
# them in parallel; the slowest one bounds the phase
.PHONY: ci-install-infra
ci-install-infra:
	"$(MAKE)" -j4 istio-install metallb-install cert-manager-install ci-deploy-redis

# CI setup for e2e tests
# Deploys e2e gateways (gateway-1, gateway-2) and controller only
# Tests create their own MCPGatewayExtensions
.PHONY: ci-setup
ci-setup: tools kind-create-cluster ci-gateway-images gateway-api-install ci-install-infra install-crd ## Setup environment for CI e2e tests
	@echo "Setting up CI environment..."
	# Create gateway namespace early — needed by both deploy-gateway and the
	# gateway TLS cert issued by deploy-tls-test-server.
	$(KUBECTL) apply -f config/istio/gateway/namespace.yaml
	# Deploy TLS infra — installs cert-manager, creates private CA, issues the
	# gateway HTTPS cert (mcp-gateway-tls-cert) and the TLS test server cert.
	# Must run before deploy-gateway so the mcp-tls listener has its cert ready.
	"$(MAKE)" deploy-tls-test-server
	# Deploy standard mcp-gateway (includes mcp-tls HTTPS listener)
	"$(MAKE)" deploy-gateway
	# Deploy e2e gateways (gateway-1, gateway-2)
	"$(MAKE)" deploy-e2e-gateways
	# Deploy controller only (no MCPGatewayExtension)
	"$(MAKE)" deploy-controller-only
	# Deploy and wait for test servers
	"$(MAKE)" deploy-test-servers-ci
	@echo "CI setup complete (3 gateways: mcp-gateway, e2e-1, e2e-2)"

# Deploy test servers for CI (TEST_SERVER_IMAGE_SOURCE selects build vs pull)
.PHONY: deploy-test-servers-ci
deploy-test-servers-ci: load-test-servers ## Deploy test servers for CI
	$(KUBECTL) apply -k config/test-servers/
	"$(MAKE)" wait-test-servers

# Auth infrastructure for e2e auth tests.
# Deploys cert-manager, Kuadrant/Authorino, and Keycloak, then applies AuthPolicies.
# Unlike keycloak-install, this skips the API server OIDC configuration and restart
# which isn't needed for gateway auth tests and destabilises the CI cluster.
.PHONY: ci-auth-setup
ci-auth-setup: cert-manager-install kuadrant-install ## Setup auth infrastructure for CI e2e tests
	@echo "Setting up auth infrastructure for CI..."
	# deploy Keycloak
	$(KUBECTL) create namespace keycloak 2>/dev/null || true
	$(KUBECTL) apply -f config/keycloak/realm-import.yaml
	$(KUBECTL) apply -f config/keycloak/deployment.yaml
	$(KUBECTL) wait --for=condition=ready pod -l app=keycloak -n keycloak --timeout=300s
	# add Keycloak listener to gateway
	$(KUBECTL) patch gateway mcp-gateway -n gateway-system --type json -p "$$(cat config/keycloak/patch-gateway.json)"
	$(KUBECTL) apply -f config/keycloak/httproute.yaml
	# issue TLS cert via cert-manager
	$(KUBECTL) apply -f config/keycloak/certificate.yaml
	@for i in $$(seq 1 30); do $(KUBECTL) get secret mcp-gateway-keycloak-cert -n gateway-system >/dev/null 2>&1 && break; echo "Waiting for TLS cert..."; sleep 2; done; \
		$(KUBECTL) get secret mcp-gateway-keycloak-cert -n gateway-system >/dev/null 2>&1 || { echo "ERROR: TLS cert secret not created after 60s"; exit 1; }
	# extract CA cert for Authorino
	@mkdir -p out/certs
	$(KUBECTL) get secret mcp-gateway-keycloak-cert -n gateway-system -o jsonpath='{.data.ca\.crt}' | base64 -d > out/certs/ca.crt
	# resolve Keycloak hostname inside Kind node
	@GATEWAY_IP=$$($(KUBECTL) get gateway/mcp-gateway -n gateway-system -o jsonpath='{.status.addresses[0].value}' 2>/dev/null); \
		if [ -z "$$GATEWAY_IP" ]; then echo "ERROR: gateway has no IP address" && exit 1; fi; \
		docker exec mcp-gateway-control-plane bash -c "grep -q 'keycloak.127-0-0-1.sslip.io' /etc/hosts || echo '$$GATEWAY_IP keycloak.127-0-0-1.sslip.io' >> /etc/hosts"
	# apply AuthPolicies: reuse sample secrets, e2e-specific policies target mcp-tls listener
	$(KUBECTL) apply -f ./config/samples/oauth-token-exchange/trusted-header-public-key.yaml
	@$(detect-kuadrant-ns); \
	$(KUBECTL) apply -f ./config/samples/oauth-token-exchange/trusted-headers-private-key.yaml -n $$KUADRANT_NS
	$(KUBECTL) apply -f ./config/e2e/auth/mcp-auth-policy.yaml
	$(KUBECTL) apply -f ./config/e2e/auth/mcps-auth-policy.yaml
	# patch Authorino to reach Keycloak
	@$(detect-kuadrant-ns); \
	./utils/patch-authorino-to-keycloak.sh $$KUADRANT_NS
	@echo "CI auth setup complete"

# Collect debug info on failure
.PHONY: ci-debug-logs
ci-debug-logs: ## Collect logs for debugging CI failures
	@echo "=== Controller logs ==="
	-$(KUBECTL) logs -n mcp-system deployment/mcp-gateway-controller --tail=100
	@echo "=== MCPGatewayExtensions ==="
	-$(KUBECTL) get mcpgatewayextensions -A
	@echo "=== MCPServerRegistrations ==="
	-$(KUBECTL) get mcpserverregistrations -A
	@echo "=== HTTPRoutes ==="
	-$(KUBECTL) get httproutes -A
	@echo "=== Gateways ==="
	-$(KUBECTL) get gateways -A
	@echo "=== AuthPolicies ==="
	-$(KUBECTL) get authpolicies -A
	@echo "=== Pods ==="
	-$(KUBECTL) get pods -A

.PHONY: ci-debug-test-servers-logs
ci-debug-test-servers-logs: ## Collect test server logs for debugging CI failures
	@echo "=== Test server logs ==="
	-$(KUBECTL) logs -n mcp-test deployment/mcp-test-server1 --tail=50
	-$(KUBECTL) logs -n mcp-test deployment/mcp-test-server2 --tail=50
	-$(KUBECTL) logs -n mcp-test deployment/mcp-test-server3 --tail=50
