# E2E test targets

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
test-e2e: ci-setup test-e2e-run ## Run full e2e test suite (setup + run)
	@echo "E2E tests completed"

.PHONY: test-e2e-happy
test-e2e-happy: test-e2e-deps ## Quick e2e test run for local development (no setup)
	@echo "Running e2e tests (local mode)..."
	$(GINKGO) -v --tags=e2e --timeout=$(E2E_TIMEOUT) --focus="[Happy]" ./tests/e2e

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
test-e2e-ci: test-e2e-deps ## Run e2e tests in CI (no setup, fail fast)
	$(GINKGO) -v --tags=e2e --timeout=$(E2E_TIMEOUT) --fail-fast ./tests/e2e
