# Kind

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

.PHONY: kind-create-cluster
kind-create-cluster: kind # Create the "mcp-gateway" kind cluster.
	@./utils/generate-placeholder-ca.sh
	@# Set KIND provider for podman
	@if echo "$(CONTAINER_ENGINE)" | grep -q "podman"; then \
		export KIND_EXPERIMENTAL_PROVIDER=podman; \
	fi; \
	if $(KIND) get clusters | grep -q "^$(KIND_CLUSTER_NAME)$$"; then \
		echo "Kind cluster '$(KIND_CLUSTER_NAME)' already exists, skipping creation"; \
	else \
		echo "Creating Kind cluster '$(KIND_CLUSTER_NAME)' with MCP_GATEWAY port $(KIND_HOST_PORT_MCP_GATEWAY) and KEYCLOAK port $(KIND_HOST_PORT_KEYCLOAK)..."; \
		cat config/kind/cluster.yaml | sed \
			-e 's/hostPort: 8001/hostPort: $(KIND_HOST_PORT_MCP_GATEWAY)/' \
			-e 's/hostPort: 8002/hostPort: $(KIND_HOST_PORT_KEYCLOAK)/' | \
		$(KIND) create cluster --name $(KIND_CLUSTER_NAME) --config -; \
	fi

.PHONY: kind-delete-cluster
kind-delete-cluster: kind # Delete the "mcp-gateway" kind cluster.
	@# Set KIND provider for podman
	@if echo "$(CONTAINER_ENGINE)" | grep -q "podman"; then \
		export KIND_EXPERIMENTAL_PROVIDER=podman; \
	fi; \
	$(KIND) delete cluster --name $(KIND_CLUSTER_NAME)
