#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/../.." && pwd)"
HELM="${HELM:-${ROOT_DIR}/bin/helm}"
YQ="${YQ:-${ROOT_DIR}/bin/yq}"
CHART_DIR="${ROOT_DIR}/charts/mcp-gateway"
CRD_DIR="${ROOT_DIR}/config/crd"
CHART_CRD_DIR="${CHART_DIR}/crds"

if [[ ! -x "$HELM" ]]; then
    HELM="$(command -v helm)"
fi
if [[ ! -x "$YQ" ]]; then
    YQ="$(command -v yq)"
fi

render() {
    "$HELM" template "$1" "$CHART_DIR" "${@:2}"
}

assert_eq() {
    local actual="$1"
    local expected="$2"
    local message="$3"
    if [[ "$actual" != "$expected" ]]; then
        echo "FAIL: $message: got '$actual', want '$expected'" >&2
        exit 1
    fi
}

assert_crd_spec_required() {
    local crd_dir="$1"
    local crd="$2"
    local version="$3"
    local required
    required=$("$YQ" eval \
        ".spec.versions[] | select(.name == \"$version\") | (.schema.openAPIV3Schema.required // []) | contains([\"spec\"])" \
        "$crd_dir/$crd")
    assert_eq "$required" "true" "$crd_dir/$crd $version requires spec"
}

for crd_dir in "$CRD_DIR" "$CHART_CRD_DIR"; do
    for crd in \
        mcp.kuadrant.io_mcpserverregistrations.yaml \
        mcp.kuadrant.io_mcpvirtualservers.yaml; do
        assert_crd_spec_required "$crd_dir" "$crd" v1
        assert_crd_spec_required "$crd_dir" "$crd" v1alpha1
    done
done

rendered=$(render test-release \
    --namespace source-one \
    --set controller.enabled=true \
    --set gateway.create=false \
    --set mcpGatewayExtension.gatewayRef.name=shared-gateway \
    --set mcpGatewayExtension.gatewayRef.namespace=shared-system \
    --set mcpGatewayExtension.gatewayRef.sectionName=mcp)

private_host=$(printf '%s\n' "$rendered" | "$YQ" eval 'select(.kind == "MCPGatewayExtension") | .spec.privateHost // ""' -)
assert_eq "$private_host" "" "privateHost is omitted by default"

deployment_security=$(printf '%s\n' "$rendered" | "$YQ" eval 'select(.kind == "Deployment") | .spec.template.spec.securityContext.runAsNonRoot' -)
assert_eq "$deployment_security" "true" "controller runs as non-root"

seccomp_profile=$(printf '%s\n' "$rendered" | "$YQ" eval 'select(.kind == "Deployment") | .spec.template.spec.securityContext.seccompProfile.type' -)
assert_eq "$seccomp_profile" "RuntimeDefault" "controller uses the runtime seccomp profile"

container_security=$(printf '%s\n' "$rendered" | "$YQ" eval 'select(.kind == "Deployment") | .spec.template.spec.containers[0].securityContext.allowPrivilegeEscalation' -)
assert_eq "$container_security" "false" "controller disallows privilege escalation"

root_filesystem=$(printf '%s\n' "$rendered" | "$YQ" eval 'select(.kind == "Deployment") | .spec.template.spec.containers[0].securityContext.readOnlyRootFilesystem' -)
assert_eq "$root_filesystem" "true" "controller uses a read-only root filesystem"

capabilities=$(printf '%s\n' "$rendered" | "$YQ" eval 'select(.kind == "Deployment") | .spec.template.spec.containers[0].securityContext.capabilities.drop[0]' -)
assert_eq "$capabilities" "ALL" "controller drops all capabilities"

grant_name=$(printf '%s\n' "$rendered" | "$YQ" eval 'select(.kind == "ReferenceGrant") | .metadata.name' -)
[[ "$grant_name" == allow-source-one-* ]] || {
    echo "FAIL: ReferenceGrant name does not include the source namespace: $grant_name" >&2
    exit 1
}
[[ ${#grant_name} -le 253 ]] || {
    echo "FAIL: ReferenceGrant name exceeds 253 characters: $grant_name" >&2
    exit 1
}
[[ "$grant_name" != *[.-] ]] || {
    echo "FAIL: ReferenceGrant name ends with an invalid separator: $grant_name" >&2
    exit 1
}

grant_target=$(printf '%s\n' "$rendered" | "$YQ" eval 'select(.kind == "ReferenceGrant") | .spec.to[0].name' -)
assert_eq "$grant_target" "shared-gateway" "ReferenceGrant targets only the configured Gateway"

explicit_private_host=$(render test-release \
    --set controller.enabled=false \
    --set-string mcpGatewayExtension.privateHost=https://custom-gateway:443 \
    | "$YQ" eval 'select(.kind == "MCPGatewayExtension") | .spec.privateHost' -)
assert_eq "$explicit_private_host" "https://custom-gateway:443" "explicit privateHost override is preserved"

echo "PASS: chart and CRD rendering checks passed"
