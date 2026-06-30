#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<EOF
Usage: $(basename "$0") --external-url URL --issuer-url URL --client-id ID [--output-dir DIR] [--template-dir DIR]

Generates AuthPolicy YAMLs from templates.

  --external-url   External gateway URL (e.g. https://mcp.example.com:8001)
  --issuer-url     OIDC issuer URL (e.g. https://keycloak.example.com/realms/mcp)
  --client-id      OIDC client ID
  --output-dir     Output directory (default: current directory)
  --template-dir   Directory containing template YAMLs (default: script directory)
EOF
  exit 1
}

EXTERNAL_URL=""
ISSUER_URL=""
CLIENT_ID=""
OUTPUT_DIR="."
TEMPLATE_DIR=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --external-url)  EXTERNAL_URL="$2"; shift 2 ;;
    --issuer-url)    ISSUER_URL="$2"; shift 2 ;;
    --client-id)     CLIENT_ID="$2"; shift 2 ;;
    --output-dir)    OUTPUT_DIR="$2"; shift 2 ;;
    --template-dir)  TEMPLATE_DIR="$2"; shift 2 ;;
    *)               usage ;;
  esac
done

[[ -z "$EXTERNAL_URL" || -z "$ISSUER_URL" || -z "$CLIENT_ID" ]] && usage

# derive host, encoded redirect URI, and cookie security flag from EXTERNAL_URL
EXTERNAL_HOST=$(echo "$EXTERNAL_URL" | sed -E 's|^https?://||; s|/.*||; s|:[0-9]+$||')
REDIRECT_URI_ENCODED=$(printf '%s/auth/callback' "$EXTERNAL_URL" | jq -sRr @uri)
if [[ "$EXTERNAL_URL" == https://* ]]; then
  COOKIE_SECURE="Secure; "
else
  COOKIE_SECURE=""
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
[[ -z "$TEMPLATE_DIR" ]] && TEMPLATE_DIR="$SCRIPT_DIR"
TEMPLATES=(
  authpolicy-callback.yaml
  authpolicy-gateway.yaml
  authpolicy-tokens.yaml
)

mkdir -p "$OUTPUT_DIR"

for tmpl in "${TEMPLATES[@]}"; do
  src="$TEMPLATE_DIR/$tmpl"
  if [[ ! -f "$src" ]]; then
    echo "warning: template $src not found, skipping" >&2
    continue
  fi
  sed \
    -e "s|{{ EXTERNAL_URL }}|${EXTERNAL_URL}|g" \
    -e "s|{{ EXTERNAL_HOST }}|${EXTERNAL_HOST}|g" \
    -e "s|{{ REDIRECT_URI_ENCODED }}|${REDIRECT_URI_ENCODED}|g" \
    -e "s|{{ ISSUER_URL }}|${ISSUER_URL}|g" \
    -e "s|{{ CLIENT_ID }}|${CLIENT_ID}|g" \
    -e "s|{{ COOKIE_SECURE }}|${COOKIE_SECURE}|g" \
    "$src" > "$OUTPUT_DIR/$tmpl"
  echo "wrote $OUTPUT_DIR/$tmpl"
done
