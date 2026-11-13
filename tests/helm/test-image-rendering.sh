#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
HELM="${HELM:-${ROOT_DIR}/bin/helm}"
CHART_DIR="${ROOT_DIR}/charts/mcp-gateway"

if [[ ! -x "$HELM" ]]; then
    HELM="$(command -v helm)"
fi

app_version="$("$HELM" show chart "$CHART_DIR" | awk '$1 == "appVersion:" { gsub(/"/, "", $2); print $2 }')"
default_deployment="$("$HELM" template test-release "$CHART_DIR" \
    --show-only templates/deployment-controller.yaml)"
default_controller="$(awk '$1 == "image:" { print $2 }' <<< "$default_deployment")"
default_broker="$(awk '$3 == "RELATED_IMAGE_ROUTER_BROKER" { getline; print $2 }' <<< "$default_deployment")"
[[ "$default_controller" == "quay.io/kuadrant/mcp-controller:${app_version}" ]] || {
    echo "unexpected default controller image: $default_controller" >&2
    exit 1
}
[[ "$default_broker" == "quay.io/kuadrant/mcp-gateway:${app_version}" ]] || {
    echo "unexpected default broker image: $default_broker" >&2
    exit 1
}

render_image() {
    "$HELM" template test-release "$CHART_DIR" \
        --set controller.enabled=true \
        --set mcpGatewayExtension.create=false \
        --set-string imageController.repository="$1" \
        --set-string imageController.tag="$2" \
        | awk '$1 == "image:" { print $2; exit }'
}

digest_image="quay.io/kuadrant/mcp-controller@sha256:abc123"
actual_digest_image="$(render_image "$digest_image" "")"
[[ "$actual_digest_image" == "$digest_image" ]] || {
    echo "digest image rendered as $actual_digest_image, want $digest_image" >&2
    exit 1
}

tagged_image="quay.io/kuadrant/mcp-controller"
actual_tagged_image="$(render_image "$tagged_image" "v0.9.0")"
expected_tagged_image="${tagged_image}:v0.9.0"
[[ "$actual_tagged_image" == "$expected_tagged_image" ]] || {
    echo "tagged image rendered as $actual_tagged_image, want $expected_tagged_image" >&2
    exit 1
}

echo "PASS: image references render correctly"
