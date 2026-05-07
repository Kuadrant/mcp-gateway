##@ Performance Testing

PERF_MOCK_IMG ?= ghcr.io/kuadrant/mcp-gateway/perf-mock-server:latest
PERF_USERS ?= 64
PERF_MAX_USERS ?= 4096
PERF_RAMP_RATE ?= 8
PERF_DURATION ?= 5m
PERF_HOLD_DURATION ?= 5m
PERF_TARGET_URL ?= http://localhost:8001/mcp
PERF_TOOL_PREFIX ?= mock_
PERF_OUT_DIR := out/perf/$(shell date +%Y%m%d-%H%M%S)
PERF_PPROF_URL ?= http://localhost:6060

.PHONY: perf-build-mock-server
perf-build-mock-server: ## Build perf mock server image
	cd tests/perf/mock-server && $(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) -t $(PERF_MOCK_IMG) .

.PHONY: perf-build-k6
perf-build-k6: ## Build k6 binary with xk6-mcp extension
	@if [ ! -f bin/k6 ]; then \
		echo "Building k6 with xk6-mcp..."; \
		go install go.k6.io/xk6/cmd/xk6@latest && \
		xk6 build --with github.com/infobip/xk6-infobip-mcp --output ./bin/k6; \
	else \
		echo "[OK] bin/k6 already exists"; \
	fi

.PHONY: perf-setup
perf-setup: kind perf-build-mock-server ## Deploy perf mock server into Kind cluster
	@echo "Loading perf mock server image..."
	$(call load-image,$(PERF_MOCK_IMG))
	kubectl apply -f config/test-servers/namespace.yaml
	kubectl apply -f tests/perf/manifests/mock-server.yaml -n mcp-test
	kubectl apply -f tests/perf/manifests/registration.yaml
	@echo "Waiting for perf mock server..."
	@kubectl wait --for=condition=Available deployment/perf-mock-server -n mcp-test --timeout=60s
	@echo "Waiting for MCPServerRegistration..."
	@kubectl wait --for=condition=Ready mcpsr/perf-mock-server -n mcp-test --timeout=120s
	@echo "Perf test environment ready"

.PHONY: perf-teardown
perf-teardown: ## Remove perf test resources
	kubectl delete -f tests/perf/manifests/registration.yaml --ignore-not-found
	kubectl delete -f tests/perf/manifests/mock-server.yaml -n mcp-test --ignore-not-found

.PHONY: perf-run-steady
perf-run-steady: perf-build-k6 ## Run steady-state concurrency test
	@mkdir -p $(PERF_OUT_DIR)
	K6_WEB_DASHBOARD=true \
	K6_WEB_DASHBOARD_EXPORT=$(PERF_OUT_DIR)/k6-steady-$(PERF_USERS)vu.html \
	K6_WEB_DASHBOARD_PORT=-1 \
	./bin/k6 run \
		--out csv=$(PERF_OUT_DIR)/k6-steady-$(PERF_USERS)vu.csv \
		-e TARGET_URL=$(PERF_TARGET_URL) \
		-e TOOL_PREFIX=$(PERF_TOOL_PREFIX) \
		-e USERS=$(PERF_USERS) \
		-e DURATION=$(PERF_DURATION) \
		tests/perf/k6/concurrency-levels.js 2>&1 | tee $(PERF_OUT_DIR)/k6-steady-$(PERF_USERS)vu.log
	@go run ./tests/perf/cmd/report \
		-csv $(PERF_OUT_DIR)/k6-steady-$(PERF_USERS)vu.csv \
		-title "MCP Gateway Steady State: $(PERF_USERS) VUs for $(PERF_DURATION)" \
		-out $(PERF_OUT_DIR)/mcp-report.html
	@echo "Results saved to $(PERF_OUT_DIR)/"
	@echo "  open $(PERF_OUT_DIR)/mcp-report.html"

