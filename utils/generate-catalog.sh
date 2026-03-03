#!/usr/bin/env bash

# generate file-based catalog (FBC) content for OLM
# usage: generate-catalog.sh <opm> <yq> <bundle_img> <channel> <catalog_file>

set -euo pipefail

OPM=$1
YQ=$2
BUNDLE_IMG=$3
CHANNEL=$4
CATALOG_FILE=$5

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
CHANNEL_ENTRY_FILE="${PROJECT_DIR}/catalog/mcp-gateway-channel-entry.yaml"

if [[ ! -f "$CHANNEL_ENTRY_FILE" ]]; then
  echo "channel entry template not found: $CHANNEL_ENTRY_FILE"
  exit 1
fi

CATALOG_DIR="$(dirname "$CATALOG_FILE")"
mkdir -p "$CATALOG_DIR"

# render bundle into FBC format
echo "rendering bundle: $BUNDLE_IMG"
$OPM render "$BUNDLE_IMG" -o yaml > "$CATALOG_FILE"

# extract bundle name from rendered output
BUNDLE_NAME=$($YQ eval 'select(.schema == "olm.bundle") | .name' "$CATALOG_FILE" | head -1)
if [[ -z "$BUNDLE_NAME" ]]; then
  echo "failed to extract bundle name from rendered output"
  exit 1
fi
echo "bundle name: $BUNDLE_NAME"

# generate package entry
cat >> "$CATALOG_FILE" <<EOF
---
schema: olm.package
package: mcp-gateway
name: mcp-gateway
defaultChannel: $CHANNEL
EOF

# generate channel entry with the actual bundle name
$YQ eval ".entries[0].name = \"$BUNDLE_NAME\" | .name = \"$CHANNEL\"" "$CHANNEL_ENTRY_FILE" >> "$CATALOG_FILE"

echo "catalog generated: $CATALOG_FILE"
