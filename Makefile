# Platform detection
OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
ARCH := $(shell uname -m | tr '[:upper:]' '[:lower:]')
ifeq ($(ARCH),x86_64)
    ARCH = amd64
endif
ifeq ($(ARCH),aarch64)
    ARCH = arm64
endif

LOG_LEVEL ?= -4

# Container engine
CONTAINER_ENGINE ?= docker
ifeq (podman,$(CONTAINER_ENGINE))
	CONTAINER_ENGINE_EXTRA_FLAGS ?= --load
endif

WAIT_TIME ?=120s

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)


ENVTEST ?= $(LOCALBIN)/setup-envtest

# Gateway API version for CRDs
GATEWAY_API_VERSION ?= v1.4.1

# The KIND cluster name must match ./build/kind.mk
KIND_CLUSTER_NAME ?= mcp-gateway
MCP_GATEWAY_NAMESPACE ?= mcp-system
MCP_GATEWAY_SUBDOMAIN ?= mcp
MCP_GATEWAY_HOST ?= $(MCP_GATEWAY_SUBDOMAIN).127-0-0-1.sslip.io
MCP_GATEWAY_NAME ?= mcp-gateway

.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: build clean mcp-broker-router controller

# Build the combined broker and router
mcp-broker-router:
	go build -race -o bin/mcp-broker-router ./cmd/mcp-broker-router

# Build the controller
controller:
	go build -race -o bin/mcp-controller ./cmd

# Build all binaries
build: mcp-broker-router controller

# Clean build artifacts
clean:
	rm -rf bin/

# Run the broker/router (standalone mode)
run: mcp-broker-router
	./bin/mcp-broker-router --log-level=${LOG_LEVEL}

# Run the broker and router with debug logging (alias for backwards compatibility)
run-mcp-broker-router: run

# Run the controller (discovers MCP servers from Kubernetes)
run-controller: controller
	./bin/mcp-controller --log-level=${LOG_LEVEL}

# controller-gen version
CONTROLLER_GEN_VERSION ?= v0.20.0

# Install controller-gen
.PHONY: controller-gen
controller-gen: ## Install controller-gen to ./bin/
	@mkdir -p bin
	@if [ ! -f bin/controller-gen ]; then \
		echo "Installing controller-gen $(CONTROLLER_GEN_VERSION)..."; \
		GOBIN=$(shell pwd)/bin go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION); \
	fi

# Generate code (deepcopy, etc.)
generate: controller-gen ## Generate code including deepcopy functions
	bin/controller-gen object paths="./api/..."

# Generate CRDs from Go types
generate-crds: generate ## Generate CRD manifests from Go types
	bin/controller-gen crd paths="./api/..." output:dir=config/crd

# Update Helm chart CRDs from generated ones
update-helm-crds: generate-crds ## Update Helm chart CRDs (run after generate-crds)
	@echo "Copying CRDs to Helm chart..."
	@mkdir -p charts/mcp-gateway/crds
	cp config/crd/mcp.kagenti.com_*.yaml charts/mcp-gateway/crds/
	@echo "✅ Helm chart CRDs updated"

# Generate CRDs and update Helm chart in one step
generate-crds-all: update-helm-crds ## Generate CRDs and update Helm chart
	@echo "✅ All CRDs generated and synchronized"

# Check if CRDs are synchronized between config/crd and charts/
check-crd-sync: ## Check if CRDs are synchronized between config/crd and charts/mcp-gateway/crds
	@echo "Checking CRD synchronization..."
	@if [ ! -d "charts/mcp-gateway/crds" ]; then \
		echo "❌ Helm CRDs directory doesn't exist. Run 'make update-helm-crds'"; \
		exit 1; \
	fi
	@# Only compare actual CRD files, not kustomization.yaml
	@SYNC_ERROR=0; \
	for crd in config/crd/mcp.kagenti.com_*.yaml; do \
		crd_name=$$(basename "$$crd"); \
		if [ ! -f "charts/mcp-gateway/crds/$$crd_name" ]; then \
			echo "❌ Missing CRD in Helm chart: $$crd_name"; \
			SYNC_ERROR=1; \
		elif ! diff "$$crd" "charts/mcp-gateway/crds/$$crd_name" >/dev/null 2>&1; then \
			echo "❌ CRD differs: $$crd_name"; \
			SYNC_ERROR=1; \
		fi; \
	done; \
	if [ $$SYNC_ERROR -eq 1 ]; then \
		echo ""; \
		echo "Run 'make update-helm-crds' to sync, or 'make generate-crds-all' to regenerate and sync"; \
		exit 1; \
	else \
		echo "✅ CRDs are synchronized"; \
	fi

