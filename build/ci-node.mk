# Baked CI node image: kindest/node pre-seeded with the e2e infra and
# test-server images (see build/ci-node/Dockerfile). the tag is a content
# hash of everything that determines the baked image, computed by
# utils/ci-node-image-hash.sh and shared between the bake workflow and the
# e2e consumers. this file is itself a hash input, so changes here rebake.

CI_NODE_IMAGE ?= ghcr.io/kuadrant/mcp-gateway/ci-node
CI_NODE_DOCKERFILE = build/ci-node/Dockerfile

# Pre-built test server images published to ghcr.io by
# .github/workflows/test-images.yaml. defined here rather than in the root
# Makefile (whose kind-pull targets reference them) so retagging or renaming
# a published image changes the baked image hash and forces a rebake.
TEST_SERVER_IMAGE_REPO ?= ghcr.io/kuadrant/mcp-gateway
TEST_SERVER_IMAGE_TAG ?= latest
TEST_SERVER_IMAGES = test-server1 test-server2 test-server3 test-api-key-server \
	test-broken-server test-custom-path-server test-oidc-server \
	test-everything-server test-custom-response-server test-user-specific-server \
	test-a2a-server

# buildx builder with the security.insecure entitlement: containerd's layer
# extraction in the bake RUN step needs mount(2), which plain builds deny
CI_NODE_BUILDER = mcp-ci-node-baker
# true pushes straight from the builder (CI), false loads into the local
# docker image store
CI_NODE_IMAGE_PUSH ?= false

.PHONY: print-ci-node-image-tag
print-ci-node-image-tag: # print the content-hash tag for the baked CI node image
	@./utils/ci-node-image-hash.sh

.PHONY: print-ci-node-image
print-ci-node-image: # print the fully qualified baked CI node image ref
	@echo "$(CI_NODE_IMAGE):$$(./utils/ci-node-image-hash.sh)"

# istiod/proxyv2 track the Istio CR (spec.version), redis tracks its
# deployment manifest, everything else tracks the pinned make variables, so
# versions have a single source of truth. image repos are pinned here:
# istiod/proxyv2 repos come from the sail operator's version mapping, the
# sail operator repo from its helm chart values, metallb and cert-manager
# repos from their pinned install manifests. requires docker buildx.
.PHONY: ci-node-image-build
ci-node-image-build: ## Bake e2e infra and test server images into a kind node image
	@set -e; \
	istio_version="$$(awk '$$1 == "version:" { print $$2; exit }' config/istio/istio.yaml)"; \
	test -n "$$istio_version" || { echo "ERROR: could not read spec.version from config/istio/istio.yaml"; exit 1; }; \
	redis_image="$$(awk '$$1 == "image:" { print $$2; exit }' config/mcp-gateway/overlays/mcp-system/redis-deployment.yaml)"; \
	test -n "$$redis_image" || { echo "ERROR: could not read image from config/mcp-gateway/overlays/mcp-system/redis-deployment.yaml"; exit 1; }; \
	refs="gcr.io/istio-release/pilot:$${istio_version#v} \
		gcr.io/istio-release/proxyv2:$${istio_version#v} \
		quay.io/sail-dev/sail-operator:$(SAIL_VERSION) \
		quay.io/jetstack/cert-manager-controller:v$(CERT_MANAGER_VERSION) \
		quay.io/jetstack/cert-manager-cainjector:v$(CERT_MANAGER_VERSION) \
		quay.io/jetstack/cert-manager-webhook:v$(CERT_MANAGER_VERSION) \
		quay.io/metallb/controller:$(METALLB_VERSION) \
		quay.io/metallb/speaker:$(METALLB_VERSION) \
		$$redis_image"; \
	for img in $(TEST_SERVER_IMAGES) test-tls-server; do \
		refs="$$refs $(TEST_SERVER_IMAGE_REPO)/$$img:$(TEST_SERVER_IMAGE_TAG)"; \
	done; \
	tag="$$(./utils/ci-node-image-hash.sh)"; \
	if [ "$(CI_NODE_IMAGE_PUSH)" = "true" ]; then output_flag="--push"; else output_flag="--load"; fi; \
	$(CONTAINER_ENGINE) buildx inspect $(CI_NODE_BUILDER) >/dev/null 2>&1 \
		|| $(CONTAINER_ENGINE) buildx create --name $(CI_NODE_BUILDER) --driver docker-container \
			--buildkitd-flags '--allow-insecure-entitlement security.insecure'; \
	echo "Baking $(CI_NODE_IMAGE):$$tag ($$output_flag)"; \
	$(CONTAINER_ENGINE) buildx build --builder $(CI_NODE_BUILDER) --allow security.insecure $$output_flag \
		-f $(CI_NODE_DOCKERFILE) \
		--build-arg KIND_NODE_IMAGE="$(KIND_NODE_IMAGE)" \
		--build-arg BAKED_IMAGE_REFS="$$refs" \
		-t "$(CI_NODE_IMAGE):$$tag" \
		build/ci-node
