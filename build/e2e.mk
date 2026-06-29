# E2E test targets

GINKGO = $(shell pwd)/bin/ginkgo
GINKGO_VERSION = v2.27.2
E2E_TIMEOUT ?=30m

# suite definitions (Ginkgo label expressions)
# pr: curated happy-path set (~31 specs), runs on every PR
E2E_SUITE_PR = --label-filter="pr"
# pr-extended: current broader set (~61 specs, excludes Full and multi-gateway)
E2E_SUITE_PR_EXTENDED = --skip="\[Full\]|\[multi-gateway\]"
# full: everything
E2E_SUITE_FULL =

# valid suite names for test-e2e-suite (single source: build/known-suites.txt)
E2E_KNOWN_SUITES = $(shell cat build/known-suites.txt)

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
test-e2e-happy: test-e2e-deps ## Quick e2e test run for local development (pr suite, no setup)
	@echo "Running e2e tests (local mode, pr suite)..."
	$(GINKGO) -v --tags=e2e --timeout=$(E2E_TIMEOUT) $(E2E_SUITE_PR) ./tests/e2e

.PHONY: test-e2e-cleanup
test-e2e-cleanup: ## Clean up e2e test resources
	@echo "Cleaning up e2e test resources..."
	-kubectl delete mcpserverregistrations -n mcp-test --all
	-kubectl delete httproutes -n mcp-test -l test=e2e

.PHONY: test-e2e-watch
test-e2e-watch: test-e2e-deps ## Run e2e tests in watch mode for development
	$(GINKGO) watch -v --tags=e2e ./tests/e2e

# --- CI targets ---

.PHONY: test-e2e-pr
test-e2e-pr: test-e2e-deps ## Run PR-gate e2e tests (~31 specs, curated happy-path set)
	$(GINKGO) -v --tags=e2e --timeout=$(E2E_TIMEOUT) --fail-fast $(E2E_SUITE_PR) ./tests/e2e

.PHONY: test-e2e-pr-extended
test-e2e-pr-extended: test-e2e-deps ## Run extended PR coverage (~61 specs, excludes Full/multi-gateway)
	$(GINKGO) -v --tags=e2e --timeout=$(E2E_TIMEOUT) --fail-fast $(E2E_SUITE_PR_EXTENDED) ./tests/e2e

.PHONY: test-e2e-ci-full
test-e2e-ci-full: test-e2e-deps ## Run all e2e tests in CI (full run, reports every failure)
	$(GINKGO) -v --tags=e2e --timeout=$(E2E_TIMEOUT) ./tests/e2e

# run a named functional suite (e.g. make test-e2e-suite SUITE=discovery)
.PHONY: test-e2e-suite
test-e2e-suite: test-e2e-deps ## Run a named e2e suite (SUITE=core|routing|sessions|...)
	@if [ -z "$(SUITE)" ]; then \
		echo "usage: make test-e2e-suite SUITE=<name>"; \
		echo "known suites: $(E2E_KNOWN_SUITES)"; \
		exit 1; \
	fi
	$(GINKGO) -v --tags=e2e --timeout=$(E2E_TIMEOUT) --fail-fast --label-filter="$(SUITE)" ./tests/e2e

# run only auth-focused tests (CI runs this after ci-auth-setup)
.PHONY: test-e2e-auth-ci
test-e2e-auth-ci: test-e2e-deps ## Run auth e2e tests only (requires ci-auth-setup)
	$(GINKGO) -v --tags=e2e --timeout=$(E2E_TIMEOUT) --fail-fast --label-filter="auth-policy" ./tests/e2e

.PHONY: test-e2e-https
test-e2e-https: test-e2e-deps ## Run HTTPS-focused E2E tests (requires cert-manager + MCP_PAT)
	@echo "Running HTTPS MCP backend E2E tests..."
	$(GINKGO) -v --tags=e2e --timeout=$(E2E_TIMEOUT) --fail-fast --label-filter="tls" ./tests/e2e

.PHONY: test-e2e-list-suites
test-e2e-list-suites: ## List available e2e suites
	@echo "Available suites: $(E2E_KNOWN_SUITES)"
	@echo ""
	@echo "Aggregate targets:"
	@echo "  pr            curated happy-path (~31 specs)"
	@echo "  pr-extended   broader coverage (~61 specs)"
	@echo "  full          all specs"
