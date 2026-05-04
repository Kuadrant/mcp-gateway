# Kind

ARCH ?= $(shell uname -m)
KIND_CLUSTER_NAME ?= mcp-gateway

# node image for CI clusters; the baked CI node image overrides this on a
# bake hit. defaults to the pinned KIND_NODE_IMAGE so it always matches the
# bin/kind the load targets use.
KIND_CLUSTER_IMAGE ?= $(KIND_NODE_IMAGE)

# CI cluster creation uses the same pinned kind binary and node image as the
# make load targets. creating with the runner-preinstalled kind broke when
# its default node image moved to a containerd config version the pinned
# bin/kind cannot load into (kindest/node v1.36.1 ships containerd config
# version 4; kind v0.29.0 supports 2 and 3).
.PHONY: kind-create-cluster-ci
kind-create-cluster-ci: kind # Create the CI kind cluster with the pinned kind binary and node image
	$(KIND) create cluster --name $(KIND_CLUSTER_NAME) --config config/kind/cluster-ci.yaml --image "$(KIND_CLUSTER_IMAGE)"
# Match the Kubernetes version your repo expects for kind
KIND_K8S_VERSION ?= v1.33.1

# Default upstream kind node image name for supported arches
KIND_NODE_IMAGE ?= kindest/node:$(KIND_K8S_VERSION)

# Custom node image name for ppc64le
ifeq ($(ARCH),ppc64le)
	KIND_NODE_IMAGE_PPC64LE := quay.io/powercloud/kind-node:$(KIND_K8S_VERSION)
else
	KIND_NODE_IMAGE_PPC64LE := docker.io/kindest/node:$(KIND_K8S_VERSION)
endif

.PHONY: kind-node-image
kind-node-image: kind ## Build custom kind node image for ppc64le when needed
	@if [ "$(ARCH)" = "ppc64le" ]; then \
		echo "Ensuring custom Kind node image exists for $(ARCH)..."; \
		if ! $(CONTAINER_ENGINE) image inspect "$(KIND_NODE_IMAGE_PPC64LE)" >/dev/null 2>&1; then \
			echo "Building $(KIND_NODE_IMAGE_PPC64LE) from Kubernetes release $(KIND_K8S_VERSION)..."; \
			KIND_EXPERIMENTAL_PROVIDER=$(CONTAINER_ENGINE) $(KIND) build node-image \
				--image "$(KIND_NODE_IMAGE_PPC64LE)" \
				--type release \
				"$(KIND_K8S_VERSION)"; \
		else \
			echo "[OK] Custom Kind node image already exists: $(KIND_NODE_IMAGE_PPC64LE)"; \
		fi; \
	else \
		echo "[OK] Default kind node image will be used for $(ARCH)"; \
	fi

.PHONY: kind-create-cluster
kind-create-cluster: kind kind-node-image ## Create the "mcp-gateway" kind cluster.
	@./utils/generate-placeholder-ca.sh
	@# Set KIND provider for podman
	@if echo "$(CONTAINER_ENGINE)" | grep -q "podman"; then \
		export KIND_EXPERIMENTAL_PROVIDER=podman; \
	fi; \
	NODE_IMAGE="$(KIND_NODE_IMAGE)"; \
	if [ "$(ARCH)" = "ppc64le" ]; then \
		NODE_IMAGE="$(KIND_NODE_IMAGE_PPC64LE)"; \
	fi; \
	if $(KIND) get clusters | grep -q "^$(KIND_CLUSTER_NAME)$$"; then \
		echo "Kind cluster '$(KIND_CLUSTER_NAME)' already exists, skipping creation"; \
	else \
		echo "Creating Kind cluster '$(KIND_CLUSTER_NAME)' with image $$NODE_IMAGE ..."; \
		cat config/kind/cluster.yaml | sed \
			-e 's/hostPort: 8001/hostPort: $(KIND_HOST_PORT_MCP_GATEWAY)/' \
			-e 's/hostPort: 8002/hostPort: $(KIND_HOST_PORT_KEYCLOAK)/' | \
		$(KIND) create cluster --name $(KIND_CLUSTER_NAME) --image "$$NODE_IMAGE" --config -; \
	fi

.PHONY: kind-delete-cluster
kind-delete-cluster: kind ## Delete the "mcp-gateway" kind cluster.
	@# Set KIND provider for podman
	@if echo "$(CONTAINER_ENGINE)" | grep -q "podman"; then \
		export KIND_EXPERIMENTAL_PROVIDER=podman; \
	fi; \
	$(KIND) delete cluster --name $(KIND_CLUSTER_NAME)
