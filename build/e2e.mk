MCPCHECKER = $(shell go env GOPATH)/bin/mcpchecker
MCPCHECKER_VERSION = v0.0.16

.PHONY: install-mcpchecker
install-mcpchecker: ## Install mcpchecker locally if necessary
	@test -f $(MCPCHECKER) || go install github.com/mcpchecker/mcpchecker/cmd/mcpchecker@$(MCPCHECKER_VERSION)

GINKGO = $(shell pwd)/bin/ginkgo
GINKGO_VERSION = v2.27.2
E2E_TIMEOUT ?=15m

.PHONY: ginkgo
ginkgo: ## Download ginkgo locally if necessary
	@test -f $(GINKGO) || GOBIN=$(shell pwd)/bin go install github.com/onsi/ginkgo/v2/ginkgo@$(GINKGO_VERSION)

.PHONY: test-e2e-deps
test-e2e-deps: ginkgo ## Install e2e test dependencies
	go mod download

.PHONY: test-e2e-run
test-e2e-run: test-e2e-deps ## Run e2e tests (assumes cluster is ready)
	@echo "Running e2e tests..."
	$(GINKGO) -v --tags=e2e --timeout=$(E2E_TIMEOUT) ./tests/e2e

.PHONY: test-e2e
test-e2e: ci-setup test-e2e-run ## Run full e2e test suite (setup + run + gevals + conformance)
	@trap '$(MAKE) test-e2e-gevals-cleanup' EXIT; \
	$(MAKE) test-e2e-gevals && \
	$(MAKE) test-conformance
	@echo "E2E tests completed"

.PHONY: test-e2e-happy
test-e2e-happy: test-e2e-deps ## Quick e2e test run for local development (no setup)
	@echo "Running e2e tests (local mode)..."
	$(GINKGO) -v --tags=e2e --timeout=$(E2E_TIMEOUT) --focus="\[Happy\]" ./tests/e2e

.PHONY: test-e2e-cleanup
test-e2e-cleanup: ## Clean up e2e test resources
	@echo "Cleaning up e2e test resources..."
	-kubectl delete mcpserverregistrations -n mcp-test --all
	-kubectl delete httproutes -n mcp-test -l test=e2e

.PHONY: test-e2e-watch
test-e2e-watch: test-e2e-deps ## Run e2e tests in watch mode for development
	$(GINKGO) watch -v --tags=e2e ./tests/e2e

# CI-specific target that assumes cluster exists
.PHONY: test-e2e-ci
test-e2e-ci: test-e2e-deps enable-debug-logging ## Run e2e tests in CI (no setup, fail fast)
	$(GINKGO) -v --tags=e2e --timeout=$(E2E_TIMEOUT) --fail-fast ./tests/e2e

.PHONY: test-e2e-all-ci
test-e2e-all-ci: test-e2e-ci ## Run all e2e tests (Ginkgo, Gevals, Conformance) assuming cluster exists
	@trap '$(MAKE) test-e2e-gevals-cleanup' EXIT; \
	$(MAKE) test-e2e-gevals && \
	$(MAKE) test-conformance

# run only auth-focused tests (CI runs this after ci-auth-setup)
.PHONY: test-e2e-auth-ci
test-e2e-auth-ci: test-e2e-deps enable-debug-logging ## Run auth e2e tests only (requires ci-auth-setup)
	$(GINKGO) -v --tags=e2e --timeout=$(E2E_TIMEOUT) --fail-fast --focus="AuthPolicy" ./tests/e2e

.PHONY: test-e2e-gevals-setup
test-e2e-gevals-setup: ## Setup resources for gevals e2e tests
	@echo "Setting up gevals e2e resources..."
	kubectl apply -f tests/e2e/gevals/gevals-setup.yaml
	@echo "Waiting for gevals gateway readiness..."
	@kubectl wait --for=condition=Ready mcpgatewayextension/gevals -n mcp-gevals --timeout=180s
	@kubectl wait --for=condition=Ready mcpserverregistration/everything -n mcp-gevals --timeout=180s

.PHONY: test-e2e-gevals-run
test-e2e-gevals-run: install-mcpchecker ## Run gevals tests against e2e cluster
	@echo "Running mcpchecker..."
	@export PATH=$$(go env GOPATH)/bin:$$PATH; \
	mcpchecker check --verbose evals/gemini-agent/eval-e2e.yaml; \
	EXIT_VAL=$$?; \
	RESULTS_FILE=$$(ls -t mcpchecker-*-out.json 2>/dev/null | head -1); \
	if [ -z "$$RESULTS_FILE" ]; then \
		echo "No results file found — mcpchecker may have crashed"; \
		exit 1; \
	fi; \
	mcpchecker result verify "$$RESULTS_FILE" --task 1.0 --assertion 1.0 || EXIT_VAL=1; \
	exit $$EXIT_VAL

.PHONY: test-e2e-gevals
test-e2e-gevals: test-e2e-gevals-setup test-e2e-gevals-run ## Run gevals e2e tests (requires cluster)

.PHONY: test-e2e-gevals-cleanup
test-e2e-gevals-cleanup: ## Cleanup gevals e2e resources
	@echo "Cleaning up gevals e2e resources..."
	kubectl delete -f tests/e2e/gevals/gevals-setup.yaml --ignore-not-found

.PHONY: test-conformance
test-conformance: deploy-conformance-server ## Run MCP conformance tests
	@echo "Running MCP conformance tests..."
	@MCP_URL="http://mcp.127-0-0-1.sslip.io:8001/mcp"; \
	SCENARIOS="server-initialize tools-list ping tools-call-simple-text tools-call-image tools-call-audio tools-call-embedded-resource tools-call-mixed-content tools-call-error tools-call-with-progress"; \
	for scenario in $$SCENARIOS; do \
		echo "=== Running scenario: $$scenario ==="; \
		npx -y @modelcontextprotocol/conformance@0.1.15 server \
			--url $$MCP_URL \
			--scenario "$$scenario" || exit 1; \
	done

.PHONY: enable-debug-logging
enable-debug-logging: ## Enable debug logging on controller and wait for restart
	@echo "Enabling debug logging on mcp-gateway-controller..."
	kubectl patch deployment mcp-gateway-controller -n mcp-system --type='json' \
		-p='[{"op": "replace", "path": "/spec/template/spec/containers/0/command", "value": ["./mcp_controller", "--log-level=-4"]}]'
	@echo "Waiting for controller rollout..."
	kubectl rollout status deployment/mcp-gateway-controller -n mcp-system --timeout=120s
