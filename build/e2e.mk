# E2E test targets

GINKGO = $(shell pwd)/bin/ginkgo
GINKGO_VERSION = v2.27.2
E2E_TIMEOUT ?=30m
# Tier 2 (slow/heavy) suites are excluded from the PR gate; they run in the full/nightly suite
E2E_GINKGO_SKIP_TIER2 = --skip="\[Full\]|\[multi-gateway\]"
# Local quick run: happy-path specs only (matches [Happy] and combined [Happy,...] tags)
E2E_GINKGO_FOCUS_HAPPY = --focus="\[Happy"
E2E_PROCS ?= 1

.PHONY: ginkgo
ginkgo: ## Download ginkgo locally if necessary
	@test -f $(GINKGO) || GOBIN=$(shell pwd)/bin go install github.com/onsi/ginkgo/v2/ginkgo@$(GINKGO_VERSION)

.PHONY: test-e2e-deps
test-e2e-deps: ginkgo ## Install e2e test dependencies
	go mod download

.PHONY: test-e2e-run
test-e2e-run: test-e2e-deps ## Run e2e tests (assumes cluster is ready)
	@echo "Running e2e tests..."
	$(GINKGO) -v --tags=e2e --procs=$(E2E_PROCS) --timeout=$(E2E_TIMEOUT) ./tests/e2e

.PHONY: test-e2e
test-e2e: ci-setup test-e2e-run ## Run full e2e test suite (setup + run)
	@echo "E2E tests completed"

.PHONY: test-e2e-happy
test-e2e-happy: test-e2e-deps ## Quick e2e test run for local development (no setup)
	@echo "Running e2e tests (local mode)..."
	$(GINKGO) -v --tags=e2e --procs=$(E2E_PROCS) --timeout=$(E2E_TIMEOUT) $(E2E_GINKGO_FOCUS_HAPPY) ./tests/e2e

.PHONY: test-e2e-cleanup
test-e2e-cleanup: ## Clean up e2e test resources
	@echo "Cleaning up e2e test resources..."
	-kubectl delete mcpserverregistrations -n mcp-test --all
	-kubectl delete httproutes -n mcp-test -l test=e2e

.PHONY: test-e2e-watch
test-e2e-watch: test-e2e-deps ## Run e2e tests in watch mode for development
	$(GINKGO) watch -v --tags=e2e ./tests/e2e

# CI-specific target that assumes cluster exists.
# Debug logging is baked into config/mcp-gateway/overlays/ci/, no post-deploy patch needed.
.PHONY: test-e2e-ci
test-e2e-ci: test-e2e-deps ## Run PR-gate e2e tests in CI (excludes slow tier 2 suites, fail fast)
	$(GINKGO) -v --tags=e2e --procs=$(E2E_PROCS) --timeout=$(E2E_TIMEOUT) --fail-fast $(E2E_GINKGO_SKIP_TIER2) ./tests/e2e

.PHONY: test-e2e-ci-full
test-e2e-ci-full: test-e2e-deps ## Run all e2e tests in CI (tier 1 + 2, full run reports every failure)
	$(GINKGO) -v --tags=e2e --procs=$(E2E_PROCS) --timeout=$(E2E_TIMEOUT) ./tests/e2e

# run only auth-focused tests (CI runs this after ci-auth-setup)
.PHONY: test-e2e-auth-ci
test-e2e-auth-ci: test-e2e-deps ## Run auth e2e tests only (requires ci-auth-setup)
	$(GINKGO) -v --tags=e2e --procs=$(E2E_PROCS) --timeout=$(E2E_TIMEOUT) --fail-fast --focus="AuthPolicy" ./tests/e2e

.PHONY: test-e2e-https
test-e2e-https: test-e2e-deps ## Run HTTPS-focused E2E tests (requires cert-manager + MCP_PAT)
	@echo "Running HTTPS MCP backend E2E tests..."
	$(GINKGO) -v --tags=e2e --procs=$(E2E_PROCS) --timeout=$(E2E_TIMEOUT) --fail-fast --focus="\[HTTPS\]" ./tests/e2e
