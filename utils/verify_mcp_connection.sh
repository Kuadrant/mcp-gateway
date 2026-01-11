#!/bin/bash
# Usage: ./verify_mcp_connection.sh [GATEWAY_URL] [TOOL_PREFIX]
set -e

GATEWAY_URL="${1:-https://mcp.apps.btofel-netedg-251219.devcluster.openshift.com/mcp}"
TOOL_PREFIX="${2:-local_}"

echo "Verifying connection to $GATEWAY_URL (expecting tools with prefix '$TOOL_PREFIX')..."

cleanup() {
  rm -f sse.log headers.txt tools_resp.json
}
trap cleanup EXIT

# 1. Start SSE Connection
echo "1. Establishing SSE connection..."
curl -k -N -s -H "Accept: text/event-stream" "$GATEWAY_URL" > sse.log 2>&1 &
CURL_PID=$!

# Allow connection to establish
sleep 3

# 2. Extract Initial Session ID (UUID)
if grep -q "endpoint" sse.log; then
    ENDPOINT=$(grep -A 1 "event: endpoint" sse.log | grep "data:" | awk '{print $2}')
    if [ -z "$ENDPOINT" ]; then
        SESSION_ID=$(grep "sessionId=" sse.log | sed 's/.*sessionId=\([^&]*\).*/\1/')
        if [ -n "$SESSION_ID" ]; then
             ENDPOINT="/mcp?sessionId=$SESSION_ID"
        fi
    fi
else
    # Fallback to logs if running against the specific cluster (optional/debug)
    # This part is brittle and specific to the current context, so we'll rely on curl output mostly.
    # But for robustness in this specific dev environment:
    echo "Endpoint not found in curl output, checking logs (best effort)..."
    SESSION_ID=$(kubectl logs -n mcp-system -l component=broker-router --tail=50 2>/dev/null | grep "Gateway client session connected" | tail -n 1 | sed 's/.*gatewaySessionID=\([^ ]*\).*/\1/')
    SESSION_ID=$(echo "$SESSION_ID" | tr -d '\r\n')
    if [ -n "$SESSION_ID" ]; then
        ENDPOINT="/mcp?sessionId=$SESSION_ID"
    fi
fi

if [ -z "$ENDPOINT" ]; then
    echo "FAILURE: Could not establish SSE connection or find session ID."
    echo "Curl output:"
    cat sse.log
    kill $CURL_PID
    exit 1
fi

BASE_URL=$(echo "$GATEWAY_URL" | sed 's|/mcp$||')
POST_URL="${BASE_URL}${ENDPOINT}"
echo "Initial Session URL: $POST_URL"

# 3. Send Initialize & Capture New Session ID (JWT)
echo "2. Sending 'initialize' to upgrade session..."
curl -k -s -D headers.txt -X POST "$POST_URL" \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc": "2.0",
    "method": "initialize",
    "params": {
        "protocolVersion": "2024-11-05",
        "capabilities": {},
        "clientInfo": {"name": "verify-script", "version": "1.0"}
    },
    "id": 1
}' 

# Extract JWT Session ID
JWT_SESSION_ID=$(grep -i "mcp-session-id:" headers.txt | awk '{print $2}' | tr -d '\r\n')

if [ -n "$JWT_SESSION_ID" ]; then
    echo "Success: Received Upgraded Session ID (JWT)."
    # Update POST URL
    POST_URL="${GATEWAY_URL}?sessionId=${JWT_SESSION_ID}"
else
    echo "Warning: No mcp-session-id header received. Continuing with original session ID (might fail if Gateway requires JWT)."
    # If the gateway didn't upgrade, we keep the original session (UUID)
    JWT_SESSION_ID=$(echo "$ENDPOINT" | sed 's/.*sessionId=\([^&]*\).*/\1/')
fi

# 4. List Tools
echo "3. Sending 'tools/list' with Session ID..."
curl -k -s -X POST "$POST_URL" \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $JWT_SESSION_ID" \
  -d '{
    "jsonrpc": "2.0",
    "method": "tools/list",
    "id": 2
}' > tools_resp.json

# 5. Verify Results
if grep -q "$TOOL_PREFIX" tools_resp.json; then
    TOOL_COUNT=$(grep -o "$TOOL_PREFIX" tools_resp.json | wc -l)
    echo -e "\nSUCCESS: Found $TOOL_COUNT tools with prefix '$TOOL_PREFIX'!"
    exit 0
else
    echo -e "\nFAILURE: Did not find tools with prefix '$TOOL_PREFIX'."
    echo "Response:"
    cat tools_resp.json
    exit 1
fi

kill $CURL_PID
