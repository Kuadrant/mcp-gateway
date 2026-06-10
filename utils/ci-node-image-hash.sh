#!/usr/bin/env bash
# prints the content-hash tag for the baked CI node image. single
# implementation shared by .github/workflows/ci-node-image.yaml (producer)
# and the e2e workflows (consumers) so the two can never drift.
set -euo pipefail
cd "$(dirname "$0")/.."

# everything that determines the baked image content: the version-pinning make
# files, the manifests whose image refs are baked (the Istio CR pins the
# istiod/proxyv2 version, the redis deployment pins the redis image), the
# test-server sources their published images are built from (same paths
# test-images.yaml triggers on), and the bake implementation itself
fixed_inputs=(
	build/istio.mk
	build/cert-manager.mk
	build/metallb.mk
	build/gateway-api.mk
	build/tools.mk
	build/ci-node.mk
	build/ci-node/Dockerfile
	config/istio/istio.yaml
	config/mcp-gateway/overlays/mcp-system/redis-deployment.yaml
	utils/ci-node-image-hash.sh
)

if command -v sha256sum >/dev/null 2>&1; then
	sha() { sha256sum "$@"; }
else
	sha() { shasum -a 256 "$@"; }
fi

{
	printf '%s\n' "${fixed_inputs[@]}"
	git ls-files tests/servers internal/tests
} | LC_ALL=C sort -u | while IFS= read -r f; do sha "$f"; done | sha | cut -c1-12
