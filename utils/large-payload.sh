#!/bin/bash

# Script to generate and send large JSON-RPC payload to test MCP gateway
# Usage: ./utils/large-payload.sh [size_in_kb] [gateway_url] [session_token]

set -e

SIZE_KB=${1:-7}
GATEWAY_URL=${2:-http://mcp.127-0-0-1.sslip.io:8888/mcp}
SESSION_TOKEN=${3:-}

# Calculate number of characters needed for desired size
# JSON structure adds ~200 bytes overhead
CHARS_NEEDED=$((SIZE_KB * 1024 - 200))

echo "Generating ${SIZE_KB}KB payload..."

# Create temporary file for the request
TEMP_FILE=$(mktemp)
trap "rm -f $TEMP_FILE" EXIT

# Generate the large payload
python3 -c "
import json

payload = {
    'jsonrpc': '2.0',
    'id': 1,
    'method': 'tools/call',
    'params': {
        'name': 'test2_time',
        'arguments': {
            'large_data': 'A' * ${CHARS_NEEDED},
            'metadata': {
                'size_kb': ${SIZE_KB},
                'purpose': 'testing large payload handling',
                'generated_at': '$(date -u +%Y-%m-%dT%H:%M:%SZ)'
            }
        }
    }
}

print(json.dumps(payload, indent=2))
" > "$TEMP_FILE"

# Check actual file size
ACTUAL_SIZE=$(wc -c < "$TEMP_FILE")
ACTUAL_KB=$((ACTUAL_SIZE / 1024))

echo "Generated payload: ${ACTUAL_SIZE} bytes (~${ACTUAL_KB}KB)"
echo "Target URL: ${GATEWAY_URL}"

# Build curl command with appropriate headers
CURL_CMD="curl -X POST \"${GATEWAY_URL}\" -H \"Content-Type: application/json\""

if [ -n "$SESSION_TOKEN" ]; then
    CURL_CMD="${CURL_CMD} -H \"mcp-session-id: ${SESSION_TOKEN}\""
    echo "Using session token: ${SESSION_TOKEN:0:20}..."
else
    echo "Warning: No session token provided, request may fail authentication"
fi

CURL_CMD="${CURL_CMD} -d @\"${TEMP_FILE}\" -v"

echo ""
echo "Sending request..."
echo ""

eval "$CURL_CMD"

echo ""
echo "Request complete"