# Install CRD
install-crd: ## Install MCPServerRegistration and MCPVirtualServer CRDs
	kubectl apply -f config/crd/mcp.kagenti.com_mcpserverregistrations.yaml
	kubectl apply -f config/crd/mcp.kagenti.com_mcpvirtualservers.yaml
	kubectl apply -f config/crd/mcp.kagenti.com_mcpgatewayextensions.yaml

# Deploy mcp-gateway components
deploy: install-crd deploy-namespaces  deploy-controller deploy-broker ## Deploy broker/router and controller to mcp-system namespace


# Deploy only the broker/router
deploy-broker: install-crd ## Deploy only the broker/router (without controller)
	kubectl apply -k config/mcp-gateway/overlays/mcp-system/
	kubectl patch deployment mcp-broker-router -n mcp-system --patch-file config/mcp-gateway/overlays/mcp-system/poll-interval-patch.yaml

# Deploy a new gateway httproute and broker instance configured to work with the new gateway
deploy-gateway-instance-helm: install-crd ## Deploy only the broker/router (without controller)

	$(KUBECTL) create ns $(MCP_GATEWAY_NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
	$(HELM) install mcp-gateway ./charts/mcp-gateway \
  --namespace $(MCP_GATEWAY_NAMESPACE) \
  --set gateway.create=true \
  --set gateway.name=$(MCP_GATEWAY_NAME) \
  --set gateway.namespace=gateway-system \
  --set envoyFilter.create=true \
  --set controller.enabled=false \
  --set envoyFilter.namespace=istio-system \
  --set envoyFilter.name=$(MCP_GATEWAY_NAMESPACE) \
  --set broker.checkInterval=10\
  --set gateway.publicHost=$(MCP_GATEWAY_HOST) \
  --set httpRoute.create=true \
  --set gateway.nodePort.create=true \
  --set mcpGatewayExtension.gatewayRef.name=$(MCP_GATEWAY_NAME) \
  --set mcpGatewayExtension.gatewayRef.namespace=gateway-system

.PHONY: configure-redis
configure-redis:  ## patch deployment with redis connection
	kubectl apply -f config/mcp-gateway/overlays/mcp-system/redis-deployment.yaml
	kubectl apply -f config/mcp-gateway/overlays/mcp-system/redis-service.yaml
	kubectl patch deployment mcp-broker-router -n mcp-system --patch-file config/mcp-gateway/overlays/mcp-system/deployment-controller-redis-patch.yaml

# Deploy only the controller
deploy-controller: install-crd ## Deploy only the controller
	kubectl apply -k config/mcp-gateway/overlays/mcp-system/

define load-image
	echo "Loading image $(1) into Kind cluster..."
	$(eval TMP_DIR := $(shell mktemp -d))
	$(CONTAINER_ENGINE) save -o $(TMP_DIR)/image.tar $(1) \
	   && KIND_EXPERIMENTAL_PROVIDER=$(CONTAINER_ENGINE) $(KIND) load image-archive $(TMP_DIR)/image.tar --name $(KIND_CLUSTER_NAME) ; \
	   EXITVAL=$$? ; \
	   rm -rf $(TMP_DIR) ;\
	   exit $${EXITVAL}
endef

.PHONY: restart-all
restart-all:
	kubectl rollout restart deployment/mcp-broker-router -n mcp-system 2>/dev/null || true
	kubectl rollout restart deployment/mcp-controller -n mcp-system 2>/dev/null || true

.PHONY: build-and-load-image
build-and-load-image: kind build-image load-image restart-all  ## Build & load router/broker/controller image into the Kind cluster and restart
	@echo "Building and loading image into Kind cluster..."

.PHONY: load-image
load-image: kind ## Load the mcp-gateway image into the kind cluster
	$(call load-image,ghcr.io/kuadrant/mcp-gateway:latest)
	$(call load-image,ghcr.io/kuadrant/mcp-controller:latest)

.PHONY: build-image
build-image: kind ## Build the mcp-gateway image
	$(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) -t ghcr.io/kuadrant/mcp-gateway:latest .
	$(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) --file Dockerfile.controller -t ghcr.io/kuadrant/mcp-controller:latest .

# Deploy example MCPServerRegistration
deploy-example: install-crd ## Deploy example MCPServerRegistration resource
	@echo "Waiting for test servers to be ready..."
	@kubectl wait --for=condition=Available deployment -n mcp-test -l app=mcp-test-server1 --timeout=$(WAIT_TIME)
	@kubectl wait --for=condition=Available deployment -n mcp-test -l app=mcp-test-server2 --timeout=$(WAIT_TIME)
	@kubectl wait --for=condition=Available deployment -n mcp-test -l app=mcp-test-server3 --timeout=$(WAIT_TIME)
	@kubectl wait --for=condition=Available deployment -n mcp-test -l app=mcp-api-key-server --timeout=$(WAIT_TIME)
	@kubectl wait --for=condition=Available deployment -n mcp-test -l app=mcp-custom-path-server --timeout=$(WAIT_TIME) 2>/dev/null || true
	@kubectl wait --for=condition=Available deployment -n mcp-test -l app=mcp-oidc-server --timeout=$(WAIT_TIME)
	@kubectl wait --for=condition=Available deployment -n mcp-test -l app=everything-server --timeout=$(WAIT_TIME)
	@kubectl wait --for=condition=Available deployment -n mcp-test -l app=mcp-custom-response --timeout=$(WAIT_TIME)
	@echo "All test servers ready, deploying MCPServerRegistration resources..."
	kubectl apply -f config/samples/mcpserverregistration-test-servers-base.yaml
	kubectl apply -f config/samples/mcpserverregistration-test-servers-extended.yaml
	@echo "Waiting for controller to process MCPServerRegistration..."
	@sleep 3
	@echo "Restarting broker to ensure all connections..."
	kubectl rollout restart deployment/mcp-broker-router -n mcp-system
	@kubectl rollout status deployment/mcp-broker-router -n mcp-system --timeout=$(WAIT_TIME)

# Build test server Docker images
build-test-servers: ## Build test server Docker images locally
	@echo "Building test server images..."
	cd tests/servers/server1 && $(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) -t ghcr.io/kuadrant/mcp-gateway/test-server1:latest .
	cd tests/servers/server2 && $(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) -t ghcr.io/kuadrant/mcp-gateway/test-server2:latest .
	cd tests/servers/server3 && $(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) -t ghcr.io/kuadrant/mcp-gateway/test-server3:latest .
	cd tests/servers/api-key-server && $(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) -t ghcr.io/kuadrant/mcp-gateway/test-api-key-server:latest .
	cd tests/servers/broken-server && $(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) -t ghcr.io/kuadrant/mcp-gateway/test-broken-server:latest .
	cd tests/servers/custom-path-server && $(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) -t ghcr.io/kuadrant/mcp-gateway/test-custom-path-server:latest .
	cd tests/servers/oidc-server && $(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) -t ghcr.io/kuadrant/mcp-gateway/test-oidc-server:latest .
	cd tests/servers/everything-server && $(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) -t ghcr.io/kuadrant/mcp-gateway/test-everything-server:latest .
	cd tests/servers/custom-response-server && $(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) -t ghcr.io/kuadrant/mcp-gateway/test-custom-response-server:latest .	

# Build conformance server Docker image
.PHONY: build-conformance-server
build-conformance-server: ## Build conformance server Docker image locally
	@echo "Building conformance server image..."
	cd tests/servers/conformance-server && $(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) -t ghcr.io/kuadrant/mcp-gateway/test-conformance-server:latest .

# Load test server images into Kind cluster
kind-load-test-servers: kind build-test-servers ## Load test server images into Kind cluster
	@echo "Loading test server images into Kind cluster..."
	$(call load-image,ghcr.io/kuadrant/mcp-gateway/test-server1:latest)
	$(call load-image,ghcr.io/kuadrant/mcp-gateway/test-server2:latest)
	$(call load-image,ghcr.io/kuadrant/mcp-gateway/test-server3:latest)
	$(call load-image,ghcr.io/kuadrant/mcp-gateway/test-api-key-server:latest)
	$(call load-image,ghcr.io/kuadrant/mcp-gateway/test-broken-server:latest)
	$(call load-image,ghcr.io/kuadrant/mcp-gateway/test-custom-path-server:latest)
	$(call load-image,ghcr.io/kuadrant/mcp-gateway/test-oidc-server:latest)
	$(call load-image,ghcr.io/kuadrant/mcp-gateway/test-everything-server:latest)
	$(call load-image,ghcr.io/kuadrant/mcp-gateway/test-custom-response-server:latest)

# Load conformance server image into Kind cluster
.PHONY: kind-load-conformance-server
kind-load-conformance-server: kind build-conformance-server ## Load conformance server image into Kind cluster
	@echo "Loading conformance server image into Kind cluster..."
	$(call load-image,ghcr.io/kuadrant/mcp-gateway/test-conformance-server:latest)

# Deploy test servers
deploy-test-servers: kind-load-test-servers ## Deploy test MCP servers for local testing
	@echo "Deploying test MCP servers..."
	kubectl apply -k config/test-servers/
	@echo "Patching OIDC-enabled MCP server to be able to connect to Keycloak..."
	@kubectl create configmap mcp-gateway-keycloak-cert -n mcp-test --from-file=keycloak.crt=./out/certs/ca.crt 2>/dev/null || true
	@kubectl wait --for=condition=Programmed gateway/mcp-gateway -n gateway-system --timeout=${WAIT_TIME}
	@export GATEWAY_ADDRESS=$$(kubectl get gateway/mcp-gateway -n gateway-system -o jsonpath='{.status.addresses[0].value}'); \
	  export GATEWAY_IP=$$(./utils/resolve_ip.sh $$GATEWAY_ADDRESS); \
	  if [ -z "$$GATEWAY_IP" ]; then exit 1; fi; \
	  echo "Gateway IP: $$GATEWAY_IP"; \
	  kubectl patch deployment mcp-oidc-server -n mcp-test --type='json' -p="$$(cat config/keycloak/patch-hostaliases.json | envsubst)"

# Deploy conformance server
.PHONY: deploy-conformance-server
deploy-conformance-server: kind-load-conformance-server ## Deploy conformance MCP server
	@echo "Deploying conformance MCP server..."
	kubectl apply -k config/test-servers/conformance-server/
	@echo "Waiting for conformance server to be ready..."
	@kubectl wait --for=condition=Available deployment -n mcp-test -l app=conformance-server --timeout=60s
	@echo "Conformance server ready, deploying MCPServerRegistration resource..."
	kubectl apply -f config/samples/mcpserverregistration-conformance-server.yaml
	@echo "Waiting for MCPServerRegistration to be Ready..."
	@kubectl wait --for=condition=Ready mcpsr/conformance-server -n mcp-test --timeout=120s

# Build and push container image TODO we have this and build-image lets just use one
docker-build: ## Build container image locally
	$(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) -t ghcr.io/kuadrant/mcp-gateway:latest .
	$(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) --file Dockerfile.controller -t ghcr.io/kuadrant/mcp-controller:latest .

# Common reload steps
define reload-image
	@docker tag mcp-gateway:local ghcr.io/kuadrant/mcp-gateway:latest
	@$(call load-image,ghcr.io/kuadrant/mcp-gateway:latest)
endef

.PHONY: reload-controller
reload-controller: build kind ## Build, load to Kind, and restart controller
	$(CONTAINER_ENGINE) build $(CONTAINER_ENGINE_EXTRA_FLAGS) --file Dockerfile.controller -t ghcr.io/kuadrant/mcp-controller:latest .	
	$(call load-image,ghcr.io/kuadrant/mcp-controller:latest)	
	@kubectl rollout restart -n mcp-system deployment/mcp-controller
	@kubectl rollout status -n mcp-system deployment/mcp-controller --timeout=60s

.PHONY: reload-broker
reload-broker: build docker-build kind ## Build, load to Kind, and restart broker
	$(call reload-image)
	@kubectl rollout restart -n mcp-system deployment/mcp-broker-router
	@kubectl rollout status -n mcp-system deployment/mcp-broker-router --timeout=60s

.PHONY: reload
reload: build docker-build kind ## Build, load to Kind, and restart both controller and broker
	$(call reload-image)
	@kubectl rollout restart -n mcp-system deployment/mcp-controller deployment/mcp-broker-router
	@kubectl rollout status -n mcp-system deployment/mcp-controller --timeout=60s
	@kubectl rollout status -n mcp-system deployment/mcp-broker-router --timeout=60s

##@ E2E Testing

# E2E test targets are in build/e2e.mk

# Build multi-platform image
docker-buildx: ## Build multi-platform container image
	$(CONTAINER_ENGINE) buildx build --platform linux/amd64,linux/arm64 $(CONTAINER_ENGINE_EXTRA_FLAGS) -t mcp-gateway:local .

# Download dependencies
deps:
	go mod download

# Update dependencies
update:
	go mod tidy
	go get -u ./...

# Lint

.PHONY: fmt
fmt:
	find . -name '*.go' ! -name '*deepcopy*.go' ! -path './vendor/*' -print0 | xargs -0 goimports -w

.PHONY: vet
vet:
	go vet $$(go list ./... | grep -v /zz_generated)

.PHONY: golangci-lint
golangci-lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	elif [ -f bin/golangci-lint ]; then \
		bin/golangci-lint run ./...; \
	else \
		"$(MAKE)" golangci-lint-bin && bin/golangci-lint run ./...; \
	fi

# To install cspell, do `npm install -g cspell@latest`.
# If this reports "Unknown word" for valid spellings, do
# `cspell --words-only --unique . | sort --ignore-case >> project-words.txt`
# to add new words to the list.
.PHONY: spell
spell:
	cspell --quiet .

.PHONY: lint
lint: check-gofmt check-goimports check-newlines fmt vet golangci-lint spell ## Run all linting and style checks
	@echo "All lint checks passed!"

# Code style checks
.PHONY: check-style
check-style: check-gofmt check-goimports check-newlines

.PHONY: check-gofmt
check-gofmt:
	@echo "Checking gofmt..."
	@if [ -n "$$(gofmt -s -l . | grep -v '^vendor/' | grep -v '\.deepcopy\.go')" ]; then \
		echo "Files need gofmt -s:"; \
		gofmt -s -l . | grep -v '^vendor/' | grep -v '\.deepcopy\.go'; \
		echo "Run 'make fmt' to fix."; \
		exit 1; \
	fi

.PHONY: check-goimports
check-goimports:
	@echo "Checking goimports..."
	@if [ -n "$$(goimports -l . | grep -v '^vendor/' | grep -v '\.deepcopy\.go')" ]; then \
		echo "Files need goimports:"; \
		goimports -l . | grep -v '^vendor/' | grep -v '\.deepcopy\.go'; \
		echo "Run 'make fmt' to fix."; \
		exit 1; \
	fi

.PHONY: check-newlines
check-newlines:
	@set -e; \
	echo "Checking for missing EOF newlines..."; \
	LINT_FILES=$$(git ls-files | \
		git check-attr --stdin linguist-generated | grep -Ev ': (set|true)$$' | cut -d: -f1 | \
		git check-attr --stdin linguist-vendored  | grep -Ev ': (set|true)$$' | cut -d: -f1 | \
		grep -Ev '^(third_party/|.github|docs/)' | \
		grep -v '\.ai$$' | \
		grep -v '\.svg$$'); \
	FAIL=0; \
	for x in $$LINT_FILES; do \
		if [ -f "$$x" ]; then \
			if [ -s "$$x" ] && [ -n "$$(tail -c 1 "$$x")" ]; then \
				echo "Missing newline at end of file: $$x"; \
				echo "Try fixing with 'make fix-newlines'"; \
				FAIL=1; \
			fi; \
		fi; \
	done; \
	exit $$FAIL

.PHONY: fix-newlines
fix-newlines:
	@echo "Fixing missing EOF newlines..."
	@LINT_FILES=$$(git ls-files | \
		git check-attr --stdin linguist-generated | grep -Ev ': (set|true)$$' | cut -d: -f1 | \
		git check-attr --stdin linguist-vendored  | grep -Ev ': (set|true)$$' | cut -d: -f1 | \
		grep -Ev '^(third_party/|.github|docs/)' | \
		grep -v '\.ai$$' | \
		grep -v '\.svg$$'); \
	for x in $$LINT_FILES; do \
		if [ -f "$$x" ]; then \
			if [ -s "$$x" ] && [ -n "$$(tail -c 1 "$$x")" ]; then \
				echo "" >> "$$x"; \
				echo "Fixed: $$x"; \
			fi; \
		fi; \
	done


test-unit:
	go test -v -race ./...

.PHONY: test-controller-integration
test-controller-integration: envtest gateway-api-crds ## Run controller integration tests
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" $(GINKGO) $(GINKGO_FLAGS) -tags=integration ./internal/controller
  

.PHONY: envtest
envtest: $(ENVTEST) ## Download envtest-setup locally if necessary.
$(ENVTEST): $(LOCALBIN)
	test -s $(LOCALBIN)/setup-envtest || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: gateway-api-crds
gateway-api-crds: ## Download Gateway API CRDs for integration tests
	@mkdir -p config/crd/gateway-api
	@if [ ! -f config/crd/gateway-api/standard.yaml ]; then \
		echo "Downloading Gateway API CRDs $(GATEWAY_API_VERSION)..."; \
		curl -sL https://github.com/kubernetes-sigs/gateway-api/releases/download/$(GATEWAY_API_VERSION)/standard-install.yaml -o config/crd/gateway-api/standard.yaml; \
	fi

.PHONY: tools
tools: ## Install all required tools (kind, helm, kustomize, yq, istioctl, controller-gen) to ./bin/
	@echo "Checking and installing required tools to ./bin/ ..."
	@if [ -f bin/kind ]; then echo "[OK] kind already installed"; else echo "Installing kind..."; "$(MAKE)" -s kind; fi
	@if [ -f bin/helm ]; then echo "[OK] helm already installed"; else echo "Installing helm..."; "$(MAKE)" -s helm; fi
	@if [ -f bin/kustomize ]; then echo "[OK] kustomize already installed"; else echo "Installing kustomize..."; "$(MAKE)" -s kustomize; fi
	@if [ -f bin/yq ]; then echo "[OK] yq already installed"; else echo "Installing yq..."; "$(MAKE)" -s yq; fi
	@if [ -f bin/istioctl ]; then echo "[OK] istioctl already installed"; else echo "Installing istioctl..."; "$(MAKE)" -s istioctl; fi
	@if [ -f bin/controller-gen ]; then echo "[OK] controller-gen already installed"; else echo "Installing controller-gen..."; "$(MAKE)" -s controller-gen; fi
	@echo "All tools ready!"

.PHONY: local-env-setup
local-env-setup: ## Setup complete local demo environment with Kind, Istio, MCP Gateway, and test servers
	@echo "========================================="
	@echo "Starting MCP Gateway Environment Setup"
	@echo "========================================="
	"$(MAKE)" tools
	"$(MAKE)" kind-create-cluster
	"$(MAKE)" build-and-load-image
	"$(MAKE)" gateway-api-install
	"$(MAKE)" istio-install
	"$(MAKE)" metallb-install
	"$(MAKE)" deploy-namespaces
	"$(MAKE)" deploy-gateway
	"$(MAKE)" deploy
	"$(MAKE)" add-jwt-key	
	"$(MAKE)" deploy-test-servers
	"$(MAKE)" deploy-example

.PHONY: local-bare-setup
local-bare-setup: ## Setup complete local demo environment with Kind, Istio, MCP Gateway, and test servers
	@echo "========================================="
	@echo "Starting MCP Gateway Environment Setup"
	@echo "========================================="
	"$(MAKE)" tools
	"$(MAKE)" kind-create-cluster
	"$(MAKE)" build-and-load-image
	"$(MAKE)" gateway-api-install
	"$(MAKE)" istio-install
	"$(MAKE)" metallb-install
	"$(MAKE)" deploy-namespaces

.PHONY: local-env-teardown
local-env-teardown: ## Tear down the local Kind cluster
	"$(MAKE)" kind-delete-cluster


.PHONY: add-jwt-key
add-jwt-key: #add the public key needed to validate any incoming jwt based headers such as x-allowed-tools
	@for i in 1 2 3 4 5; do \
		kubectl get deployment/mcp-broker-router -n mcp-system -o yaml | \
		bin/yq '.spec.template.spec.containers[0].env += [{"name":"TRUSTED_HEADER_PUBLIC_KEY","valueFrom":{"secretKeyRef":{"name":"trusted-headers-public-key","key":"key"}}}] | .spec.template.spec.containers[0].env |= unique_by(.name)' | \
		kubectl apply -f - && break || (echo "Retry $$i/5 failed, waiting 2s..." && sleep 2); \
	done

.PHONY: dev
dev: ## Setup cluster for local development (binaries run on host)
	"$(MAKE)" dev-setup
	@echo ""
	@echo "Ready for local development! Run these in separate terminals:"
	@echo "  1. make run-mcp-broker-router"
	@echo "  2. make dev-gateway-forward"
	@echo ""
	@echo "Then test with: make dev-test"

##@ Getting Started

.PHONY: info
info: ## Show quick setup info and useful commands
	@"$(MAKE)" -s -f build/info.mk info-impl

##@ Inspection

.PHONY: urls
urls: ## Show all available service URLs
	@"$(MAKE)" -s -f build/inspect.mk urls-impl

.PHONY: status
status: ## Show status of all MCP components
	@"$(MAKE)" -s -f build/inspect.mk status-impl


##@ Tools

.PHONY: istioctl
istioctl: ## Download and install istioctl
	@"$(MAKE)" -s -f build/istio.mk istioctl-impl

.PHONY: cert-manager-install
cert-manager-install: ## Install cert-manager for TLS certificate management
	@echo "Installing Cert-manager"
	@"$(MAKE)" -s -f build/cert-manager.mk cert-manager-install-impl

.PHONY: keycloak-install
keycloak-install: ## Install Keycloak IdP for development
	@echo "Installing Keycloak - using official image with dev-file database"
	@"$(MAKE)" -s -f build/keycloak.mk keycloak-install-impl

.PHONY: keycloak-status
keycloak-status: ## Show Keycloak URLs, credentials, and OIDC endpoints
	@"$(MAKE)" -s -f build/keycloak.mk keycloak-status-impl

.PHONY: kuadrant-install
kuadrant-install: ## Install Kuadrant operator for API gateway policies
	@"$(MAKE)" -s -f build/kuadrant.mk kuadrant-install-impl

.PHONY: kuadrant-status
kuadrant-status: ## Show Kuadrant operator status and available CRDs
	@"$(MAKE)" -s -f build/kuadrant.mk kuadrant-status-impl

.PHONY: kuadrant-configure
kuadrant-configure: ## Apply Kuadrant configuration from config/kuadrant
	@"$(MAKE)" -s -f build/kuadrant.mk kuadrant-configure-impl

##@ Debug

.PHONY: debug-envoy
debug-envoy: ## Enable debug logging for Istio gateway
	@"$(MAKE)" -s -f build/debug.mk debug-envoy-impl

.PHONY: istio-clusters
istio-clusters: ## Show all registered clusters in the gateway
	@"$(MAKE)" -s -f build/istio-debug.mk istio-clusters-impl

.PHONY: istio-config
istio-config: ## Show all proxy configurations
	@"$(MAKE)" -s -f build/istio-debug.mk istio-config-impl

.PHONY: debug-envoy-off
debug-envoy-off: ## Disable debug logging for Istio gateway
	@"$(MAKE)" -s -f build/debug.mk debug-envoy-off-impl

.PHONY: logs
logs: ## Tail Istio gateway logs
	@"$(MAKE)" -s -f build/debug.mk debug-logs-gateway-impl

-include build/*.mk

.PHONY: testwithcoverage
testwithcoverage:
	go test -race ./... -coverprofile=coverage.out

.PHONY: coverage
coverage: testwithcoverage
	@echo "test coverage: $(shell go tool cover -func coverage.out | grep total | awk '{print substr($$3, 1, length($$3)-1)}')"

.PHONY: htmlcov
htmlcov: coverage
	go tool cover -html=coverage.out
