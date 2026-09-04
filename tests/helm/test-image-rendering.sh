#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
HELM="${HELM:-${ROOT_DIR}/bin/helm}"
CHART_DIR="${ROOT_DIR}/charts/mcp-gateway"

if [[ ! -x "$HELM" ]]; then
    HELM="$(command -v helm)"
fi

render_image() {
    "$HELM" template test-release "$CHART_DIR" \
        --set controller.enabled=true \
        --set mcpGatewayExtension.create=false \
        --set-string imageController.repository="$1" \
        --set-string imageController.tag="$2" \
        | awk '$1 == "image:" { print $2; exit }'
}

digest_image="ghcr.io/kuadrant/mcp-controller@sha256:abc123"
actual_digest_image="$(render_image "$digest_image" "")"
[[ "$actual_digest_image" == "$digest_image" ]] || {
    echo "digest image rendered as $actual_digest_image, want $digest_image" >&2
    exit 1
}

tagged_image="ghcr.io/kuadrant/mcp-controller"
actual_tagged_image="$(render_image "$tagged_image" "v0.9.0")"
expected_tagged_image="${tagged_image}:v0.9.0"
[[ "$actual_tagged_image" == "$expected_tagged_image" ]] || {
    echo "tagged image rendered as $actual_tagged_image, want $expected_tagged_image" >&2
    exit 1
}

echo "PASS: image references render correctly"