.PHONY: perf-run-ramp
perf-run-ramp: perf-build-k6 ## Run ramp-up test with profiling
	@mkdir -p $(PERF_OUT_DIR)/profiles
	@# start resource collector in background
	@tests/perf/scripts/collect-resources.sh $(PERF_OUT_DIR)/resources.csv 2 &
	@# start pprof port-forward
	@kubectl port-forward -n $(MCP_GATEWAY_NAMESPACE) deployment/$(BROKER_ROUTER_NAME) 6060:6060 > /dev/null 2>&1 &
	@sleep 2
	@echo "Capturing baseline profiles..."
	@go tool pprof -proto -output $(PERF_OUT_DIR)/profiles/heap-before.pb.gz $(PERF_PPROF_URL)/debug/pprof/heap 2>/dev/null || echo "  (baseline heap failed)"
	@go tool pprof -proto -output $(PERF_OUT_DIR)/profiles/goroutine-before.pb.gz $(PERF_PPROF_URL)/debug/pprof/goroutine 2>/dev/null || echo "  (baseline goroutine failed)"
	@# run k6 with CPU profiling in parallel
	@echo "Starting ramp-up test ($(PERF_MAX_USERS) users at $(PERF_RAMP_RATE)/s)..."
	@go tool pprof -proto -output $(PERF_OUT_DIR)/profiles/cpu.pb.gz $(PERF_PPROF_URL)/debug/pprof/profile?seconds=60 2>/dev/null &
	K6_WEB_DASHBOARD=true \
	K6_WEB_DASHBOARD_EXPORT=$(PERF_OUT_DIR)/k6-ramp.html \
	K6_WEB_DASHBOARD_PORT=-1 \
	./bin/k6 run \
		--out csv=$(PERF_OUT_DIR)/k6-ramp.csv \
		-e TARGET_URL=$(PERF_TARGET_URL) \
		-e TOOL_PREFIX=$(PERF_TOOL_PREFIX) \
		-e MAX_USERS=$(PERF_MAX_USERS) \
		-e RAMP_RATE=$(PERF_RAMP_RATE) \
		-e HOLD_DURATION=$(PERF_HOLD_DURATION) \
		tests/perf/k6/ramp-up.js 2>&1 | tee $(PERF_OUT_DIR)/k6-ramp.log
	@# re-establish port-forward (may have died under load) and capture post-load profiles
	@-pkill -f 'port-forward.*6060' 2>/dev/null || true
	@kubectl port-forward -n $(MCP_GATEWAY_NAMESPACE) deployment/$(BROKER_ROUTER_NAME) 6060:6060 > /dev/null 2>&1 &
	@sleep 3
	@echo "Capturing post-load profiles..."
	@go tool pprof -proto -output $(PERF_OUT_DIR)/profiles/heap-after.pb.gz $(PERF_PPROF_URL)/debug/pprof/heap 2>/dev/null || echo "  (heap capture failed - broker may have crashed)"
	@go tool pprof -proto -output $(PERF_OUT_DIR)/profiles/goroutine-after.pb.gz $(PERF_PPROF_URL)/debug/pprof/goroutine 2>/dev/null || echo "  (goroutine capture failed)"
	@go tool pprof -proto -output $(PERF_OUT_DIR)/profiles/mutex.pb.gz $(PERF_PPROF_URL)/debug/pprof/mutex 2>/dev/null || echo "  (mutex capture failed)"
	@# collect broker logs and pod status
	@kubectl logs -n $(MCP_GATEWAY_NAMESPACE) deployment/$(BROKER_ROUTER_NAME) --tail=2000 > $(PERF_OUT_DIR)/broker.log 2>&1
	@kubectl get pods -n $(MCP_GATEWAY_NAMESPACE) -o wide > $(PERF_OUT_DIR)/pod-status.txt 2>&1
	@# stop resource collector
	@touch $(PERF_OUT_DIR)/resources.csv.stop
	@sleep 2
	@# generate MCP report
	@go run ./tests/perf/cmd/report \
		-csv $(PERF_OUT_DIR)/k6-ramp.csv \
		-resources $(PERF_OUT_DIR)/resources.csv \
		-title "MCP Gateway Ramp-Up: $(PERF_MAX_USERS) users at $(PERF_RAMP_RATE)/s" \
		-out $(PERF_OUT_DIR)/mcp-report.html
	@echo ""
	@echo "=== Results saved to $(PERF_OUT_DIR)/ ==="
	@echo "  mcp-report.html      MCP performance report"
	@echo "  k6-ramp.csv          k6 time-series metrics"
	@echo "  resources.csv        CPU/memory/goroutine time-series"
	@echo "  broker.log           broker-router logs"
	@echo "  profiles/            pprof profiles (before/after + 60s CPU)"
	@echo ""
	@echo "  open $(PERF_OUT_DIR)/mcp-report.html"
	@echo "  go tool pprof -http :9090 $(PERF_OUT_DIR)/profiles/cpu.pb.gz"
	@-pkill -f 'port-forward.*6060' 2>/dev/null || true

.PHONY: perf-profile
perf-profile: ## Capture a one-off pprof snapshot from broker-router
	@mkdir -p out/profiles
	@kubectl port-forward -n $(MCP_GATEWAY_NAMESPACE) deployment/$(BROKER_ROUTER_NAME) 6060:6060 > /dev/null 2>&1 &
	@sleep 2
	@echo "Capturing CPU profile (30s)..."
	@go tool pprof -proto -output out/profiles/cpu.pb.gz $(PERF_PPROF_URL)/debug/pprof/profile?seconds=30
	@echo "Capturing heap profile..."
	@go tool pprof -proto -output out/profiles/heap.pb.gz $(PERF_PPROF_URL)/debug/pprof/heap
	@echo "Capturing goroutine profile..."
	@go tool pprof -proto -output out/profiles/goroutine.pb.gz $(PERF_PPROF_URL)/debug/pprof/goroutine
	@echo "Capturing mutex profile..."
	@go tool pprof -proto -output out/profiles/mutex.pb.gz $(PERF_PPROF_URL)/debug/pprof/mutex
	@echo "Profiles saved to out/profiles/"
	@-pkill -f 'port-forward.*6060' 2>/dev/null || true
